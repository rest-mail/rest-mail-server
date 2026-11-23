# CI runner container startup

The ARC pool uses a Docker-compatible Podman sidecar scoped to the runner pod's
user namespace (`hostUsers: false`). The runner uses that endpoint, not the host
Docker socket. Preserve the user-namespace boundary; do not switch to the initial
namespace, grant extra host privileges, or use `sudo` to make a check pass.

The sidecar cannot lower a process's `oom_score_adj`: that requires
`CAP_SYS_RESOURCE` in the initial user namespace. Starting an exec target with
`--oom-score-adj 1000` makes crun's exec-time write a raise (or no change). Without
it, `docker exec` can fail before `pg_isready` runs. The CI service options in
[`.github/workflows/ci.yml:572`](../../.github/workflows/ci.yml#L572) and the
[Docker shim at `scripts/ci/docker-retry/docker:47`](../../scripts/ci/docker-retry/docker#L47)
enforce the setting. The CI service-option check is
[`scripts/check_service_containers.py`](../../scripts/check_service_containers.py).

The local gate waits up to 10 seconds for `docker info` before creating the service.
If that fails, start the selected Docker backend or check the context/socket in the
diagnostic; do not fall back to a host socket. PostgreSQL gets up to 60 seconds to
accept TCP connections before the database-backed test setup starts. A stopped
container is a startup failure, not a retry: the gate reports its name, status,
exit code, and up to 20 lines of logs. Probes and failure log collection each have
a two-second command limit.

Run the focused harness tests with the CI Python dependencies installed:

```sh
python3 -m unittest discover -s scripts/tests -p 'test_ci_substrate.py' -v
```

On an owned runner with Docker and Go, set `RESTMAIL_SUBSTRATE_LIVE=1` for the same
command. It starts an isolated ICU PostgreSQL database on a Docker-assigned
loopback port and exercises cleanup on real disposable processes and containers.
This checks the local harness, not the health of the hosted ARC pool.
