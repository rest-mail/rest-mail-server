"""The local gate stops before Go tests when loadgen's loopback addresses cannot be bound (#1942)."""

import errno
import importlib.util
import io
import socket
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ci_local", ROOT / "scripts/ci_local.py")
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)

PROBE = gate.unbindable_loopbacks
LOADGEN = "github.com/rest-mail/rest-mail-server/tests/load/loadgen"


def refusing(*refused):
    """A socket class whose bind fails, as macOS does, on the given addresses."""
    bound = []

    class Socket:
        def __init__(self, family, kind):
            assert (family, kind) == (socket.AF_INET, socket.SOCK_STREAM)

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

        def bind(self, address):
            host, port = address
            if host in refused:
                raise OSError(errno.EADDRNOTAVAIL, "Can't assign requested address")
            bound.append((host, port))

    return Socket, bound


class Probe(unittest.TestCase):
    def test_it_names_each_address_bind_refuses(self):
        fake, bound = refusing("127.0.1.2", "127.0.1.3")
        self.assertEqual(
            gate.unbindable_loopbacks(open_socket=fake), ["127.0.1.2", "127.0.1.3"]
        )
        self.assertEqual(bound, [("127.0.1.1", 0)])

    def test_it_checks_the_addresses_loadgen_dials_from(self):
        source = (ROOT / "tests/load/loadgen/loadgen_test.go").read_text()
        for address in gate.LOOPBACK_ALIASES:
            self.assertIn(f'"{address}"', source)

    def test_a_real_bind_of_the_loopback_address_succeeds(self):
        self.assertEqual(gate.unbindable_loopbacks(("127.0.0.1",)), [])


class Step(unittest.TestCase):
    def gate(self, packages):
        scratch = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: scratch.rmdir())
        check = gate.Gate(ROOT, scratch, scratch, False)
        check.packages = packages
        return check

    def test_missing_aliases_stop_the_gate_with_the_commands_that_add_them(self):
        fake, _ = refusing(*gate.LOOPBACK_ALIASES)
        check = self.gate(["mod/internal/api", LOADGEN])
        with patch.object(
            gate, "unbindable_loopbacks", lambda: PROBE(open_socket=fake)
        ):
            with self.assertRaises(gate.CouldNotRun) as stopped:
                check.loopback_aliases()
        message = str(stopped.exception)
        self.assertIn("cannot bind 127.0.1.1, 127.0.1.2, 127.0.1.3", message)
        for address in gate.LOOPBACK_ALIASES:
            self.assertIn(f"sudo ifconfig lo0 alias {address} up", message)

    def test_a_failed_step_is_could_not_run_and_go_tests_are_not_attempted(self):
        fake, _ = refusing("127.0.1.3")
        check = self.gate([LOADGEN])
        ran = []
        steps = [
            ("Package selection", lambda: ran.append("Package selection")),
            ("Loopback aliases", check.loopback_aliases),
            ("Go tests", lambda: ran.append("Go tests")),
        ]
        output = io.StringIO()
        with patch.object(
            gate, "unbindable_loopbacks", lambda: PROBE(open_socket=fake)
        ), redirect_stdout(output):
            code = gate.run_steps(steps)
        self.assertEqual(code, gate.EXIT_FAILED)
        self.assertEqual(ran, ["Package selection"])
        self.assertIn("COULD-NOT-RUN: Loopback aliases", output.getvalue())
        self.assertIn("sudo ifconfig lo0 alias 127.0.1.3 up", output.getvalue())
        self.assertIn("COULD-NOT-RUN: Go tests — not attempted", output.getvalue())

    def test_bound_aliases_pass(self):
        fake, _ = refusing()
        check = self.gate([LOADGEN])
        with patch.object(
            gate, "unbindable_loopbacks", lambda: PROBE(open_socket=fake)
        ), redirect_stdout(io.StringIO()):
            self.assertIsNone(check.loopback_aliases())

    def test_it_is_not_required_without_loadgen(self):
        check = self.gate(["mod/internal/api"])
        with patch.object(gate, "unbindable_loopbacks", side_effect=AssertionError):
            self.assertTrue(check.loopback_aliases().startswith("NOT REQUIRED"))

    def test_it_runs_after_package_selection_and_before_go_tests(self):
        names = [name for name, _ in self.gate([]).steps()]
        self.assertLess(names.index("Package selection"), names.index("Loopback aliases"))
        self.assertLess(names.index("Loopback aliases"), names.index("PostgreSQL"))
        self.assertLess(names.index("Loopback aliases"), names.index("Go tests"))
        actions = dict(self.gate([]).steps())
        self.assertEqual(actions["Loopback aliases"].__name__, "loopback_aliases")


if __name__ == "__main__":
    unittest.main()
