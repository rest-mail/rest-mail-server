"""Load the shared hooks and register fixtures that reuse their setup session."""

import runpy
from pathlib import Path

import vertrag_hooks

directory = Path(__file__).resolve().parent
shared = runpy.run_path(str(directory / "hooks.py"))
settings = runpy.run_path(str(directory / "account_settings_hooks.py"))
settings["register_absent_settings_hooks"](shared)
# Only a reset and the path, and only with a reset asked for (own_data.py):
# generated bodies stay whole.
vertrag_hooks.before("setDomainRelay")(shared["a_domain_setting_of_its_own"])
alias = runpy.run_path(str(directory / "alias_refusal_hooks.py"))
alias["register_alias_refusal_hooks"](shared["admin_token"])
targets = runpy.run_path(str(directory / "coverage_target_hooks.py"))
targets["register_coverage_target_hooks"](shared)
queue = runpy.run_path(str(directory / "queue_entry_hooks.py"))
queue["register_queue_entry_hooks"](shared)
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
quiet["register_quiet_hours_hooks"](
    shared["admin_token"], shared["asked_for_a_clean_start"]
)
# After the quiet hours hook, which, only without a reset asked for, finds a
# live account for the examples' 1 without changing the body: then the reset,
# and an account of its own, only with a reset asked for (own_data.py).
for operation in ("getQuietHours", "setQuietHours"):
    vertrag_hooks.before(operation)(shared["account_settings_of_its_own"])
contact_draft = runpy.run_path(str(directory / "contact_draft_hooks.py"))
contact_draft["register_contact_draft_hooks"](shared["fixture_call"], shared["ADMIN"])
postmaster = runpy.run_path(str(directory / "postmaster_hooks.py"))
postmaster["register_postmaster_hooks"](shared["mfa_admin_token"])
# Last, so that it sees the body every hook above leaves.
lost = runpy.run_path(str(directory / "lost_probes.py"))
lost["register_lost_probe_count"]()
