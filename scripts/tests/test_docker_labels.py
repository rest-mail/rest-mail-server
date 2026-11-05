"""Docker resources carry a label, and `chore clean` goes by it and nothing else (#1963)."""

import importlib.util
import io
import json
import os
import subprocess
import tarfile
import tempfile
import textwrap
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
LABEL = 'rest-mail-loop=fixture'


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / f"scripts/{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


check = load("check_docker_labels")
source_volume = load("source_volume")


class TheTreeTests(unittest.TestCase):
    def test_chores_and_tasks_pass_the_check(self):
        result = subprocess.run(["python3", "-B", "scripts/check_docker_labels.py"], cwd=ROOT, capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_the_check_reads_the_task_scripts_too(self):
        names = [path.name for path in check.source_files(ROOT)]
        for name in ("chores.yml", "api.yml", "stage-certs.sh", "js-filter-network.sh", "dev-gocache.sh", "docker-hygiene.sh"):
            self.assertIn(name, names)

    def test_the_label_is_defined_for_every_task(self):
        environment = yaml.safe_load((ROOT / "chores.yml").read_text())["env"]
        self.assertIn("rest-mail.run=local", environment["RESTMAIL_LABEL"]["sh"])

    def test_clean_is_wired_to_the_script_and_needs_no_config(self):
        task = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]["clean"]
        self.assertEqual(task["dotenv"], [])
        self.assertIn("sh tasks/clean.sh", task["cmds"])
        self.assertEqual(task["args"][0]["name"], "volumes")

    def test_a_topology_ending_drops_the_guest_cache(self):
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        for name in ("e2e:down", "instance:down:rest-mail"):
            self.assertEqual(tasks[name]["cmds"][-1], "sh tasks/docker-hygiene.sh drop-guest-cache", name)

    def test_every_image_build_caps_the_cache_afterwards(self):
        for path in sorted((ROOT / "tasks").glob("*.yml")):
            tasks = yaml.safe_load(path.read_text()).get("tasks") or {}
            for name, task in tasks.items():
                commands = task.get("cmds") or [] if isinstance(task, dict) else []
                if any("docker build" in command for command in commands if isinstance(command, str)):
                    self.assertIn('sh "{{.TASKFILE_DIR}}/docker-hygiene.sh" cap-build-cache', commands, f"{path.name}:{name}")


class TheCheckTests(unittest.TestCase):
    def found(self, text, name="chores.yml"):
        return check.problems({name: text})

    def test_a_labelled_resource_passes(self):
        text = textwrap.dedent("""
            docker volume create --label "$RESTMAIL_LABEL" cache
            docker network create --label "$RESTMAIL_LABEL" net
            docker create --name a --label "$RESTMAIL_LABEL" image
            docker run --rm --label "$RESTMAIL_LABEL" \\
              -v cache:/cache \\
              -v "$(pwd)/config:/config:ro" \\
              image
            docker compose --label-file x -l "$RESTMAIL_LABEL" up
        """)
        self.assertEqual(self.found(text), [])

    def test_each_kind_of_creation_without_a_label_fails(self):
        for command in (
            "docker run --rm alpine:3.20 true",
            "docker run -d --name a image",
            "docker create --name a image",
            "docker network create net",
            "docker volume create data",
            "docker compose up -d",
            "docker compose -f x.yml create",
            "docker-compose up",
        ):
            with self.subTest(command=command):
                self.assertEqual(len(self.found(command + "\n")), 1)

    def test_a_label_of_something_else_does_not_count(self):
        self.assertEqual(len(self.found("docker run -d --label docker-proxy.api.port=80 -l other=1 image\n")), 1)

    def test_a_label_on_a_later_continuation_line_counts(self):
        self.assertEqual(self.found('docker run --rm \\\n  --network x \\\n  --label "$RESTMAIL_LABEL" \\\n  image\n'), [])

    def test_a_comment_or_prose_is_not_a_command(self):
        text = textwrap.dedent("""
            # Drives the container via plain `docker run`.
            desc: docker run without a label, only in a description
            echo 'never on the `docker run` command line'
        """)
        self.assertEqual(self.found(text), [])

    def test_a_named_volume_docker_would_create_itself_fails(self):
        found = self.found('docker run --rm --label "$RESTMAIL_LABEL" -v cache:/cache image\n')
        self.assertEqual(len(found), 1)
        self.assertIn("volume cache", found[0])

    def test_a_volume_created_in_another_file_counts(self):
        files = {
            "tasks/a.yml": 'docker volume create --label "$RESTMAIL_LABEL" {{.CACHE}}\n',
            "tasks/b.yml": 'docker run --rm --label "$RESTMAIL_LABEL" -v {{.CACHE}}:/cache image\n',
        }
        self.assertEqual(check.problems(files), [])

    def test_a_volume_staged_by_the_certificate_script_counts(self):
        text = (
            '"{{.TASKFILE_DIR}}/stage-certs.sh" {{.OWN}} 0:0 x:y\n'
            'docker run --rm --label "$RESTMAIL_LABEL" -v {{.OWN}}:/certs:ro image\n'
        )
        self.assertEqual(self.found(text), [])

    def test_volumes_the_testbed_owns_are_not_this_repositorys_to_create(self):
        self.assertEqual(self.found('docker run --rm --label "$RESTMAIL_LABEL" -v "{{.CERTS_VOLUME}}:/certs" image\n'), [])

    def test_the_checkout_bind_mounted_at_src_fails(self):
        for mount in ("{{.ROOT_DIR}}:/src", '"{{.ROOT_DIR}}:/src"', '"$(pwd):/src"', "$PWD:/src:ro"):
            with self.subTest(mount=mount):
                found = self.found(f'docker run --rm --label "$RESTMAIL_LABEL" -v {mount} -w /src image\n')
                self.assertTrue(any("bind mounted" in line for line in found), found)


class DockerStub(unittest.TestCase):
    """Runs a script with a stub docker that answers from a table and records every call."""

    STATE = {}

    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.directory = Path(temp.name).resolve()
        self.capture = self.directory / "calls.jsonl"
        self.state = self.directory / "state.json"
        self.state.write_text(json.dumps(self.STATE))
        stub = self.directory / "docker"
        stub.write_text(textwrap.dedent("""\
            #!/usr/bin/env python3
            import json, os, sys
            args = sys.argv[1:]
            with open(os.environ["CAPTURE"], "a") as output:
                output.write(json.dumps(args) + "\\n")
            state = json.load(open(os.environ["STATE"]))
            def answer(key):
                value = state.get(key, "")
                if isinstance(value, int):
                    sys.exit(value)
                print(value)
            if args[:1] == ["info"]:
                answer("info")
            elif args[:3] == ["builder", "prune", "--help"]:
                answer("prune_help")
            elif args[:2] == ["builder", "prune"]:
                sys.exit(state.get("prune_status", 0))
            elif args[:1] == ["run"]:
                if "/decoys:ro" in " ".join(args):
                    sys.exit(state.get("decoy_run_status", state.get("run_status", 0)))
                sys.exit(state.get("run_status", 0))
            elif args[:1] == ["ps"]:
                answer("containers")
            elif args[:2] == ["network", "ls"]:
                answer("networks")
            elif args[:2] == ["volume", "ls"]:
                filters = [args[i + 1] for i, word in enumerate(args) if word == "--filter"]
                answer("data_volumes" if "label=rest-mail.data=true" in filters else "volumes")
            elif args[:2] == ["volume", "rm"] and args[2] in state.get("stuck", []):
                sys.exit(1)
            """))
        stub.chmod(0o755)

    def script(self, *arguments, **environment):
        base = {key: value for key, value in os.environ.items() if key not in {"CI", "VOLUMES", "RESTMAIL_LABEL", "RESTMAIL_BUILD_CACHE_CAP"}}
        return subprocess.run(
            arguments,
            env={**base, "PATH": f"{self.directory}:{os.environ['PATH']}", "CAPTURE": str(self.capture), "STATE": str(self.state), "RESTMAIL_GUEST_DECOYS": str(self.directory / "decoys"), **environment},
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )

    def calls(self):
        if not self.capture.exists():
            return []
        return [json.loads(line) for line in self.capture.read_text().splitlines()]


class GuestCacheTests(DockerStub):
    def drop(self, **environment):
        return self.script("sh", "tasks/docker-hygiene.sh", "drop-guest-cache", **environment)

    def runs(self):
        return [call for call in self.calls() if call[0] == "run"]

    def test_on_docker_desktop_it_drops_only_dentries_and_inodes(self):
        self.state.write_text(json.dumps({"info": "Docker Desktop"}))
        self.assertEqual(self.drop(RESTMAIL_LABEL=LABEL).returncode, 0)
        [run] = self.runs()
        self.assertEqual(run[:4], ["run", "--rm", "--label", LABEL])
        for flag in ("--privileged", "--pid=host", "alpine:3.20"):
            self.assertIn(flag, run)
        self.assertEqual(run[-1], "sync; cat /decoys/* >/dev/null; echo 2 > /proc/sys/vm/drop_caches")

    def test_it_reads_stable_files_so_the_vm_keeps_those_open_instead(self):
        self.state.write_text(json.dumps({"info": "Docker Desktop"}))
        self.assertEqual(self.drop().returncode, 0)
        decoys = self.directory / "decoys"
        self.assertEqual(len(list(decoys.iterdir())), 256)
        self.assertIn(f"{decoys}:/decoys:ro", self.runs()[0])
        # They are never replaced, or a later run would pin the deleted ones.
        before = {path.name: path.stat().st_ino for path in decoys.iterdir()}
        self.drop()
        self.assertEqual({path.name: path.stat().st_ino for path in decoys.iterdir()}, before)

    def test_decoys_the_daemon_cannot_reach_still_get_a_plain_drop(self):
        self.state.write_text(json.dumps({"info": "Docker Desktop", "decoy_run_status": 125}))
        self.assertEqual(self.drop().returncode, 0)
        plain = self.runs()[-1]
        self.assertEqual(len(self.runs()), 2)
        self.assertNotIn("/decoys", " ".join(plain))
        self.assertEqual(plain[-1], "sync; echo 2 > /proc/sys/vm/drop_caches")

    def test_a_linux_runner_is_left_alone(self):
        for system in ("Ubuntu 24.04.3 LTS", "Debian GNU/Linux 12 (bookworm)"):
            self.state.write_text(json.dumps({"info": system}))
            self.assertEqual(self.drop().returncode, 0)
        self.assertEqual(self.runs(), [])

    def test_no_daemon_is_not_a_failure(self):
        self.state.write_text(json.dumps({"info": 1}))
        self.assertEqual(self.drop().returncode, 0)
        self.assertEqual(self.runs(), [])

    def test_a_failed_drop_does_not_fail_the_run(self):
        self.state.write_text(json.dumps({"info": "Docker Desktop", "run_status": 125}))
        result = self.drop()
        self.assertEqual(result.returncode, 0)
        self.assertIn("could not drop", result.stderr)


class BuildCacheTests(DockerStub):
    def cap(self, **environment):
        return self.script("sh", "tasks/docker-hygiene.sh", "cap-build-cache", **environment)

    def prunes(self):
        return [call for call in self.calls() if call[:2] == ["builder", "prune"] and "--help" not in call]

    def test_it_prunes_by_size_only(self):
        self.state.write_text(json.dumps({"prune_help": "--max-used-space bytes"}))
        self.assertEqual(self.cap().returncode, 0)
        self.assertEqual(self.prunes(), [["builder", "prune", "--force", "--all", "--max-used-space", "5GB"]])

    def test_an_older_client_gets_the_older_flag(self):
        self.state.write_text(json.dumps({"prune_help": "--keep-storage bytes"}))
        self.assertEqual(self.cap(RESTMAIL_BUILD_CACHE_CAP="2GB").returncode, 0)
        self.assertEqual(self.prunes(), [["builder", "prune", "--force", "--all", "--keep-storage", "2GB"]])

    def test_ci_keeps_its_cache(self):
        self.assertEqual(self.cap(CI="true").returncode, 0)
        self.assertEqual(self.calls(), [])

    def test_a_failed_prune_does_not_fail_the_build(self):
        self.state.write_text(json.dumps({"prune_status": 1}))
        result = self.cap()
        self.assertEqual(result.returncode, 0)
        self.assertIn("could not trim", result.stderr)


class CleanTests(DockerStub):
    STATE = {
        "info": "Docker Desktop",
        "containers": "c1\nc2",
        "networks": "n1",
        "volumes": "cache\npostgres-data",
        "data_volumes": "postgres-data",
    }

    def clean(self, **environment):
        return self.script("sh", "tasks/clean.sh", RESTMAIL_LABEL=LABEL, **environment)

    def removed_volumes(self):
        return [call[2] for call in self.calls() if call[:2] == ["volume", "rm"]]

    def test_it_lists_only_by_the_label(self):
        self.assertEqual(self.clean().returncode, 0)
        listings = [call for call in self.calls() if call[0] == "ps" or call[1:2] == ["ls"]]
        self.assertEqual(len(listings), 4)
        for call in listings:
            self.assertIn(f"label={LABEL}", call)

    def test_containers_go_with_their_anonymous_volumes(self):
        self.clean()
        self.assertIn(["rm", "-fv", "c1", "c2"], self.calls())
        self.assertIn(["network", "rm", "n1"], self.calls())

    def test_data_volumes_stay_unless_asked(self):
        result = self.clean()
        self.assertEqual(self.removed_volumes(), ["cache"])
        self.assertIn("postgres-data", result.stdout)
        self.assertIn("chore clean --volumes", result.stdout)

    def test_volumes_true_removes_them(self):
        self.clean(VOLUMES="true")
        self.assertEqual(self.removed_volumes(), ["cache", "postgres-data"])

    def test_the_guest_cache_goes_last(self):
        self.clean()
        self.assertEqual(self.calls()[-1][0], "run")
        self.assertIn("--privileged", self.calls()[-1])

    def test_a_volume_it_could_not_remove_is_a_failure(self):
        state = dict(self.STATE, stuck=["cache"])
        self.state.write_text(json.dumps(state))
        result = self.clean()
        self.assertEqual(result.returncode, 1)
        self.assertIn("could not be removed", result.stderr)

    def test_nothing_to_remove_is_success(self):
        self.state.write_text(json.dumps({"info": "Ubuntu"}))
        result = self.clean()
        self.assertEqual(result.returncode, 0)
        self.assertFalse([call for call in self.calls() if call[0] == "rm" or call[1:2] == ["rm"]])


class SourceVolumeTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        git = ["git", "-C", str(self.root), "-c", "user.name=t", "-c", "user.email=t@example.test"]
        subprocess.run([*git, "init", "-q"], check=True)
        (self.root / ".gitignore").write_text(".cache/\n")
        (self.root / "kept.go").write_text("package kept\n")
        (self.root / "deleted.go").write_text("package deleted\n")
        (self.root / "run.sh").write_text("#!/bin/sh\n")
        (self.root / "run.sh").chmod(0o755)
        subprocess.run([*git, "add", "."], check=True)
        subprocess.run([*git, "commit", "-qm", "fixture"], check=True)
        (self.root / "deleted.go").unlink()
        (self.root / "new.go").write_text("package new\n")
        (self.root / ".cache").mkdir()
        (self.root / ".cache/secret").write_text("not for the volume\n")

    def test_the_copy_is_what_git_would_commit(self):
        self.assertEqual(source_volume.tree_files(self.root), [".gitignore", "kept.go", "new.go", "run.sh"])

    def test_the_archive_keeps_modes_and_contents(self):
        stream = io.BytesIO()
        source_volume.write_tree(self.root, stream)
        stream.seek(0)
        with tarfile.open(fileobj=stream) as archive:
            self.assertEqual(archive.extractfile("kept.go").read(), b"package kept\n")
            self.assertTrue(archive.getmember("run.sh").mode & 0o100)
            self.assertNotIn(".cache/secret", archive.getnames())

    def test_resources_it_makes_carry_the_label(self):
        text = (ROOT / "scripts/source_volume.py").read_text()
        self.assertEqual(text.count('"--label", label()'), 4)
        self.assertEqual(text.count('"volume", "create"') + text.count('"docker", "run"') + text.count('docker("run"'), 4)


if __name__ == "__main__":
    unittest.main()
