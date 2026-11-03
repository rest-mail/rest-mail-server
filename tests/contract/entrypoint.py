"""Load the shared hooks and register fixtures that reuse their setup session."""

import runpy
from pathlib import Path

directory = Path(__file__).resolve().parent
shared = runpy.run_path(str(directory / "hooks.py"))
targets = runpy.run_path(str(directory / "coverage_target_hooks.py"))
targets["register_coverage_target_hooks"](shared)
policy = runpy.run_path(str(directory / "policy_lookup_hooks.py"))
policy["register_policy_absent_hook"](shared["admin_token"])
own = runpy.run_path(str(directory / "own_account_hooks.py"))
own["register_own_account_hooks"](shared["admin_token"])
due = runpy.run_path(str(directory / "thread_due_hooks.py"))
due["register_thread_due_hook"](shared["admin_token"])
snooze = runpy.run_path(str(directory / "thread_snooze_hooks.py"))
snooze["register_thread_snooze_hook"](shared["admin_token"])
dmarc = runpy.run_path(str(directory / "dmarc_report_hooks.py"))
dmarc["register_dmarc_report_hook"](shared["admin_token"])
quiet = runpy.run_path(str(directory / "quiet_hours_hooks.py"))
quiet["register_quiet_hours_hooks"](shared["admin_token"])
contact_draft = runpy.run_path(str(directory / "contact_draft_hooks.py"))
contact_draft["register_contact_draft_hooks"](shared["fixture_call"], shared["ADMIN"])
