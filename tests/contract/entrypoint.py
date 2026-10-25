"""Load the shared hooks and register fixtures that reuse their setup session."""

import runpy
from pathlib import Path

directory = Path(__file__).resolve().parent
shared = runpy.run_path(str(directory / "hooks.py"))
policy = runpy.run_path(str(directory / "policy_lookup_hooks.py"))
policy["register_policy_absent_hook"](shared["admin_token"])
