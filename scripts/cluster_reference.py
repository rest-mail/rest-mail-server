#!/usr/bin/env python3
"""Stage 3 of the installed-cluster check (#1750): a reference mail server in the cluster.

It deploys a reference Postfix/Dovecot (docker-mailserver, pinned by digest)
into its own namespace, next to a running helm/restmail install, and gives
both a DNS of their own in the cluster's CoreDNS, so each side finds the
other's MX the way mail between two real domains does:

  reference.test     MX mail.reference.test     A  the reference's Service
  <mail domain>      MX <mail host>             A  the install's SMTP gateway Service

Then scripts/cluster_e2e.py sends mail both ways (steps mail-out, mail-in,
pop3-retrieve). The reference is a fixed point to test REST Mail's wiring
against, not a subject: its spam, DKIM, DMARC and SPF checks are off, so
only REST Mail's sending, receiving and gateways decide a result.

The reference account's address and password are in the Secret
<namespace>/reference-mail (keys address, password), where cluster_e2e.py
reads them.

It changes cluster-wide DNS (kube-system/coredns), so run it on a throwaway
cluster: cluster-e2e.yml's kind cluster, or by hand on one like it.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import secrets
import subprocess
import sys
import tempfile
from pathlib import Path

# docker-mailserver 15.1.0, pinned by its manifest-list digest
# (ghcr.io/docker-mailserver/docker-mailserver). Bump deliberately.
REFERENCE_IMAGE = ("ghcr.io/docker-mailserver/docker-mailserver:15.1.0"
                   "@sha256:af51b15dd3fc72153c0e90eb7692bb5e3a463212d87959a80fa7aa89b617d44a")

REFERENCE_DOMAIN = "reference.test"
REFERENCE_HOST = "mail." + REFERENCE_DOMAIN
REFERENCE_USER = "reference"
SECRET = "reference-mail"
TLS_SECRET = "reference-mail-tls"
SERVICE = "mail"

# What the reference listens on: inbound SMTP, authenticated submission
# (STARTTLS) and IMAP over implicit TLS.
PORTS = {"smtp": 25, "submission": 587, "imaps": 993}

# Between these markers is the Corefile block this script owns; a second run
# replaces it rather than adding another.
BEGIN, END = "# BEGIN restmail-cluster-e2e zones", "# END restmail-cluster-e2e zones"

# Everything a reference does besides moving mail is off. Each one would test
# docker-mailserver, or need DNS the cluster does not have.
REFERENCE_ENV = {
    "OVERRIDE_HOSTNAME": REFERENCE_HOST,
    "SSL_TYPE": "manual",
    "SSL_CERT_PATH": "/etc/reference-tls/tls.crt",
    "SSL_KEY_PATH": "/etc/reference-tls/tls.key",
    "ENABLE_RSPAMD": "0",
    "ENABLE_OPENDKIM": "0",
    "ENABLE_OPENDMARC": "0",
    "ENABLE_POLICYD_SPF": "0",
    "ENABLE_AMAVIS": "0",
    "ENABLE_CLAMAV": "0",
    "ENABLE_SPAMASSASSIN": "0",
    "ENABLE_FAIL2BAN": "0",
    "ENABLE_POSTGREY": "0",
    "ENABLE_DNSBL": "0",
    "ENABLE_POP3": "0",
    "ENABLE_UPDATE_CHECK": "0",
    "POSTSCREEN_ACTION": "ignore",
    "SPOOF_PROTECTION": "0",
    "PERMIT_DOCKER": "none",
    "LOG_LEVEL": "info",
}


def accounts_line(address: str, password: str) -> str:
    """One postfix-accounts.cf line. Dovecot reads the {PLAIN} scheme as is."""
    if not re.fullmatch(r"[^@|\s]+@[^@|\s]+", address):
        raise ValueError(f"not an address: {address!r}")
    if not password or re.search(r"[|:\s]", password):
        raise ValueError("the password must be non-empty, without '|', ':' or whitespace")
    return f"{address}|{{PLAIN}}{password}\n"


def zone_file(zone: str, mail_host: str, mail_ip: str) -> str:
    """A zone with an SOA, an MX to mail_host and mail_host's address.

    mail_host is inside the zone. CoreDNS's file plugin answers NXDOMAIN and
    NODATA from it properly, which DANE (TLSA) and MTA-STS (TXT) lookups on
    both sides need: a SERVFAIL there would defer the mail.
    """
    zone, mail_host = zone.rstrip("."), mail_host.rstrip(".")
    if not mail_host.endswith("." + zone):
        raise ValueError(f"{mail_host} is not inside {zone}")
    if not re.fullmatch(r"\d{1,3}(\.\d{1,3}){3}", mail_ip):
        raise ValueError(f"not an IPv4 address: {mail_ip!r}")
    label = mail_host[: -len(zone) - 1]
    return (f"$ORIGIN {zone}.\n"
            f"$TTL 30\n"
            f"@ IN SOA ns.{zone}. hostmaster.{zone}. 1 30 30 300 30\n"
            f"@ IN NS ns.{zone}.\n"
            f"@ IN MX 10 {mail_host}.\n"
            f"ns IN A {mail_ip}\n"
            f"{label} IN A {mail_ip}\n")


def zone_key(zone: str) -> str:
    """The ConfigMap key (and file under /etc/coredns) of a zone's file."""
    return "zone." + zone.rstrip(".") + ".db"


def corefile_with_zones(corefile: str, zones: list[str]) -> str:
    """The Corefile with a server block per zone, answered from its zone file.

    Any block this script added before is replaced, so it can run twice.
    """
    kept = re.sub(rf"\n*{re.escape(BEGIN)}.*?{re.escape(END)}\n*", "\n", corefile, flags=re.S).rstrip("\n")
    blocks = [BEGIN]
    for zone in zones:
        zone = zone.rstrip(".")
        blocks += [f"{zone}:53 {{", "    errors", f"    file /etc/coredns/{zone_key(zone)} {zone}", "}"]
    blocks.append(END)
    return kept + "\n" + "\n".join(blocks) + "\n"


def config_volume_patch(deployment: dict) -> list[dict]:
    """A JSON patch that mounts every key of CoreDNS's ConfigMap, not only the Corefile.

    kind's CoreDNS mounts the coredns ConfigMap with items: [Corefile], so a
    zone file added to the ConfigMap would not appear. Empty when every key is
    already mounted.
    """
    volumes = deployment["spec"]["template"]["spec"].get("volumes") or []
    for index, volume in enumerate(volumes):
        config_map = volume.get("configMap") or {}
        if config_map.get("name") == "coredns":
            if "items" in config_map:
                return [{"op": "remove", "path": f"/spec/template/spec/volumes/{index}/configMap/items"}]
            return []
    raise ValueError("the CoreDNS Deployment mounts no coredns ConfigMap")


def manifests(namespace: str, image: str = REFERENCE_IMAGE) -> dict:
    """The reference's Deployment and Service, as one kubectl List."""
    labels = {"app.kubernetes.io/name": "reference-mail", "app.kubernetes.io/part-of": "restmail-cluster-e2e"}
    config = "/tmp/docker-mailserver"
    deployment = {
        "apiVersion": "apps/v1", "kind": "Deployment",
        "metadata": {"name": "reference-mail", "namespace": namespace, "labels": labels},
        "spec": {
            "replicas": 1,
            "selector": {"matchLabels": labels},
            "template": {
                "metadata": {"labels": labels},
                "spec": {
                    "hostname": "mail",
                    # The accounts file goes into a writable directory: the
                    # image writes its other config files next to it.
                    "initContainers": [{
                        "name": "accounts", "image": image,
                        "command": ["cp", "/secret/postfix-accounts.cf", config + "/postfix-accounts.cf"],
                        "volumeMounts": [{"name": "account", "mountPath": "/secret", "readOnly": True},
                                         {"name": "config", "mountPath": config}],
                    }],
                    "containers": [{
                        "name": "mailserver", "image": image,
                        "env": [{"name": k, "value": v} for k, v in REFERENCE_ENV.items()],
                        "ports": [{"name": name, "containerPort": port} for name, port in PORTS.items()],
                        "readinessProbe": {"tcpSocket": {"port": PORTS["submission"]},
                                           "periodSeconds": 5, "failureThreshold": 60},
                        "volumeMounts": [{"name": "config", "mountPath": config},
                                         {"name": "tls", "mountPath": "/etc/reference-tls", "readOnly": True}],
                    }],
                    "volumes": [
                        {"name": "account", "secret": {"secretName": SECRET,
                                                       "items": [{"key": "postfix-accounts.cf",
                                                                  "path": "postfix-accounts.cf"}]}},
                        {"name": "tls", "secret": {"secretName": TLS_SECRET}},
                        {"name": "config", "emptyDir": {}},
                    ],
                },
            },
        },
    }
    service = {
        "apiVersion": "v1", "kind": "Service",
        "metadata": {"name": SERVICE, "namespace": namespace, "labels": labels},
        "spec": {"selector": labels,
                 "ports": [{"name": name, "port": port, "targetPort": port} for name, port in PORTS.items()]},
    }
    return {"apiVersion": "v1", "kind": "List", "items": [deployment, service]}


def secret_manifest(namespace: str, name: str, data: dict[str, str], kind: str = "Opaque") -> dict:
    return {"apiVersion": "v1", "kind": "Secret", "type": kind,
            "metadata": {"name": name, "namespace": namespace},
            "data": {k: base64.b64encode(v.encode()).decode() for k, v in data.items()}}


class Kubectl:
    def __init__(self, binary: str):
        self.binary = binary

    def __call__(self, *args: str, stdin: str | None = None) -> str:
        out = subprocess.run([self.binary, *args], input=stdin, capture_output=True, text=True)
        if out.returncode != 0:
            raise SystemExit(f"kubectl {' '.join(args)}: {out.stderr.strip()}")
        return out.stdout

    def apply(self, manifest: dict) -> None:
        print(self("apply", "-f", "-", stdin=json.dumps(manifest)).strip())

    def cluster_ip(self, namespace: str, service: str) -> str:
        ip = self("-n", namespace, "get", "service", service, "-o", "jsonpath={.spec.clusterIP}").strip()
        if not re.fullmatch(r"\d{1,3}(\.\d{1,3}){3}", ip):
            raise SystemExit(f"service {namespace}/{service} has no IPv4 ClusterIP ({ip!r})")
        return ip


def self_signed(host: str) -> tuple[str, str]:
    """A throwaway certificate and key for host, from openssl."""
    with tempfile.TemporaryDirectory() as tmp:
        key, crt = Path(tmp, "tls.key"), Path(tmp, "tls.crt")
        subprocess.run(["openssl", "req", "-x509", "-nodes", "-newkey", "rsa:2048", "-days", "2",
                        "-subj", f"/CN={host}", "-addext", f"subjectAltName=DNS:{host}",
                        "-keyout", str(key), "-out", str(crt)], check=True, capture_output=True)
        return crt.read_text(), key.read_text()


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="cluster_reference.py", description=__doc__.splitlines()[0])
    parser.add_argument("--namespace", default=os.environ.get("REFERENCE_NAMESPACE", "reference"),
                        help="where the reference runs (made if missing)")
    parser.add_argument("--restmail-namespace", default=os.environ.get("NAMESPACE", "restmail"))
    parser.add_argument("--release", default=os.environ.get("RELEASE", "restmail"))
    parser.add_argument("--mail-domain", required=True, help="the install's mail domain (mailserver.domain)")
    parser.add_argument("--mail-host", required=True, help="the install's mail host (mailserver.hostname)")
    parser.add_argument("--kubectl", default=os.environ.get("KUBECTL", "kubectl"))
    parser.add_argument("--timeout", default="300s", help="how long to wait for each rollout")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    kubectl = Kubectl(args.kubectl)
    ns = args.namespace
    kubectl.apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": ns}})

    address, password = f"{REFERENCE_USER}@{REFERENCE_DOMAIN}", secrets.token_hex(16)
    kubectl.apply(secret_manifest(ns, SECRET, {"address": address, "password": password,
                                               "postfix-accounts.cf": accounts_line(address, password)}))
    crt, key = self_signed(REFERENCE_HOST)
    kubectl.apply(secret_manifest(ns, TLS_SECRET, {"tls.crt": crt, "tls.key": key}, "kubernetes.io/tls"))
    kubectl.apply(manifests(ns))

    # DNS for both domains, pointing at the two Services. The ClusterIPs are
    # known once the Services exist, which the install and the apply above made.
    zones = {
        REFERENCE_DOMAIN: zone_file(REFERENCE_DOMAIN, REFERENCE_HOST, kubectl.cluster_ip(ns, SERVICE)),
        args.mail_domain: zone_file(args.mail_domain, args.mail_host,
                                    kubectl.cluster_ip(args.restmail_namespace, f"{args.release}-smtp-gateway")),
    }
    config_map = json.loads(kubectl("-n", "kube-system", "get", "configmap", "coredns", "-o", "json"))
    data = config_map.setdefault("data", {})
    data["Corefile"] = corefile_with_zones(data.get("Corefile", ""), list(zones))
    for zone, text in zones.items():
        data[zone_key(zone)] = text
        print(f"zone {zone}:\n{text}")
    for key in ("resourceVersion", "uid", "creationTimestamp", "managedFields", "annotations"):
        config_map["metadata"].pop(key, None)
    kubectl.apply(config_map)
    deployment = json.loads(kubectl("-n", "kube-system", "get", "deployment", "coredns", "-o", "json"))
    patch = config_volume_patch(deployment)
    if patch:
        kubectl("-n", "kube-system", "patch", "deployment", "coredns", "--type=json", "-p", json.dumps(patch))
    else:
        # The reload plugin picks the Corefile up in its own time; a restart
        # makes it now.
        kubectl("-n", "kube-system", "rollout", "restart", "deployment/coredns")
    kubectl("-n", "kube-system", "rollout", "status", "deployment/coredns", f"--timeout={args.timeout}")
    kubectl("-n", ns, "rollout", "status", "deployment/reference-mail", f"--timeout={args.timeout}")
    print(f"reference {address} at {REFERENCE_HOST}, {args.mail_domain} MX {args.mail_host}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
