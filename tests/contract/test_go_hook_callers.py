"""The Go tests that register a contract hook themselves call it as entrypoint.py does.

A test ending in WithVertrag writes a small Python file that loads one of the hook
files here and calls its register function, inside a Go string. Unit CI skips those
tests (they need the Vertrag binary), so a register function whose signature changes
can leave such a caller broken until the merge queue's contract pairs run. This reads
each of those calls out of the Go source, evaluates its arguments, and holds them to
the function's signature and to what entrypoint.py passes for each parameter: a
callable where entrypoint.py passes one of hooks.py's functions, and a value of the
same type where it passes a literal.
"""

import ast
import inspect
import re
import runpy
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
CONTRACT = ROOT / "tests" / "contract"

# loaded['register_x'](ARGUMENTS)\n, inside a Go string literal: the \n is the two
# characters of the Go escape. %q and %d are the Go verbs fmt.Sprintf fills in.
GO_CALL = re.compile(r"loaded\['(register_\w+)'\]\((.*?)\)\\n")
GO_VERBS = {"%q": "'go-value'", "%d": "1", "%s": "'go-value'"}


def go_callers():
    """(Go file, register function, argument source) for each call in a Go test."""
    found = []
    for path in sorted((ROOT / "internal").rglob("*_test.go")):
        text = path.read_text()
        for match in GO_CALL.finditer(text):
            arguments = match.group(2)
            for verb, value in GO_VERBS.items():
                arguments = arguments.replace(verb, value)
            found.append((path.relative_to(ROOT), match.group(1), arguments))
    return found


def register_functions():
    """Each register function a hook file here defines, with the file that defines it."""
    stub = types.SimpleNamespace(
        before=lambda operation: lambda callback: callback,
        after=lambda operation: lambda callback: callback,
        stash={},
    )
    functions = {}
    for path in sorted(CONTRACT.glob("*_hooks.py")):
        if not re.search(r"^def register_\w+\(", path.read_text(), re.MULTILINE):
            continue
        with patch.dict(sys.modules, {"vertrag_hooks": stub}):
            loaded = runpy.run_path(str(path))
        for name, value in loaded.items():
            if name.startswith("register_") and inspect.isfunction(value):
                functions[name] = value
    return functions


def hooks_functions():
    """The names hooks.py defines with def: what entrypoint.py's shared[...] calls."""
    tree = ast.parse((CONTRACT / "hooks.py").read_text())
    return {node.name for node in tree.body if isinstance(node, ast.FunctionDef)}


def entrypoint_kinds(functions):
    """For each register function entrypoint.py calls, what it passes each parameter:
    "callable" for one of hooks.py's functions, or the type of a literal."""
    defined = hooks_functions()
    kinds = {}
    tree = ast.parse((CONTRACT / "entrypoint.py").read_text())
    for node in ast.walk(tree):
        if not (
            isinstance(node, ast.Call)
            and isinstance(node.func, ast.Subscript)
            and isinstance(node.func.slice, ast.Constant)
            and node.func.slice.value in functions
        ):
            continue
        name = node.func.slice.value
        bound = inspect.signature(functions[name]).bind(
            *node.args, **{k.arg: k.value for k in node.keywords}
        )
        for parameter, value in bound.arguments.items():
            if (
                isinstance(value, ast.Subscript)
                and isinstance(value.value, ast.Name)
                and value.value.id == "shared"
                and isinstance(value.slice, ast.Constant)
                and value.slice.value in defined
            ):
                kind = "callable"
            else:
                try:
                    kind = type(ast.literal_eval(value))
                except ValueError:
                    continue
            kinds.setdefault(name, {}).setdefault(parameter, set()).add(kind)
    return kinds


class GoHookCallers(unittest.TestCase):
    def setUp(self):
        self.callers = go_callers()
        self.functions = register_functions()
        self.kinds = entrypoint_kinds(self.functions)

    def test_every_go_call_is_read(self):
        # The pattern must not miss a call the Go tests make, or this checks nothing.
        written = sum(
            path.read_text().count("loaded['register_")
            for path in (ROOT / "internal").rglob("*_test.go")
        )
        self.assertEqual(len(self.callers), written)
        self.assertIn(
            "register_quiet_hours_hooks", {name for _, name, _ in self.callers}
        )

    def test_each_go_call_fits_the_signature_and_what_entrypoint_passes(self):
        for path, name, arguments in self.callers:
            with self.subTest(path=str(path), function=name):
                self.assertIn(name, self.functions, "no hook file defines it")
                call = ast.parse(f"f({arguments})", mode="eval").body
                values = [
                    eval(compile(ast.Expression(a), "<go>", "eval")) for a in call.args
                ]
                keywords = {
                    k.arg: eval(compile(ast.Expression(k.value), "<go>", "eval"))
                    for k in call.keywords
                }
                bound = inspect.signature(self.functions[name]).bind(
                    *values, **keywords
                )
                expected = self.kinds.get(name, {})
                for parameter, value in bound.arguments.items():
                    for kind in expected.get(parameter, ()):
                        if kind == "callable":
                            self.assertTrue(
                                callable(value),
                                f"{parameter} gets {value!r}; entrypoint.py passes a function",
                            )
                        else:
                            self.assertIsInstance(value, kind, parameter)


if __name__ == "__main__":
    unittest.main()
