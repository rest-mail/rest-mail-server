#!/usr/bin/env python3
"""Reject floating external container inputs and third-party actions (#2113 AC01).

Locally built image tags and Dockerfile stages are outputs, not downloaded inputs.
This checks literal Docker commands and declared image defaults, including composite
actions. Package-manager/bootstrap tool reproducibility is a separate obligation.
"""

import ast
from pathlib import Path
import re
import shlex
import subprocess
import sys

import yaml

from check_docker_labels import logical_lines


ROOT = Path(__file__).resolve().parents[1]
DIGEST = re.compile(r"^[\w./:-]+@sha256:[0-9a-f]{64}$")
ACTION = re.compile(r"^[\w.-]+/[\w./-]+@[0-9a-f]{40}$")
DEFAULT = re.compile(r'\|\s*default\s+"([^"]*)"')
TEMPLATE = re.compile(r"\{\{.*?\}\}")
# This repository's own service images. They are built from its Dockerfiles before the
# topology uses them (tasks/*.yml `docker build -t`), so a tag is an output, not a download.
OWN_IMAGE = re.compile(r"^ghcr\.io/rest-mail/(api|smtp-gateway|imap-gateway|pop3-gateway|js-filter|webmail|admin):[\w.-]+$")
DOCKER = re.compile(r'(?:\bdocker|"\$real")\s+(run|create|pull)\s+')
VALUE_OPTIONS = {
    "--add-host", "--attach", "-a", "--cap-add", "--cap-drop", "--cidfile",
    "--cpus", "--cpu-shares", "--device", "--dns", "--entrypoint", "--env", "-e",
    "--env-file", "--expose", "--gpus", "--group-add", "--health-cmd",
    "--health-interval", "--health-retries", "--health-start-period", "--health-timeout",
    "--hostname", "-h", "--ip", "--ip6", "--ipc", "--label", "-l", "--label-file", "--log-driver",
    "--log-opt", "--memory", "-m", "--memory-swap", "--memory-reservation", "--mount", "--name", "--network",
    "--network-alias", "--oom-score-adj", "--pid", "--pids-limit", "--platform", "--publish", "-p",
    "--pull", "--restart", "--security-opt", "--shm-size", "--stop-timeout", "--tmpfs",
    "--ulimit", "--user", "-u", "--volume", "-v", "--volumes-from", "--workdir", "-w",
}
BOOLEAN_OPTIONS = {
    "--rm", "--detach", "--interactive", "--tty", "--privileged", "--init",
    "--read-only", "--oom-kill-disable", "--disable-content-trust", "--quiet", "-q",
}


def source_files(root):
    paths = [root / "chores.yml"]
    paths += sorted(path for path in (root / "tasks").rglob("*") if path.suffix in {".yml", ".yaml", ".sh"})
    paths += sorted(path for path in (root / ".github").rglob("*") if path.suffix in {".yml", ".yaml"})
    paths += sorted((root / "scripts").glob("*.py"))
    dockerfiles = subprocess.check_output(["git", "ls-files", "*Dockerfile*"], cwd=root, text=True)
    paths += [root / name for name in dockerfiles.splitlines()]
    return paths


def docker_images(text):
    for number, line in logical_lines(text):
        for command in DOCKER.finditer(line):
            remainder = TEMPLATE.sub("__template__", line[command.end():])
            remainder = re.sub(r"\$\([^)]*\)", "__shell__", remainder)
            lexer = shlex.shlex(remainder, posix=True)
            lexer.whitespace_split = True
            try:
                for token in lexer:
                    if token == "$mounts":
                        continue
                    if token in VALUE_OPTIONS:
                        next(lexer)
                    elif token in BOOLEAN_OPTIONS or re.fullmatch(r"-[dit]+", token) or token.startswith("--") and "=" in token:
                        continue
                    elif token.startswith("-"):
                        yield number, f"<unrecognized docker option {token}>"
                        break
                    else:
                        yield number, token
                        break
            except (ValueError, StopIteration):
                yield number, "<unparseable docker image>"


def python_images(text):
    for node in ast.walk(ast.parse(text)):
        if isinstance(node, ast.Assign):
            try:
                value = ast.literal_eval(node.value)
            except (ValueError, TypeError):
                continue
            if isinstance(value, str):
                if any(isinstance(target, ast.Name) and target.id.endswith("_IMAGE") for target in node.targets):
                    yield node.lineno, value
                yield from docker_images(value)
        elif isinstance(node, ast.Dict):
            for key, value in zip(node.keys, node.values):
                if isinstance(key, ast.Constant) and isinstance(key.value, str) and key.value.endswith("_IMAGE") and isinstance(value, ast.Constant) and isinstance(value.value, str):
                    yield node.lineno, value.value
        elif isinstance(node, (ast.List, ast.Tuple)):
            values = [item.value if isinstance(item, ast.Constant) and isinstance(item.value, str) else "__template__" for item in node.elts]
            if len(values) > 1 and values[:2] in (["docker", "run"], ["docker", "create"], ["docker", "pull"]):
                yield from docker_images(" ".join(shlex.quote(value) for value in values))
        elif isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "start" and len(node.args) > 1:
            value = node.args[1]
            if isinstance(value, ast.Constant) and isinstance(value.value, str):
                yield node.lineno, value.value


def problems(files):
    found = []

    def image(name, reference, location):
        if not reference or any(marker in reference for marker in ("{{", "$", "__template__", "__shell__")):
            return
        if not DIGEST.fullmatch(reference) and not OWN_IMAGE.fullmatch(reference):
            found.append(f"{name}:{location}: floating image {reference}")

    def walk(name, value, built_variables, location=""):
        if isinstance(value, dict):
            for key, item in value.items():
                where = f"{location}.{key}"
                if key == "uses" and isinstance(item, str):
                    if item.startswith("docker://"):
                        image(name, item.removeprefix("docker://"), where)
                    elif not item.startswith("./") and not ACTION.fullmatch(item):
                        found.append(f"{name}:{where}: floating action {item}")
                container_input = key == "container" or (key == "image" and (".services." in where or ".container." in where))
                if isinstance(item, str) and (container_input or str(key).endswith("_IMAGE")) and key not in built_variables:
                    defaults = DEFAULT.findall(item)
                    reference = defaults[-1] if defaults else item
                    tag_key = str(key).removesuffix("_IMAGE") + "_TAG"
                    if tag_key in value:
                        tags = DEFAULT.findall(str(value[tag_key]))
                        reference += ":" + (tags[-1] if tags else str(value[tag_key]))
                    image(name, reference, where)
                walk(name, item, built_variables, where)
        elif isinstance(value, list):
            for index, item in enumerate(value):
                walk(name, item, built_variables, f"{location}[{index}]")

    for name, text in files.items():
        if Path(name).suffix == ".py":
            for number, reference in python_images(text):
                image(name, reference, number)
            continue
        if "Dockerfile" in Path(name).name:
            stages = {"scratch"}
            arguments = {}
            for number, line in logical_lines(text):
                tokens = line.split()
                if tokens and tokens[0].upper() == "ARG" and len(tokens) == 2:
                    argument, _, default = tokens[1].partition("=")
                    arguments[argument] = default
                if not tokens or tokens[0].upper() != "FROM":
                    continue
                reference = next(token for token in tokens[1:] if not token.startswith("--"))
                reference = re.sub(r"\$\{(\w+)\}|\$(\w+)", lambda match: arguments.get(match[1] or match[2]) or "<unpinned build argument>", reference)
                if reference not in stages:
                    image(name, reference, number)
                if len(tokens) > 3 and tokens[-2].upper() == "AS":
                    stages.add(tokens[-1])
            continue
        if Path(name).suffix in {".yml", ".yaml"}:
            built_variables = set(re.findall(r'\bdocker\s+build[^\n]*?\s-t\s+["\']?\$\{?(\w+)', text))
            walk(name, yaml.safe_load(text), built_variables)
        built = set(re.findall(r'\bdocker\s+build[^\n]*?\s-t\s+["\']?([^\s"\']+)', text))
        for number, reference in docker_images(text):
            if reference not in built:
                image(name, reference, number)
    return found


def main():
    files = {str(path.relative_to(ROOT)): path.read_text() for path in source_files(ROOT)}
    found = problems(files)
    for problem in found:
        print(problem, file=sys.stderr)
    return int(bool(found))


if __name__ == "__main__":
    sys.exit(main())
