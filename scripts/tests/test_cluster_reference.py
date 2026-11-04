"""The stage-3 reference mail server's deployment (#1750), without a cluster.

What cluster-e2e.yml applies (the manifests, the CoreDNS change, the account)
is built here as data; these check that data. Whether the reference and REST
Mail then exchange mail only the workflow's kind cluster shows.
"""

import importlib.util
import re
import unittest
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

    def test_the_account_is_copied_into_a_writable_config_directory(self):
        pod = self.items["Deployment"]["spec"]["template"]["spec"]
        copy = pod["initContainers"][0]
        self.assertEqual(copy["command"][-1], "/tmp/docker-mailserver/postfix-accounts.cf")
        volumes = {v["name"]: v for v in pod["volumes"]}
        self.assertEqual(volumes["account"]["secret"]["secretName"], reference.SECRET)
        self.assertIn("emptyDir", volumes["config"])

    def test_secret_values_are_base64(self):
        secret = reference.secret_manifest("ref", "s", {"password": "p"})
        self.assertEqual(secret["data"], {"password": "cA=="})
        self.assertTrue(re.fullmatch(r"[a-z0-9-]+", reference.TLS_SECRET))


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
