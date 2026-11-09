#!/usr/bin/env python3
"""Plan an isolated, labelled local topology from its name alone (#1942).

    python3 scripts/topology_plan.py p1904                  the plan, as JSON
    python3 scripts/topology_plan.py p1904 --environment    NAME=value lines for `chore`
    python3 scripts/topology_plan.py p1904 --commands       the docker commands for the substrate
    python3 scripts/topology_plan.py p1904 --create         only the network and volume commands
    python3 scripts/topology_plan.py p1904 --resolver       only the resolver command, which runs last
    python3 scripts/topology_plan.py p1904 --resources      "<kind> <name>" for each thing it creates
    python3 scripts/topology_plan.py --taken                the subnets Docker has, comma-separated

The shared topology has fixed container names, one subnet (10.99.0.0/16), one DNS
container (`testbed-dnsmasq`) and fixed addresses. A second run on the same machine
collides with it, or restarts the DNS container the first run depends on. A private
run needs names, a subnet and a resolver of its own. This computes them.

- Every name starts with `rml-<name>`, and none can equal a shared one.
- Every container, network and volume carries `rest-mail-loop=<name>`, so
  `RESTMAIL_LABEL=rest-mail-loop=<name> chore clean --volumes` finds all of it.
- The subnet is a /16 from 10.100.0.0/16 up, the first one not overlapping a subnet
  Docker already has. Each address keeps its host part: 10.99.0.20 becomes 10.<n>.0.20.
- Docker chooses every host port. A publish is `-p 127.0.0.1::<container port>`.

It plans and prints. It starts nothing and reads Docker only when no --existing-subnets
is given, to learn which subnets are taken.
"""

import argparse
import hashlib
import ipaddress
import json
import re
import subprocess
import sys

LABEL_KEY = "rest-mail-loop"
SHARED_SUBNET = "10.99.0.0/16"
POOL = [f"10.{second}.0.0/16" for second in range(100, 200)]
NAME = re.compile(r"^[a-z0-9][a-z0-9-]{0,23}$")
# A name the shared topology could own or that would read as it: refuse rather than guess.
RESERVED = {"testbed", "mailref", "rest-mail"}
API_PORT = 8080

# Every address the shared topology fixes, with the role it has. The names are the
# plan's `addresses` keys. Keep it a superset of the 10.99.x.y literals in chores.yml
# and tasks/: scripts/tests/test_topology_plan.py fails when one is missing.
ROLES = {
    "dns": "10.99.0.10",
    "api": "10.99.0.20",
    "webmail": "10.99.0.21",
    "js_filter": "10.99.0.22",
    "smtp": "10.99.0.13",
    "imap": "10.99.0.15",
    "pop3": "10.99.0.16",
    "admin": "10.99.0.27",
    "website": "10.99.0.26",
    "postgres": "10.99.0.43",
    "garage": "10.99.0.44",
    "prometheus": "10.99.0.30",
    "grafana": "10.99.0.31",
    "postgres_exporter": "10.99.0.32",
    "node_exporter": "10.99.0.33",
    "mail1_postgres": "10.99.0.41",
    "mail1_postfix": "10.99.0.11",
    "mail1_dovecot": "10.99.0.111",
    "mail1_rspamd": "10.99.0.17",
    "mail1_fail2ban": "10.99.0.14",
    "mail2_postgres": "10.99.0.42",
    "mail2_postfix": "10.99.0.12",
    "mail2_dovecot": "10.99.0.112",
    "mail2_rspamd": "10.99.0.23",
    "mail2_fail2ban": "10.99.0.24",
}
# Addresses that appear in the task files without a role of their own: the resolver's
# second address, used by monitoring and the e2e client.
ALSO = ("10.99.0.3",)
SHARED_ADDRESSES = tuple(sorted({*ROLES.values(), *ALSO}, key=ipaddress.ip_address))

# The chore variable that carries each address.
ADDRESS_VARIABLES = {
    "dns": ("DNS_IP", "RESTMAIL_TESTBED_DNS_IP"),
    "api": ("RESTMAIL_API_IP",),
    "webmail": ("RESTMAIL_WEBMAIL_IP",),
    "js_filter": ("RESTMAIL_JS_FILTER_IP",),
    "smtp": ("RESTMAIL_SMTP_IP",),
    "imap": ("RESTMAIL_IMAP_IP",),
    "pop3": ("RESTMAIL_POP3_IP",),
    "admin": ("RESTMAIL_ADMIN_IP",),
    "website": ("RESTMAIL_WEBSITE_IP",),
    "postgres": ("RESTMAIL_POSTGRES_IP",),
    "garage": ("RESTMAIL_GARAGE_IP",),
    "prometheus": ("MONITORING_PROMETHEUS_IP",),
    "grafana": ("MONITORING_GRAFANA_IP",),
    "postgres_exporter": ("MONITORING_POSTGRES_EXPORTER_IP",),
    "node_exporter": ("MONITORING_NODE_EXPORTER_IP",),
    "mail1_postgres": ("MAIL1_POSTGRES_IP",),
    "mail1_postfix": ("MAIL1_POSTFIX_IP",),
    "mail1_dovecot": ("MAIL1_DOVECOT_IP",),
    "mail1_rspamd": ("MAIL1_RSPAMD_IP",),
    "mail1_fail2ban": ("MAIL1_FAIL2BAN_IP",),
    "mail2_postgres": ("MAIL2_POSTGRES_IP",),
    "mail2_postfix": ("MAIL2_POSTFIX_IP",),
    "mail2_dovecot": ("MAIL2_DOVECOT_IP",),
    "mail2_rspamd": ("MAIL2_RSPAMD_IP",),
    "mail2_fail2ban": ("MAIL2_FAIL2BAN_IP",),
}

DNSMASQ_IMAGE = "ghcr.io/rest-mail/reference-dnsmasq:0.1.0"


def validate(name):
    if not isinstance(name, str) or not NAME.match(name) or name.endswith("-"):
        raise ValueError(f"run name {name!r} must be 1 to 24 characters of a-z, 0-9 and -, not ending in -")
    if name in RESERVED or any(name.startswith(f"{word}-") for word in RESERVED):
        raise ValueError(f"run name {name!r} would read as the shared topology")


def shift(address, subnet):
    """The address in the private subnet with the same host part as a shared address."""
    if ipaddress.ip_address(address) not in ipaddress.ip_network(SHARED_SUBNET):
        raise ValueError(f"{address} is not in the shared subnet {SHARED_SUBNET}")
    prefix = ".".join(subnet.split(".")[:2])
    return ".".join([prefix, *address.split(".")[2:]])


def shift_text(text, subnet):
    """Moves every shared-subnet address in a text, such as a reference server's env file."""
    prefix = ".".join(subnet.split(".")[:2])
    return re.sub(r"(?<![\d.])10\.99\.(?=\d)", f"{prefix}.", text)


def render_manifest(text, plan):
    """An instance manifest placed on the run's own project, network, certs volume and subnet."""
    names = plan["names"]
    placement = {
        "project": names["instance_project"],
        "network": names["network"],
        "certs_volume": names["certs_volume"],
        "testbed_dns_ip": plan["dns_ip"],
    }
    for key, value in placement.items():
        text, count = re.subn(rf"(?m)^({key}:\s+)\S+", rf"\g<1>{value}", text)
        if count != 1:
            raise ValueError(f"manifest has {count} `{key}:` lines, not one")
    return shift_text(text, plan["subnet"])


def choose_subnet(name, taken, pool):
    """The first pool subnet from the name's hashed position that overlaps nothing taken."""
    blocked = [ipaddress.ip_network(entry) for entry in taken] + [ipaddress.ip_network(SHARED_SUBNET)]
    start = int(hashlib.sha256(name.encode()).hexdigest(), 16) % len(pool)
    for step in range(len(pool)):
        candidate = pool[(start + step) % len(pool)]
        network = ipaddress.ip_network(candidate)
        if not any(network.overlaps(other) for other in blocked):
            return candidate
    raise RuntimeError("no subnet in " + pool[0] + " .. " + pool[-1] + " is free of the subnets Docker already has")


def publish(container_port):
    """A publish flag that leaves the host port to Docker, on the loopback address only."""
    return f"-p 127.0.0.1::{int(container_port)}"


def names_for(name):
    testbed = f"rml-{name}-tb"
    instance = f"rml-{name}"
    return {
        "testbed_project": testbed,
        "instance_project": instance,
        "network": f"{testbed}_mailnet",
        "certs_volume": f"{testbed}_certs",
        "fragments_volume": f"{testbed}_dnsmasq_fragments",
        "dnsmasq": f"{testbed}-dnsmasq",
        "mail1_config": f"rml-{name}-mail1",
        "mail2_config": f"rml-{name}-mail2",
    }


def commands_for(label, names, subnet, dns_ip):
    quoted = f'--label "{label}"'
    return [
        f"docker network create {quoted} --subnet={subnet} {names['network']}",
        f"docker volume create {quoted} {names['certs_volume']}",
        f"docker volume create {quoted} {names['fragments_volume']}",
        f"docker run -d {quoted} --name {names['dnsmasq']} --cap-add NET_ADMIN "
        f"--network {names['network']} --ip {dns_ip} "
        f"-v {names['fragments_volume']}:/etc/dnsmasq-overlay:ro {DNSMASQ_IMAGE}",
    ]


def resources(names):
    """The containers, networks and volumes the substrate creates, as (kind, name)."""
    return [
        ("network", names["network"]),
        ("volume", names["certs_volume"]),
        ("volume", names["fragments_volume"]),
        ("container", names["dnsmasq"]),
    ]


def plan(name, existing_subnets=(), pool=None):
    validate(name)
    pool = list(pool or POOL)
    subnet = choose_subnet(name, existing_subnets, pool)
    names = names_for(name)
    label = f"{LABEL_KEY}={name}"
    addresses = {role: shift(address, subnet) for role, address in ROLES.items()}
    environment = {"RESTMAIL_LABEL": label}
    environment.update(
        {
            "TESTBED_PROJECT": names["testbed_project"],
            "RESTMAIL_PROJECT": names["instance_project"],
            "RESTMAIL_NETWORK": names["network"],
            "RESTMAIL_CERTS_VOLUME": names["certs_volume"],
            "DNSMASQ_CONTAINER": names["dnsmasq"],
            "DNSMASQ_FRAGMENTS_VOLUME": names["fragments_volume"],
            "SUBNET": subnet,
        }
    )
    for role, variables in ADDRESS_VARIABLES.items():
        for variable in variables:
            environment[variable] = addresses[role]
    environment["API_BASE_URL"] = environment["CONTRACT_ENDPOINT"] = f"http://{addresses['api']}:{API_PORT}"
    return {
        "name": name,
        "label": label,
        "subnet": subnet,
        "dns_ip": addresses["dns"],
        "names": names,
        "addresses": addresses,
        "environment": environment,
        "commands": commands_for(label, names, subnet, addresses["dns"]),
        "resources": [list(item) for item in resources(names)],
        "published_ports": [],
    }


def docker_subnets():
    """The subnet of every Docker network on this machine."""
    listing = subprocess.run(["docker", "network", "ls", "-q"], capture_output=True, text=True, check=False)
    if listing.returncode != 0:
        raise RuntimeError("cannot list Docker networks; pass --existing-subnets to plan without Docker: " + listing.stderr.strip())
    ids = listing.stdout.split()
    if not ids:
        return []
    inspected = subprocess.run(
        ["docker", "network", "inspect", "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}", *ids],
        capture_output=True,
        text=True,
        check=False,
    )
    if inspected.returncode != 0:
        raise RuntimeError("cannot inspect Docker networks: " + inspected.stderr.strip())
    return [entry for entry in inspected.stdout.split() if ":" not in entry]


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("name", nargs="?", help="the run's name, such as p1904")
    parser.add_argument(
        "--existing-subnets",
        help="comma-separated subnets already taken; an empty value means none and skips Docker",
    )
    output = parser.add_mutually_exclusive_group()
    output.add_argument("--environment", action="store_true", help="print NAME=value lines")
    output.add_argument("--commands", action="store_true", help="print the docker commands")
    output.add_argument("--create", action="store_true", help="print the network and volume commands")
    output.add_argument("--resolver", action="store_true", help="print the resolver command")
    output.add_argument("--resources", action="store_true", help="print '<kind> <name>' for each resource")
    output.add_argument("--taken", action="store_true", help="print the subnets Docker has, comma-separated")
    arguments = parser.parse_args(argv)
    try:
        if arguments.taken:
            print(",".join(docker_subnets()))
            return 0
        if not arguments.name:
            parser.error("a run name is required")
        if arguments.existing_subnets is None:
            taken = docker_subnets()
        else:
            taken = [entry for entry in arguments.existing_subnets.split(",") if entry]
        result = plan(arguments.name, taken)
    except (ValueError, RuntimeError) as error:
        print(f"topology_plan: {error}", file=sys.stderr)
        return 2
    if arguments.environment:
        print("\n".join(f"{key}={value}" for key, value in result["environment"].items()))
    elif arguments.commands:
        print("\n".join(result["commands"]))
    elif arguments.create:
        print("\n".join(result["commands"][:-1]))
    elif arguments.resolver:
        print(result["commands"][-1])
    elif arguments.resources:
        print("\n".join(f"{kind} {name}" for kind, name in result["resources"]))
    else:
        print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
