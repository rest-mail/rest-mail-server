#!/usr/bin/env python3
"""Stage 3 of the installed-cluster check (#1750): a reference mail server in the cluster.

It deploys a reference Postfix/Dovecot (docker-mailserver, pinned by digest)
into its own namespace, next to a running helm/restmail install, and gives
both a DNS of their own in the cluster's CoreDNS, so each side finds the
other's MX the way mail between two real domains does:

  reference.test     MX mail.reference.test     A  the reference's Service
  <mail domain>      MX <mail host>             A  the install's SMTP gateway Service

With --peer-namespace, a second REST Mail install (same chart and images,
values-ci-peer.yaml) gets a zone too. A peer finds its MX host on one address
for SMTP (port 25, where the EHLO keyword RESTMAIL says it speaks REST) and
HTTPS (443, /.well-known/restmail and /restmail/messages), so that address is
a Service of this script's (<peer namespace>/mx) with no selector: its
EndpointSlices send 25 to the peer's SMTP gateway pod and 443 to the ingress
controller, which serves the peer's Ingress:

  <peer domain>      MX <peer host>             A  <peer namespace>/mx

For MTA-STS (RFC 8461) a policy host, mta-sts.<domain>, serves
/.well-known/mta-sts.txt over HTTPS for two domains the reference receives
for: reference.test, whose policy names its MX, and sts-mismatch.test, whose
policy names an MX the domain does not have. Both MX hosts are the
reference's Service; cluster_e2e.py publishes each _mta-sts TXT record when
its step runs (mta-sts-enforce, mta-sts-mismatch):

  reference.test     mta-sts  A  the policy host's Service
  sts-mismatch.test  MX mail.sts-mismatch.test  A  the reference's Service
                     mta-sts  A  the policy host's Service

The reference's certificate (the Secret reference-mail-tls) names every one
of those hosts. The workflow issues it from the run's CA, which REST Mail
trusts, so the MTA-STS checks verify it; without that Secret this script
makes a self-signed one, which an enforce policy rejects.

The chart's NetworkPolicy lets the SMTP gateway reach port 25 anywhere; a
pod-to-pod hop arrives on the peer gateway's container port, so a policy of
this script's opens that one port from the first install's gateway to the
peer's namespace. That stands in for the Internet between two servers.

Then scripts/cluster_e2e.py sends mail both ways (steps mail-out, mail-in,
pop3-retrieve), publishes the first install's DKIM key in its zone
(publish_records, step dkim-key) and delivers to the peer (restmail-peer). The reference is a fixed point to test REST Mail's wiring
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

# The MTA-STS policy host: its Deployment, Service and ConfigMap (the server
# script and a policy file per policy host name).
STS_NAME = "mta-sts"
STS_PORT = 443
MISMATCH_DOMAIN = "sts-mismatch.test"
MISMATCH_HOST = "mail." + MISMATCH_DOMAIN

# The hosts the reference's certificate names.
TLS_HOSTS = (REFERENCE_HOST, "mta-sts." + REFERENCE_DOMAIN, MISMATCH_HOST, "mta-sts." + MISMATCH_DOMAIN)

# Serves /etc/mta-sts/policies/<Host header> at the well-known path, over TLS
# with the reference's certificate. It logs each request, which the check
# reads back as the proof REST Mail fetched the policy.
STS_SERVER = '''import http.server, pathlib, ssl
POLICIES = pathlib.Path("/etc/mta-sts/policies")
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        host = (self.headers.get("Host") or "").split(":")[0].lower().rstrip(".")
        policy = POLICIES / host
        if self.path != "/.well-known/mta-sts.txt" or not host or "/" in host or not policy.is_file():
            self.send_error(404)
            return
        body = policy.read_bytes()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, fmt, *args):
        print("mta-sts %s host=%s %s" % (self.address_string(), self.headers.get("Host"), fmt % args), flush=True)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.minimum_version = ssl.TLSVersion.TLSv1_2
context.load_cert_chain("/etc/reference-tls/tls.crt", "/etc/reference-tls/tls.key")
server = http.server.ThreadingHTTPServer(("", PORT), Handler)
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
'''.replace("PORT", str(STS_PORT), 1)

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


# Postfix settings the reference applies over its own (docker-mailserver reads
# postfix-main.cf from its config directory). REST Mail greylists a new
# sender/recipient/IP triple (internal/pipeline/templates.go, 5 minutes), so
# the reference's first delivery to it is deferred with a 451, as it would be
# for any first-time sender. Postfix's default backoff is 300s to 4000s, with
# a queue run every 300s: the retry after the greylist delay could land well
# past 10 minutes. These make it retry every minute, like a busy MTA would.
POSTFIX_MAIN = ("queue_run_delay = 30s\n"
                "minimal_backoff_time = 30s\n"
                "maximal_backoff_time = 60s\n")


def accounts_line(address: str, password: str) -> str:
    """One postfix-accounts.cf line. Dovecot reads the {PLAIN} scheme as is."""
    if not re.fullmatch(r"[^@|\s]+@[^@|\s]+", address):
        raise ValueError(f"not an address: {address!r}")
    if not password or re.search(r"[|:\s]", password):
        raise ValueError("the password must be non-empty, without '|', ':' or whitespace")
    return f"{address}|{{PLAIN}}{password}\n"


# Between these markers in a zone file are the records publish_records added.
RECORDS_BEGIN, RECORDS_END = "; BEGIN published records", "; END published records"


def zone_file(zone: str, mail_host: str, mail_ip: str, serial: int = 1, hosts: dict[str, str] | None = None) -> str:
    """A zone with an SOA, an MX to mail_host and mail_host's address, and an
    A record per hosts entry (label relative to the zone: address).

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
    extra = ""
    for name, ip in sorted((hosts or {}).items()):
        if not re.fullmatch(r"[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?", name) or name in ("ns", label):
            raise ValueError(f"not a host label this zone can add: {name!r}")
        if not re.fullmatch(r"\d{1,3}(\.\d{1,3}){3}", ip):
            raise ValueError(f"not an IPv4 address for {name}: {ip!r}")
        extra += f"{name} IN A {ip}\n"
    return (f"$ORIGIN {zone}.\n"
            f"$TTL 30\n"
            f"@ IN SOA ns.{zone}. hostmaster.{zone}. {serial} 30 30 300 30\n"
            f"@ IN NS ns.{zone}.\n"
            f"@ IN MX 10 {mail_host}.\n"
            f"ns IN A {mail_ip}\n"
            f"{label} IN A {mail_ip}\n"
            + extra)


def sts_policy(mx: list[str], mode: str = "enforce", max_age: int = 86400) -> str:
    """An MTA-STS policy file (RFC 8461 3.2), CRLF line endings."""
    if mode not in ("enforce", "testing", "none") or not mx or max_age < 0:
        raise ValueError(f"not a policy: mode={mode!r} mx={mx!r} max_age={max_age}")
    for host in mx:
        if not re.fullmatch(r"(\*\.)?[A-Za-z0-9.-]+", host):
            raise ValueError(f"not an MX pattern: {host!r}")
    lines = ["version: STSv1", f"mode: {mode}", *[f"mx: {host}" for host in mx], f"max_age: {max_age}"]
    return "\r\n".join(lines) + "\r\n"


# Each policy host and the policy it serves: reference.test's names its MX,
# sts-mismatch.test's names the reference's host, which is not that domain's MX.
STS_POLICIES = {
    "mta-sts." + REFERENCE_DOMAIN: sts_policy([REFERENCE_HOST]),
    "mta-sts." + MISMATCH_DOMAIN: sts_policy([REFERENCE_HOST]),
}


def txt_rdata(value: str) -> str:
    """A TXT record's data: the value in quoted strings of at most 255 bytes each.

    A DKIM key is longer than one string may be (RFC 1035 3.3.14); a reader
    joins the strings back together (RFC 6376 3.6.2.2).
    """
    if not value or not value.isascii() or any(c in value for c in '"\\\n'):
        raise ValueError(f"not a TXT value this zone writer quotes: {value!r}")
    return " ".join(f'"{value[i:i + 255]}"' for i in range(0, len(value), 255))


def zone_with_records(text: str, records: dict[str, str]) -> str:
    """The zone file with these TXT records (owner name relative to the zone,
    value), replacing those added before, and its SOA serial one higher."""
    kept = re.sub(rf"\n*{re.escape(RECORDS_BEGIN)}.*?{re.escape(RECORDS_END)}\n*", "\n", text,
                  flags=re.S).rstrip("\n")
    soa = re.search(r"^(@ IN SOA \S+ \S+ )(\d+)( .*)$", kept, flags=re.M)
    if not soa:
        raise ValueError("the zone file has no SOA this script wrote")
    kept = kept[:soa.start()] + soa.group(1) + str(int(soa.group(2)) + 1) + soa.group(3) + kept[soa.end():]
    lines = [RECORDS_BEGIN]
    for name, value in sorted(records.items()):
        if not re.fullmatch(r"[A-Za-z0-9_]([A-Za-z0-9_.-]*[A-Za-z0-9])?", name):
            raise ValueError(f"not a relative owner name: {name!r}")
        lines.append(f"{name} IN TXT {txt_rdata(value)}")
    lines.append(RECORDS_END)
    return kept + "\n" + "\n".join(lines) + "\n"


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
                    # The accounts file and the Postfix overrides go into a
                    # writable directory: the image writes its other config
                    # files next to them.
                    "initContainers": [{
                        "name": "accounts", "image": image,
                        "command": ["cp", "/secret/postfix-accounts.cf", "/secret/postfix-main.cf", config + "/"],
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
                                                       "items": [{"key": key, "path": key} for key in
                                                                 ("postfix-accounts.cf", "postfix-main.cf")]}},
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


def sts_manifests(namespace: str, policies: dict[str, str], image: str = REFERENCE_IMAGE) -> dict:
    """The MTA-STS policy host: a ConfigMap (server script, policies), a
    Deployment running it with the reference's certificate, and its Service.

    It runs on the reference's image, already pulled, for its python3.
    """
    labels = {"app.kubernetes.io/name": STS_NAME, "app.kubernetes.io/part-of": "restmail-cluster-e2e"}
    data = {"serve.py": STS_SERVER}
    items = [{"key": "serve.py", "path": "serve.py"}]
    for index, (host, policy) in enumerate(sorted(policies.items())):
        if not re.fullmatch(r"mta-sts\.[a-z0-9.-]+", host):
            raise ValueError(f"not a policy host: {host!r}")
        key = f"policy-{index}"
        data[key] = policy
        items.append({"key": key, "path": f"policies/{host}"})
    config_map = {"apiVersion": "v1", "kind": "ConfigMap",
                  "metadata": {"name": STS_NAME, "namespace": namespace, "labels": labels}, "data": data}
    deployment = {
        "apiVersion": "apps/v1", "kind": "Deployment",
        "metadata": {"name": STS_NAME, "namespace": namespace, "labels": labels},
        "spec": {
            "replicas": 1,
            "selector": {"matchLabels": labels},
            "template": {
                "metadata": {"labels": labels},
                "spec": {
                    "containers": [{
                        "name": "policy", "image": image,
                        "command": ["python3", "-I", "/etc/mta-sts/serve.py"],
                        "ports": [{"name": "https", "containerPort": STS_PORT}],
                        "readinessProbe": {"tcpSocket": {"port": STS_PORT}, "periodSeconds": 2,
                                           "failureThreshold": 60},
                        "volumeMounts": [{"name": "config", "mountPath": "/etc/mta-sts", "readOnly": True},
                                         {"name": "tls", "mountPath": "/etc/reference-tls", "readOnly": True}],
                    }],
                    "volumes": [{"name": "config", "configMap": {"name": STS_NAME, "items": items}},
                                {"name": "tls", "secret": {"secretName": TLS_SECRET}}],
                },
            },
        },
    }
    service = {
        "apiVersion": "v1", "kind": "Service",
        "metadata": {"name": STS_NAME, "namespace": namespace, "labels": labels},
        "spec": {"selector": labels, "ports": [{"name": "https", "port": STS_PORT, "targetPort": STS_PORT}]},
    }
    return {"apiVersion": "v1", "kind": "List", "items": [config_map, deployment, service]}


PEER_SERVICE = "mx"
PEER_POLICY = "cluster-e2e-peer-smtp"


def peer_front(namespace: str, smtp_ip: str, smtp_port: int, https_ip: str, https_port: int = 443) -> dict:
    """The peer's MX address: a Service with no selector, and an EndpointSlice
    per port, 25 to the peer's SMTP gateway pod, 443 to the ingress controller."""
    labels = {"app.kubernetes.io/part-of": "restmail-cluster-e2e"}
    service = {
        "apiVersion": "v1", "kind": "Service",
        "metadata": {"name": PEER_SERVICE, "namespace": namespace, "labels": labels},
        "spec": {"ports": [{"name": "smtp", "port": 25, "targetPort": smtp_port},
                           {"name": "https", "port": 443, "targetPort": https_port}]},
    }
    slices = []
    for name, ip, port in (("smtp", smtp_ip, smtp_port), ("https", https_ip, https_port)):
        if not re.fullmatch(r"\d{1,3}(\.\d{1,3}){3}", ip):
            raise ValueError(f"not an IPv4 address for {name}: {ip!r}")
        slices.append({
            "apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
            "metadata": {"name": f"{PEER_SERVICE}-{name}", "namespace": namespace,
                         "labels": {**labels, "kubernetes.io/service-name": PEER_SERVICE}},
            "addressType": "IPv4",
            "ports": [{"name": name, "port": port, "protocol": "TCP"}],
            "endpoints": [{"addresses": [ip], "conditions": {"ready": True}}],
        })
    return {"apiVersion": "v1", "kind": "List", "items": [service, *slices]}


def peer_egress_policy(namespace: str, release: str, peer_namespace: str, port: int) -> dict:
    """Lets the install's SMTP gateway reach the peer gateway's container port."""
    return {
        "apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
        "metadata": {"name": PEER_POLICY, "namespace": namespace,
                     "labels": {"app.kubernetes.io/part-of": "restmail-cluster-e2e"}},
        "spec": {
            "podSelector": {"matchLabels": {"app.kubernetes.io/instance": release,
                                            "app.kubernetes.io/component": "smtp-gateway"}},
            "policyTypes": ["Egress"],
            "egress": [{"to": [{"namespaceSelector": {"matchLabels": {
                "kubernetes.io/metadata.name": peer_namespace}}}],
                "ports": [{"protocol": "TCP", "port": port}]}],
        },
    }


def secret_manifest(namespace: str, name: str, data: dict[str, str], kind: str = "Opaque") -> dict:
    return {"apiVersion": "v1", "kind": "Secret", "type": kind,
            "metadata": {"name": name, "namespace": namespace},
            "data": {k: base64.b64encode(v.encode()).decode() for k, v in data.items()}}


class Kubectl:
    def __init__(self, binary: str, error=SystemExit):
        self.binary, self.error = binary, error

    def __call__(self, *args: str, stdin: str | None = None) -> str:
        out = subprocess.run([self.binary, *args], input=stdin, capture_output=True, text=True)
        if out.returncode != 0:
            raise self.error(f"kubectl {' '.join(args)}: {out.stderr.strip()}")
        return out.stdout

    def apply(self, manifest: dict) -> None:
        print(self("apply", "-f", "-", stdin=json.dumps(manifest)).strip())

    def cluster_ip(self, namespace: str, service: str) -> str:
        ip = self("-n", namespace, "get", "service", service, "-o", "jsonpath={.spec.clusterIP}").strip()
        if not re.fullmatch(r"\d{1,3}(\.\d{1,3}){3}", ip):
            raise self.error(f"service {namespace}/{service} has no IPv4 ClusterIP ({ip!r})")
        return ip

    def pod_ip(self, namespace: str, selector: str) -> str:
        ip = self("-n", namespace, "get", "pods", "-l", selector, "--field-selector=status.phase=Running",
                  "-o", "jsonpath={.items[0].status.podIP}").strip()
        if not re.fullmatch(r"\d{1,3}(\.\d{1,3}){3}", ip):
            raise self.error(f"no running pod in {namespace} matches {selector} ({ip!r})")
        return ip


def apply_coredns(kubectl: Kubectl, zones: dict[str, str], timeout: str = "300s") -> None:
    """Serve these zone files (zone: text) from CoreDNS, and wait until it does.

    Zones already served and not named keep their files. CoreDNS is restarted
    rather than left to reload: the file plugin re-reads a zone only when its
    serial grows, and only after the kubelet has synced the ConfigMap.
    """
    config_map = json.loads(kubectl("-n", "kube-system", "get", "configmap", "coredns", "-o", "json"))
    data = config_map.setdefault("data", {})
    served = re.findall(r"^(\S+?):53 \{\n    errors\n    file ", data.get("Corefile", ""), flags=re.M)
    data["Corefile"] = corefile_with_zones(data.get("Corefile", ""), list(dict.fromkeys([*served, *zones])))
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
        kubectl("-n", "kube-system", "rollout", "restart", "deployment/coredns")
    kubectl("-n", "kube-system", "rollout", "status", "deployment/coredns", f"--timeout={timeout}")


def publish_records(kubectl: Kubectl, zone: str, records: dict[str, str], timeout: str = "300s") -> str:
    """Add TXT records to a zone this script serves; return the new zone file."""
    config_map = json.loads(kubectl("-n", "kube-system", "get", "configmap", "coredns", "-o", "json"))
    text = (config_map.get("data") or {}).get(zone_key(zone))
    if not text:
        raise kubectl.error(f"CoreDNS serves no zone file for {zone}: run scripts/cluster_reference.py first")
    text = zone_with_records(text, records)
    apply_coredns(kubectl, {zone: text}, timeout)
    return text


def self_signed(*hosts: str) -> tuple[str, str]:
    """A throwaway certificate and key for these hosts, from openssl."""
    with tempfile.TemporaryDirectory() as tmp:
        key, crt = Path(tmp, "tls.key"), Path(tmp, "tls.crt")
        names = ",".join(f"DNS:{host}" for host in hosts)
        subprocess.run(["openssl", "req", "-x509", "-nodes", "-newkey", "rsa:2048", "-days", "2",
                        "-subj", f"/CN={hosts[0]}", "-addext", f"subjectAltName={names}",
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
    parser.add_argument("--peer-namespace", default=os.environ.get("PEER_NAMESPACE", ""),
                        help="where the second install runs (values-ci-peer.yaml); empty: none")
    parser.add_argument("--peer-release", default=os.environ.get("PEER_RELEASE", "restmail"))
    parser.add_argument("--peer-domain", default=os.environ.get("PEER_MAIL_DOMAIN", ""))
    parser.add_argument("--peer-host", default=os.environ.get("PEER_MAIL_HOST", ""))
    parser.add_argument("--peer-smtp-port", type=int, default=10025,
                        help="the peer SMTP gateway's container port (smtpGateway.containerPorts.inbound)")
    parser.add_argument("--ingress-namespace", default="ingress-nginx",
                        help="where the ingress controller that serves the peer's Ingress runs")
    parser.add_argument("--ingress-selector", default="app.kubernetes.io/component=controller")
    parser.add_argument("--kubectl", default=os.environ.get("KUBECTL", "kubectl"))
    parser.add_argument("--timeout", default="300s", help="how long to wait for each rollout")
    args = parser.parse_args(argv)
    if args.peer_namespace and not (args.peer_domain and args.peer_host):
        parser.error("--peer-namespace needs --peer-domain and --peer-host")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    kubectl = Kubectl(args.kubectl)
    ns = args.namespace
    kubectl.apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": ns}})

    address, password = f"{REFERENCE_USER}@{REFERENCE_DOMAIN}", secrets.token_hex(16)
    kubectl.apply(secret_manifest(ns, SECRET, {"address": address, "password": password,
                                               "postfix-accounts.cf": accounts_line(address, password),
                                               "postfix-main.cf": POSTFIX_MAIN}))
    # The workflow issues this Secret from the run's CA; without it, a
    # self-signed certificate (mail-out still works, MTA-STS enforce cannot).
    if not kubectl("-n", ns, "get", "secret", TLS_SECRET, "--ignore-not-found", "-o", "name").strip():
        crt, key = self_signed(*TLS_HOSTS)
        kubectl.apply(secret_manifest(ns, TLS_SECRET, {"tls.crt": crt, "tls.key": key}, "kubernetes.io/tls"))
    kubectl.apply(manifests(ns))
    kubectl.apply(sts_manifests(ns, STS_POLICIES))

    # DNS for both domains, pointing at the two Services. The ClusterIPs are
    # known once the Services exist, which the install and the apply above made.
    reference_ip, sts_ip = kubectl.cluster_ip(ns, SERVICE), kubectl.cluster_ip(ns, STS_NAME)
    zones = {
        REFERENCE_DOMAIN: zone_file(REFERENCE_DOMAIN, REFERENCE_HOST, reference_ip, hosts={"mta-sts": sts_ip}),
        MISMATCH_DOMAIN: zone_file(MISMATCH_DOMAIN, MISMATCH_HOST, reference_ip, hosts={"mta-sts": sts_ip}),
        args.mail_domain: zone_file(args.mail_domain, args.mail_host,
                                    kubectl.cluster_ip(args.restmail_namespace, f"{args.release}-smtp-gateway")),
    }
    if args.peer_namespace:
        peer = args.peer_namespace
        smtp_ip = kubectl.pod_ip(peer, f"app.kubernetes.io/instance={args.peer_release},"
                                       "app.kubernetes.io/component=smtp-gateway")
        https_ip = kubectl.pod_ip(args.ingress_namespace, args.ingress_selector)
        kubectl.apply(peer_front(peer, smtp_ip, args.peer_smtp_port, https_ip))
        kubectl.apply(peer_egress_policy(args.restmail_namespace, args.release, peer, args.peer_smtp_port))
        zones[args.peer_domain] = zone_file(args.peer_domain, args.peer_host,
                                            kubectl.cluster_ip(peer, PEER_SERVICE))
    apply_coredns(kubectl, zones, args.timeout)
    kubectl("-n", ns, "rollout", "status", "deployment/reference-mail", f"--timeout={args.timeout}")
    kubectl("-n", ns, "rollout", "status", f"deployment/{STS_NAME}", f"--timeout={args.timeout}")
    print(f"reference {address} at {REFERENCE_HOST}, {args.mail_domain} MX {args.mail_host}"
          + (f", {args.peer_domain} MX {args.peer_host}" if args.peer_namespace else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
