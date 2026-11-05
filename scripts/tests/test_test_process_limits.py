"""A test's limit on a process it starts is there to stop a hang, not to time it.

On a machine at load 170 a shell that runs in milliseconds took over 5 seconds, so
a short limit fails a good test (#1942). A process that truly hangs still stops at
this limit, and each job has its own timeout above that.
"""

import ast
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
SUITES = ("scripts/tests", "tests/contract", "tests/load")
LOWEST_LIMIT_SECONDS = 600
# Each of these waits on a process it starts, and each takes a limit.
STARTERS = {"run", "call", "check_call", "check_output"}


def starts_a_process(call):
    function = call.func
    return (
        isinstance(function, ast.Attribute)
        and isinstance(function.value, ast.Name)
        and function.value.id == "subprocess"
        and function.attr in STARTERS
    )


def passes_its_limit_on(function):
    return any(
        isinstance(node, ast.Call)
        and starts_a_process(node)
        and any(
            keyword.arg == "timeout"
            and isinstance(keyword.value, ast.Name)
            and keyword.value.id == "timeout"
            for keyword in node.keywords
        )
        for node in ast.walk(function)
    )


def default_limits(function):
    """The default of a parameter named timeout that goes on to a process."""
    if not passes_its_limit_on(function):
        return []
    arguments = function.args
    names = arguments.posonlyargs + arguments.args
    positional = names[len(names) - len(arguments.defaults) :]
    pairs = list(zip(positional, arguments.defaults))
    pairs += [
        (name, default)
        for name, default in zip(arguments.kwonlyargs, arguments.kw_defaults)
        if default is not None
    ]
    return [default for name, default in pairs if name.arg == "timeout"]


def short_limits(source, name="test"):
    """Each limit in this source that load alone could reach, as file:line."""
    tree = ast.parse(source)
    limits = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Call) and starts_a_process(node):
            limits += [k.value for k in node.keywords if k.arg == "timeout"]
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            limits += default_limits(node)
    return sorted(
        f"{name}:{limit.lineno}"
        for limit in limits
        if isinstance(limit, ast.Constant)
        and isinstance(limit.value, (int, float))
        and limit.value < LOWEST_LIMIT_SECONDS
    )


class TestProcessLimitTests(unittest.TestCase):
    def test_no_test_gives_a_process_a_limit_load_can_reach(self):
        found = []
        for suite in SUITES:
            for path in sorted((ROOT / suite).glob("test_*.py")):
                found += short_limits(path.read_text(), path.relative_to(ROOT))
        self.assertEqual(
            found,
            [],
            f"use timeout={LOWEST_LIMIT_SECONDS} or more: a test's limit stops a hang",
        )

    def test_the_scan_finds_a_short_limit_where_one_is_written(self):
        cases = {
            "subprocess.run(['sh'], timeout=5)": 1,
            "subprocess.check_output(['sh'], text=True, timeout=30.5)": 1,
            "def docker(*args, timeout=120):\n    subprocess.run(args, timeout=timeout)": 1,
            "def docker(check=True, timeout=120):\n    subprocess.run([], timeout=timeout)": 1,
        }
        for source, count in cases.items():
            with self.subTest(source=source):
                self.assertEqual(len(short_limits(source)), count)

    def test_the_scan_leaves_a_long_limit_and_other_calls_alone(self):
        for source in (
            "subprocess.run(['sh'], timeout=600)",
            "subprocess.run(['sh'])",
            "subprocess.run(['sh'], timeout=limit)",
            "fixture.assert_called_once_with(['chore'], timeout=20)",
            "urllib.request.urlopen(request, timeout=10)",
            "def docker(*args, timeout=600):\n    subprocess.run(args, timeout=timeout)",
            "def wait(condition, timeout=5):\n    pass",
        ):
            with self.subTest(source=source):
                self.assertEqual(short_limits(source), [])


if __name__ == "__main__":
    unittest.main()
