"""Every service container asks for the highest oom_score_adj, or it cannot be exec'd.

GitHub runs a service container by talking to the docker-compatible API that the runner's
podman sidecar serves. For every `docker exec` it stamps the container's OomScoreAdj onto
the exec'd process, and the sidecar sits at a high Burstable score. Lowering a process's
oom_score_adj needs CAP_SYS_RESOURCE in the *initial* user namespace, which a container
that is itself in a user namespace (`hostUsers: false`, as the ARC runner pods are) can
never hold - granting the capability does not help, and nor does anything in the image.

So every service definition asks for 1000, the maximum. That makes the stamp a raise
rather than a lower, and a raise needs no capability. Without it the service never turns
healthy and the shard dies before running a test:

    postgres service is starting ... 32 seconds
    unhealthy
    ##[error]Failed to initialize container postgres:16-alpine

Anything inside `options` is passed to docker verbatim, comments included. A `#` there is
not a YAML comment: it becomes part of the command line and fails with
`invalid reference format`.

Both rules are checked here, over every workflow, so a new service cannot quietly arrive
without them.
"""

from pathlib import Path

import yaml


WORKFLOWS = Path(__file__).resolve().parents[1] / ".github/workflows"

# The flag every service container must carry, and why it is spelled with the maximum.
REQUIRED_FLAG = "--oom-score-adj 1000"


def service_problems(workflows: dict[str, dict]) -> list[str]:
    """Every service container that cannot be exec'd, named by file, job and service."""

    problems: list[str] = []
    for name, document in sorted(workflows.items()):
        for job_name, job in _jobs(document):
            services = job.get("services")
            if not isinstance(services, dict):
                continue
            for service_name, config in services.items():
                where = f"{name}: job {job_name} service {service_name}"
                if not isinstance(config, dict):
                    problems.append(f"{where}: not a mapping, so its options cannot be read")
                    continue
                options = config.get("options")
                if not isinstance(options, str):
                    problems.append(f"{where}: no options, so it does not ask for {REQUIRED_FLAG}")
                    continue
                if REQUIRED_FLAG not in options:
                    problems.append(
                        f"{where}: options do not ask for {REQUIRED_FLAG}, so every docker exec "
                        f"into it is refused and its health check can never pass"
                    )
                if "#" in options:
                    problems.append(
                        f"{where}: options contain '#', which docker receives verbatim as part "
                        f"of the command line rather than as a YAML comment"
                    )
    return problems


def _jobs(document: dict):
    jobs = document.get("jobs") if isinstance(document, dict) else None
    if not isinstance(jobs, dict):
        return []
    return [(name, job) for name, job in jobs.items() if isinstance(job, dict)]


def service_count(workflows: dict[str, dict]) -> int:
    """How many service containers were seen, so a test can refuse to pass on zero."""

    total = 0
    for document in workflows.values():
        for _, job in _jobs(document):
            services = job.get("services")
            if isinstance(services, dict):
                total += len(services)
    return total


def load_workflows(directory: Path = WORKFLOWS) -> dict[str, dict]:
    workflows: dict[str, dict] = {}
    for path in sorted(directory.glob("*.yml")) + sorted(directory.glob("*.yaml")):
        workflows[path.name] = yaml.safe_load(path.read_text())
    return workflows
