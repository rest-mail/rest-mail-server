"""Image build steps stay off emulation wherever they do not need it (#1855).

docker.yml builds linux/arm64 on x86 runners, so a step in a stage that takes the
target's platform runs under QEMU. Node crashed there and hung the job (#1850).
Go cross-compiles and js-filter's modules are the same files on every platform,
so only the stage that assembles the image needs the target's platform.
"""

import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOCKER_WORKFLOW = ROOT / ".github" / "workflows" / "docker.yml"

GO_IMAGES = {
    "Dockerfile": "restmail-api",
    "projects/smtp-gateway/Dockerfile": "smtp-gateway",
    "projects/imap-gateway/Dockerfile": "imap-gateway",
    "projects/pop3-gateway/Dockerfile": "pop3-gateway",
}
JS_FILTER = "projects/js-filter-sidecar/Dockerfile"
WEB_APPS = ("webmail/Dockerfile", "admin/Dockerfile")


def stages(dockerfile):
    """Each stage's name, mapped to its FROM line and the instructions after it."""
    text = (ROOT / dockerfile).read_text(encoding="utf-8")
    # A line ending in a backslash continues on the next line.
    joined = re.sub(r"\\\n", " ", text)
    found = {}
    for instruction in re.findall(r"(?m)^(?!#)\S.*$", joined):
        if instruction.startswith("FROM "):
            name = instruction.rsplit(" AS ", 1)[1]
            found[name] = {"from": instruction, "body": []}
        else:
            found[name]["body"].append(instruction)
    return found


class GoImageTests(unittest.TestCase):
    def test_modules_are_fetched_on_the_build_platform(self):
        for dockerfile in GO_IMAGES:
            with self.subTest(dockerfile):
                self.assertIn(
                    "--platform=$BUILDPLATFORM", stages(dockerfile)["deps"]["from"]
                )

    def test_the_build_stays_on_the_module_stage(self):
        for dockerfile in GO_IMAGES:
            with self.subTest(dockerfile):
                self.assertEqual(
                    stages(dockerfile)["builder"]["from"], "FROM deps AS builder"
                )

    def test_the_binary_is_cross_compiled_for_the_target(self):
        for dockerfile, binary in GO_IMAGES.items():
            with self.subTest(dockerfile):
                body = stages(dockerfile)["builder"]["body"]
                self.assertIn("ARG TARGETOS", body)
                self.assertIn("ARG TARGETARCH", body)
                build = next(line for line in body if " go build " in line)
                # Without cgo, the compiler needs nothing from the target.
                self.assertIn(
                    "CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build",
                    build,
                )
                self.assertIn(f"-o /bin/{binary} ", build)

    def test_the_prod_stage_takes_the_target_platform_and_the_binary(self):
        for dockerfile, binary in GO_IMAGES.items():
            with self.subTest(dockerfile):
                prod = stages(dockerfile)["prod"]
                self.assertNotIn("--platform", prod["from"])
                self.assertIn(
                    f"COPY --from=builder /bin/{binary} /usr/local/bin/{binary}",
                    prod["body"],
                )


class JsFilterImageTests(unittest.TestCase):
    def test_install_runs_on_the_build_platform(self):
        deps = stages(JS_FILTER)["deps"]
        self.assertIn("--platform=$BUILDPLATFORM", deps["from"])
        self.assertIn("RUN npm ci --omit=dev --ignore-scripts", deps["body"])

    def test_the_prod_stage_takes_the_target_platform_and_the_modules(self):
        prod = stages(JS_FILTER)["prod"]
        self.assertNotIn("--platform", prod["from"])
        self.assertIn("COPY --from=deps /app/node_modules ./node_modules", prod["body"])
        self.assertFalse([line for line in prod["body"] if "npm " in line])

    def test_nothing_installed_depends_on_the_platform(self):
        # The move is only right while node_modules is the same files everywhere.
        # A package that names a platform or runs a script at install has to go
        # back to installing in the prod stage, so this fails to make that choice.
        lockfile = json.loads(
            (ROOT / "projects/js-filter-sidecar/package-lock.json").read_text(
                encoding="utf-8"
            )
        )
        for name, package in lockfile["packages"].items():
            with self.subTest(name or "the project"):
                for field in ("os", "cpu", "libc", "hasInstallScript"):
                    self.assertNotIn(field, package)


class EntrypointModeTests(unittest.TestCase):
    def test_modes_are_set_by_copy_and_not_by_an_emulated_step(self):
        for dockerfile in (*GO_IMAGES, *WEB_APPS):
            with self.subTest(dockerfile):
                prod = stages(dockerfile)["prod"]["body"]
                self.assertFalse(
                    [
                        line
                        for line in prod
                        if line.startswith("RUN ") and "chmod " in line
                    ]
                )
                self.assertTrue(
                    [line for line in prod if line.startswith("COPY --chmod=0755 ")]
                )

    def test_the_web_apps_config_directory_is_made_before_its_file(self):
        # COPY --chmod=0644 would give a directory it makes that mode as well,
        # and nginx's uid could not enter it.
        for dockerfile in WEB_APPS:
            with self.subTest(dockerfile):
                prod = stages(dockerfile)["prod"]["body"]
                self.assertLess(
                    prod.index("WORKDIR /etc/nginx/restmail"),
                    prod.index("COPY --chmod=0644 nginx.conf default.conf.template"),
                )


class PublishingTests(unittest.TestCase):
    def test_both_platforms_are_still_published(self):
        self.assertIn(
            "platforms: linux/amd64,linux/arm64",
            DOCKER_WORKFLOW.read_text(encoding="utf-8"),
        )


if __name__ == "__main__":
    unittest.main()
