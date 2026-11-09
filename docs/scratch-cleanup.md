# Cleaning up a worker's scratch run

Parallel workers share one scratch root, such as `$HOME/tmp/rest-mail-loop`. Each
worker owns exactly one run directory below it. Removing the root, a sibling,
`handoff.md` or the key beside them destroys other workers' gates and evidence
(#2065).

Use `scripts/scratch_cleanup.py` to remove a run directory. It prevents unsafe cleanup
through the helper. It does not sandbox a shell `rm`: running `rm -rf` on the shared
root is still wrong and is still prohibited.

```sh
root="$HOME/tmp/rest-mail-loop"     # must already exist
run=2065-cleanup-guard-<unique-id>  # one plain directory name, assigned to you
owner=worker-2065                   # your own identity, passed every time

python3 -B scripts/scratch_cleanup.py create   --root "$root" --run-id "$run" --owner "$owner"
# ... work only inside "$root/$run" ...
python3 -B scripts/scratch_cleanup.py complete --root "$root" --run-id "$run" --owner "$owner"
python3 -B scripts/scratch_cleanup.py clean    --root "$root" --run-id "$run" --owner "$owner" --dry-run
python3 -B scripts/scratch_cleanup.py clean    --root "$root" --run-id "$run" --owner "$owner"
python3 -B scripts/scratch_cleanup.py list     --root "$root"   # read only
```

`create` writes a marker (`.restmail-run-owner.json`) naming the run, the root and the
owner. `clean` removes the run only when all of these hold; otherwise it removes
nothing and exits with status 3:

- The root is an absolute path to a real directory. It is not a symlink, `/`, the home
  directory or a parent of it.
- The run id is one plain name. It has no separator, no `..` and no leading dash.
- The run is a real directory directly under the root, owned by you. A symlink is refused.
- The run holds a marker that is a small regular file of yours, not a symlink. The marker
  names this run, this root and exactly the owner you pass.
- The marker says the run is complete. An active run is refused, including your own.

Ownership is never inferred. Passing the owner and the run id is required, a shared name
prefix gives no ownership, and a run without a marker is refused. Only the owner can mark
a run complete. `list` and `clean --dry-run` never remove anything.

The tests are in `scripts/tests/test_scratch_cleanup.py`. They use disposable fixtures
and cover the shared root, siblings, traversal, symlinks, missing or wrong ownership and
active runs of other workers.
