"""The stage-3 reference mail server's deployment (#1750), without a cluster.

What cluster-e2e.yml applies (the manifests, the CoreDNS change, the account)
is built here as data; these check that data. Whether the reference and REST
Mail then exchange mail only the workflow's kind cluster shows.
"""

import importlib.util
import re
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / f"scripts/{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


reference = load("cluster_reference")
cluster_e2e = load("cluster_e2e")

# kind's CoreDNS Corefile, as kind v0.33 writes it.
KIND_COREFILE = """.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30
    loop
    reload
    loadbalance
}
"""


class AccountTests(unittest.TestCase):
    def test_plain_scheme_line(self):
        self.assertEqual(reference.accounts_line("reference@reference.test", "abc123"),
                         "reference@reference.test|{PLAIN}abc123\n")

    def test_refuses_what_would_break_the_file(self):
        for address, password in (("nobody", "p"), ("a@b.test", ""), ("a@b.test", "p|q"),
                                  ("a@b.test", "p:q"), ("a@b.test", "p q")):
            with self.subTest(address=address, password=password), self.assertRaises(ValueError):
                reference.accounts_line(address, password)


class ZoneTests(unittest.TestCase):
    def test_zone_has_soa_mx_and_the_mail_hosts_address(self):
        text = reference.zone_file("restmail-ci.test", "mail.restmail-ci.test", "10.96.4.5")
        self.assertIn("$ORIGIN restmail-ci.test.\n", text)
        self.assertRegex(text, r"(?m)^@ IN SOA ")
        self.assertIn("@ IN MX 10 mail.restmail-ci.test.\n", text)
        self.assertIn("mail IN A 10.96.4.5\n", text)

    def test_mail_host_must_be_in_the_zone_and_the_address_ipv4(self):
        with self.assertRaises(ValueError):
            reference.zone_file("restmail-ci.test", "mail.elsewhere.test", "10.0.0.1")
        with self.assertRaises(ValueError):
            reference.zone_file("restmail-ci.test", "mail.restmail-ci.test", "fd00::1")

    def test_corefile_gains_a_block_per_zone_and_keeps_the_rest(self):
        zones = ["reference.test", "restmail-ci.test"]
        out = reference.corefile_with_zones(KIND_COREFILE, zones)
        self.assertTrue(out.startswith(KIND_COREFILE.rstrip("\n")))
        for zone in zones:
            self.assertIn(f"{zone}:53 {{\n    errors\n    file /etc/coredns/zone.{zone}.db {zone}\n}}", out)

    def test_a_second_run_replaces_its_block(self):
        once = reference.corefile_with_zones(KIND_COREFILE, ["a.test"])
        twice = reference.corefile_with_zones(once, ["b.test"])
        self.assertEqual(twice.count(reference.BEGIN), 1)
        self.assertNotIn("a.test:53", twice)
        self.assertIn("b.test:53", twice)
        self.assertEqual(reference.corefile_with_zones(once, ["a.test"]), once)

    def test_zone_files_are_mounted_where_the_corefile_reads_them(self):
        deployment = {"spec": {"template": {"spec": {"volumes": [
            {"name": "tmp", "emptyDir": {}},
            {"name": "config-volume", "configMap": {"name": "coredns",
                                                    "items": [{"key": "Corefile", "path": "Corefile"}]}}]}}}}
        self.assertEqual(reference.config_volume_patch(deployment),
                         [{"op": "remove", "path": "/spec/template/spec/volumes/1/configMap/items"}])
        del deployment["spec"]["template"]["spec"]["volumes"][1]["configMap"]["items"]
        self.assertEqual(reference.config_volume_patch(deployment), [])
        with self.assertRaises(ValueError):
            reference.config_volume_patch({"spec": {"template": {"spec": {"volumes": []}}}})


class ManifestTests(unittest.TestCase):
    def setUp(self):
        self.items = {item["kind"]: item for item in reference.manifests("ref")["items"]}

    def test_image_is_pinned_by_digest(self):
        self.assertRegex(reference.REFERENCE_IMAGE, r"^ghcr\.io/docker-mailserver/docker-mailserver:[0-9.]+@sha256:[0-9a-f]{64}$")
        pod = self.items["Deployment"]["spec"]["template"]["spec"]
        self.assertEqual({c["image"] for c in pod["containers"] + pod["initContainers"]}, {reference.REFERENCE_IMAGE})

    def test_only_mail_moving_is_on(self):
        env = {e["name"]: e["value"] for e in self.items["Deployment"]["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertEqual(env["OVERRIDE_HOSTNAME"], reference.REFERENCE_HOST)
        for check in ("RSPAMD", "OPENDKIM", "OPENDMARC", "POLICYD_SPF", "SPAMASSASSIN", "CLAMAV", "POSTGREY"):
            self.assertEqual(env[f"ENABLE_{check}"], "0", check)
        self.assertEqual(env["SSL_TYPE"], "manual")

    def test_the_service_and_secret_are_what_the_stages_reach(self):
        service = self.items["Service"]
        self.assertEqual((service["metadata"]["name"], service["metadata"]["namespace"]),
                         (cluster_e2e.REFERENCE_SERVICE, "ref"))
        ports = {p["name"]: p["port"] for p in service["spec"]["ports"]}
        for name, port in cluster_e2e.REFERENCE_PORTS.items():
            self.assertEqual(ports[name], port)
        self.assertEqual(ports["smtp"], 25)  # where REST Mail delivers, by MX
        self.assertEqual(reference.SECRET, cluster_e2e.REFERENCE_SECRET)
        self.assertEqual(service["spec"]["selector"], self.items["Deployment"]["spec"]["selector"]["matchLabels"])

    def test_the_account_and_postfix_overrides_are_copied_into_a_writable_config_directory(self):
        pod = self.items["Deployment"]["spec"]["template"]["spec"]
        copy = pod["initContainers"][0]
        self.assertEqual(copy["command"], ["cp", "/secret/postfix-accounts.cf", "/secret/postfix-main.cf",
                                           "/tmp/docker-mailserver/"])
        volumes = {v["name"]: v for v in pod["volumes"]}
        self.assertEqual(volumes["account"]["secret"]["secretName"], reference.SECRET)
        self.assertEqual({item["key"] for item in volumes["account"]["secret"]["items"]},
                         {"postfix-accounts.cf", "postfix-main.cf"})
        self.assertIn("emptyDir", volumes["config"])

    def test_the_reference_retries_soon_after_a_greylist_deferral(self):
        # REST Mail greylists a first-time triple for 5 minutes (#1750's first
        # mail-in run: 451 4.3.0, then Postfix's default 300s+ backoff outlived
        # the step). The retries must come at most a couple of minutes apart.
        settings = dict(line.split(" = ") for line in reference.POSTFIX_MAIN.splitlines())
        seconds = {key: int(value.removesuffix("s")) for key, value in settings.items()}
        self.assertEqual(set(seconds), {"queue_run_delay", "minimal_backoff_time", "maximal_backoff_time"})
        self.assertLessEqual(seconds["queue_run_delay"], seconds["minimal_backoff_time"])
        self.assertLessEqual(seconds["minimal_backoff_time"], seconds["maximal_backoff_time"])
        self.assertLessEqual(seconds["maximal_backoff_time"] + seconds["queue_run_delay"], 120)

    def test_secret_values_are_base64(self):
        secret = reference.secret_manifest("ref", "s", {"password": "p"})
        self.assertEqual(secret["data"], {"password": "cA=="})
        self.assertTrue(re.fullmatch(r"[a-z0-9-]+", reference.TLS_SECRET))


class MTASTSTests(unittest.TestCase):
    def test_zone_adds_the_policy_host(self):
        text = reference.zone_file("reference.test", "mail.reference.test", "10.0.0.1", hosts={"mta-sts": "10.0.0.2"})
        self.assertIn("mta-sts IN A 10.0.0.2\n", text)
        self.assertIn("mail IN A 10.0.0.1\n", text)
        published = reference.zone_with_records(text, {"_mta-sts": "v=STSv1; id=abc123"})
        self.assertIn('_mta-sts IN TXT "v=STSv1; id=abc123"', published)
        self.assertIn("mta-sts IN A 10.0.0.2", published)
        for bad in ({"mail": "10.0.0.2"}, {"ns": "10.0.0.2"}, {"a b": "10.0.0.2"}, {"mta-sts": "nope"}):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                reference.zone_file("reference.test", "mail.reference.test", "10.0.0.1", hosts=bad)

    def test_policy_file_is_rfc_8461_shaped(self):
        self.assertEqual(reference.sts_policy(["mail.reference.test"]),
                         "version: STSv1\r\nmode: enforce\r\nmx: mail.reference.test\r\nmax_age: 86400\r\n")
        for bad in (([], "enforce"), (["mail.reference.test"], "strict"), (["a b"], "enforce")):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                reference.sts_policy(*bad)

    def test_the_mismatch_policy_names_an_mx_its_domain_does_not_have(self):
        good = reference.STS_POLICIES["mta-sts." + reference.REFERENCE_DOMAIN]
        mismatch = reference.STS_POLICIES["mta-sts." + reference.MISMATCH_DOMAIN]
        self.assertIn(f"mx: {reference.REFERENCE_HOST}\r\n", good)
        self.assertNotIn(reference.MISMATCH_HOST, mismatch)
        self.assertIn("mode: enforce", mismatch)
        self.assertEqual((cluster_e2e.STS_DOMAIN, cluster_e2e.STS_MX), (reference.REFERENCE_DOMAIN, reference.REFERENCE_HOST))
        self.assertEqual((cluster_e2e.STS_MISMATCH_DOMAIN, cluster_e2e.STS_MISMATCH_MX),
                         (reference.MISMATCH_DOMAIN, reference.MISMATCH_HOST))

    def test_the_certificate_names_every_host_tls_is_verified_for(self):
        self.assertEqual(set(reference.TLS_HOSTS), {reference.REFERENCE_HOST, reference.MISMATCH_HOST,
                                                    *reference.STS_POLICIES})

    def test_policy_host_manifests(self):
        items = {item["kind"]: item for item in reference.sts_manifests("ref", reference.STS_POLICIES)["items"]}
        pod = items["Deployment"]["spec"]["template"]["spec"]
        self.assertEqual(pod["containers"][0]["image"], reference.REFERENCE_IMAGE)
        self.assertEqual(pod["containers"][0]["ports"][0]["containerPort"], 443)  # what the gateway's egress allows
        volumes = {v["name"]: v for v in pod["volumes"]}
        self.assertEqual(volumes["tls"]["secret"]["secretName"], reference.TLS_SECRET)
        paths = {i["path"]: items["ConfigMap"]["data"][i["key"]] for i in volumes["config"]["configMap"]["items"]}
        self.assertEqual(paths["serve.py"], reference.STS_SERVER)
        for host, policy in reference.STS_POLICIES.items():
            self.assertEqual(paths[f"policies/{host}"], policy)
        self.assertEqual(items["Service"]["metadata"]["name"], cluster_e2e.STS_DEPLOYMENT)
        self.assertEqual(items["Service"]["spec"]["selector"], items["Deployment"]["spec"]["selector"]["matchLabels"])

    def test_the_policy_host_serves_by_host_name_and_logs_the_fetch(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            subprocess.run(["openssl", "req", "-x509", "-nodes", "-newkey", "rsa:2048", "-days", "1",
                            "-subj", "/CN=mta-sts.reference.test", "-addext",
                            "subjectAltName=DNS:mta-sts.reference.test",
                            "-keyout", str(tmp / "tls.key"), "-out", str(tmp / "tls.crt")],
                           check=True, capture_output=True)
            (tmp / "policies").mkdir()
            policy = reference.STS_POLICIES["mta-sts.reference.test"]
            (tmp / "policies" / "mta-sts.reference.test").write_text(policy, newline="")
            with socket.socket() as probe:
                probe.bind(("127.0.0.1", 0))
                port = probe.getsockname()[1]
            script = (reference.STS_SERVER.replace("/etc/mta-sts/policies", str(tmp / "policies"))
                      .replace("/etc/reference-tls", str(tmp)).replace('("", 443)', f'("127.0.0.1", {port})'))
            server = subprocess.Popen([sys.executable, "-I", "-c", script], stdout=subprocess.PIPE,
                                      stderr=subprocess.STDOUT, text=True)
            try:
                context = ssl.create_default_context(cafile=str(tmp / "tls.crt"))
                context.check_hostname = False

                def get(host, path="/.well-known/mta-sts.txt"):
                    request = urllib.request.Request(f"https://127.0.0.1:{port}{path}", headers={"Host": host})
                    try:
                        with urllib.request.urlopen(request, context=context, timeout=5) as response:
                            return response.status, response.read().decode()
                    except urllib.error.HTTPError as error:
                        return error.code, ""

                for _ in range(50):
                    try:
                        status, body = get("mta-sts.reference.test")
                        break
                    except (urllib.error.URLError, ConnectionError):
                        time.sleep(0.1)
                self.assertEqual((status, body), (200, policy))
                self.assertEqual(get("mta-sts.other.test")[0], 404)
                self.assertEqual(get("mta-sts.reference.test", "/")[0], 404)
            finally:
                server.terminate()
                log = server.communicate(timeout=10)[0]
            self.assertEqual(cluster_e2e.policy_fetches(log, "mta-sts.reference.test"), 1)
            self.assertEqual(cluster_e2e.policy_fetches(log, "mta-sts.other.test"), 0)


class PublishedRecordTests(unittest.TestCase):
    ZONE = reference.zone_file("restmail-ci.test", "mail.restmail-ci.test", "10.96.4.5")

    def test_records_are_added_and_the_serial_grows(self):
        key = "v=DKIM1; k=rsa; p=" + "A" * 400
        text = reference.zone_with_records(self.ZONE, {"sel._domainkey": key})
        self.assertRegex(text, r"(?m)^@ IN SOA ns\.restmail-ci\.test\. hostmaster\.restmail-ci\.test\. 2 ")
        line = next(l for l in text.splitlines() if l.startswith("sel._domainkey IN TXT "))
        parts = re.findall(r'"([^"]*)"', line)
        self.assertEqual("".join(parts), key)
        self.assertTrue(all(len(part) <= 255 for part in parts))
        self.assertIn("mail IN A 10.96.4.5\n", text)

    def test_publishing_again_replaces_the_records_and_bumps_again(self):
        once = reference.zone_with_records(self.ZONE, {"a._domainkey": "v=DKIM1; p=A"})
        twice = reference.zone_with_records(once, {"b._domainkey": "v=DKIM1; p=B"})
        self.assertEqual(twice.count(reference.RECORDS_BEGIN), 1)
        self.assertNotIn("a._domainkey", twice)
        self.assertIn('b._domainkey IN TXT "v=DKIM1; p=B"', twice)
        self.assertRegex(twice, r"(?m)^@ IN SOA \S+ \S+ 3 ")

    def test_refuses_what_would_break_the_zone(self):
        for name, value in (("a b", "v"), ("a._domainkey", 'say "hi"'), ("a._domainkey", ""), ("../x", "v")):
            with self.subTest(name=name, value=value), self.assertRaises(ValueError):
                reference.zone_with_records(self.ZONE, {name: value})
        with self.assertRaises(ValueError):
            reference.zone_with_records("$ORIGIN x.test.\n", {"a": "v"})


class PeerTests(unittest.TestCase):
    def test_the_peers_mx_is_one_address_for_smtp_and_https(self):
        items = reference.peer_front("restmail-peer", "10.244.0.20", 10025, "10.244.0.7")["items"]
        service, *slices = items
        self.assertEqual(service["kind"], "Service")
        self.assertNotIn("selector", service["spec"])
        self.assertEqual({(p["name"], p["port"], p["targetPort"]) for p in service["spec"]["ports"]},
                         {("smtp", 25, 10025), ("https", 443, 443)})
        by_port = {s["ports"][0]["name"]: s for s in slices}
        self.assertEqual(by_port["smtp"]["endpoints"][0]["addresses"], ["10.244.0.20"])
        self.assertEqual(by_port["smtp"]["ports"][0]["port"], 10025)
        self.assertEqual(by_port["https"]["endpoints"][0]["addresses"], ["10.244.0.7"])
        for endpoint_slice in slices:
            self.assertEqual(endpoint_slice["metadata"]["labels"]["kubernetes.io/service-name"], service["metadata"]["name"])
        with self.assertRaises(ValueError):
            reference.peer_front("p", "", 10025, "10.0.0.1")

    def test_the_policy_opens_only_the_peer_gateways_port_from_the_first_gateway(self):
        policy = reference.peer_egress_policy("restmail", "restmail", "restmail-peer", 10025)
        self.assertEqual(policy["metadata"]["namespace"], "restmail")
        self.assertEqual(policy["spec"]["podSelector"]["matchLabels"],
                         {"app.kubernetes.io/instance": "restmail", "app.kubernetes.io/component": "smtp-gateway"})
        self.assertEqual(policy["spec"]["policyTypes"], ["Egress"])
        rule = policy["spec"]["egress"][0]
        self.assertEqual(rule["ports"], [{"protocol": "TCP", "port": 10025}])
        self.assertEqual(rule["to"][0]["namespaceSelector"]["matchLabels"], {"kubernetes.io/metadata.name": "restmail-peer"})

    def test_the_peer_port_is_the_charts_container_port(self):
        import yaml
        values = yaml.safe_load((ROOT / "helm/restmail/values.yaml").read_text())
        args = reference.parse_args(["--mail-domain", "d.test", "--mail-host", "mail.d.test"])
        self.assertEqual(args.peer_smtp_port, values["smtpGateway"]["containerPorts"]["inbound"])
        import contextlib, io
        with self.assertRaises(SystemExit), contextlib.redirect_stderr(io.StringIO()):
            reference.parse_args(["--mail-domain", "d.test", "--mail-host", "mail.d.test", "--peer-namespace", "p"])

    def test_corefile_zones_already_served_are_kept(self):
        once = reference.corefile_with_zones(KIND_COREFILE, ["a.test", "b.test"])
        served = re.findall(r"^(\S+?):53 \{\n    errors\n    file ", once, flags=re.M)
        self.assertEqual(served, ["a.test", "b.test"])


if __name__ == "__main__":
    unittest.main()
