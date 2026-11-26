"""Release metadata, scoped changelogs, and installation of published Mac builds."""

import argparse
import json
import platform
import re
import shutil
import subprocess
import sys
import time
import tempfile
import zipfile
from pathlib import Path, PurePosixPath


def output(*command):
    return subprocess.check_output(command, text=True).strip()


def notes(previous=None, revision="HEAD"):
    revisions = [f"{previous}..{revision}"] if previous else [revision]
    return output(
        "git", "log", "--format=- %s (%h)", *revisions, "--", "desktop/", "webmail/"
    )


def installed_notes(install_dir):
    manifest = install_dir / "RESTMail.app/Contents/Resources/release.json"
    if not manifest.is_file():
        return f"No installed release in {install_dir}; no previous commit to compare."
    release = json.loads(manifest.read_text())
    previous = release["commit"].removesuffix("-dirty")
    if not re.fullmatch(r"[0-9a-f]{7,40}", previous):
        raise ValueError("Installed release has no valid Git commit")
    output("git", "rev-parse", "--verify", f"{previous}^{{commit}}")
    return (
        notes(previous) or "No desktop or webmail commits since the installed release."
    )


def release_notes(revision="HEAD", exclude_tag=None):
    tags = output(
        "git",
        "tag",
        "--merged",
        revision,
        "--list",
        "desktop-v*",
        "--sort=-version:refname",
    )
    previous = next(
        (
            tag
            for tag in tags.splitlines()
            if tag != exclude_tag and re.fullmatch(r"desktop-v\d+\.\d+\.\d+", tag)
        ),
        None,
    )
    return (
        notes(previous, revision)
        or "No desktop or webmail commits since the previous desktop release."
    )


SEMVER = re.compile(r"(\d+)\.(\d+)\.(\d+)")
RELEASE_TAG = re.compile(r"desktop-v\d+\.\d+\.\d+")
FEATURE = re.compile(r"feat(\([^)]*\))?!?:")
BREAKING = re.compile(r"[a-z]+(\([^)]*\))?!:")
FIRST_VERSION = "0.1.0"


def bump(version, subjects):
    """The semantic version after `version` for these conventional commit titles.

    A breaking change raises the major version (the minor one while the app is 0.x),
    a feature raises the minor version, and anything else raises the patch. No
    commits leave the version unchanged.
    """
    major, minor, patch = (int(part) for part in SEMVER.fullmatch(version).groups())
    subjects = list(subjects)
    if not subjects:
        return version
    if any(BREAKING.match(subject) for subject in subjects):
        return f"{major}.{minor + 1}.0" if major == 0 else f"{major + 1}.0.0"
    if any(FEATURE.match(subject) for subject in subjects):
        return f"{major}.{minor + 1}.0"
    return f"{major}.{minor}.{patch + 1}"


def previous_release_tag():
    tags = output("git", "tag", "--list", "desktop-v*", "--sort=-version:refname")
    return next((tag for tag in tags.splitlines() if RELEASE_TAG.fullmatch(tag)), None)


def unreleased_subjects(tag, revision="HEAD"):
    """Titles of the desktop and webmail commits in `revision` that `tag` lacks.

    `git cherry` compares patches, not hashes, so commits that were released and
    then rebased onto a newer main are not counted a second time.
    """
    subjects = []
    for line in output("git", "cherry", tag, revision).splitlines():
        if not line.startswith("+ "):
            continue
        commit = line[2:]
        touched = output(
            "git", "diff-tree", "--no-commit-id", "--name-only", "-r", commit,
            "--", ":/desktop", ":/webmail",
        )
        if touched:
            subjects.append(output("git", "log", "-1", "--format=%s", commit))
    return subjects


def next_version(revision="HEAD"):
    tag = previous_release_tag()
    if tag is None:
        return FIRST_VERSION
    return bump(tag.removeprefix("desktop-v"), unreleased_subjects(tag, revision))


def fetch_with_retries(command, attempts=3, delay=5, run=subprocess.run, sleep=time.sleep):
    """Runs a git fetch, retrying a failure: this Mac's GitHub SSH and TLS connections
    fail intermittently, and one dropped connection must not lose a finished build."""
    for attempt in range(1, attempts + 1):
        result = run(command, check=False)
        if result.returncode == 0:
            return
        if attempt < attempts:
            print(f"git fetch failed (attempt {attempt} of {attempts}); retrying in {delay}s", file=sys.stderr)
            sleep(delay)
    raise subprocess.CalledProcessError(result.returncode, command)


def publish(build_dir):
    metadata = json.loads((build_dir / "release.json").read_text())
    version = metadata["version"]
    commit = metadata["commit"]
    if version.endswith("-dirty") or commit.endswith("-dirty"):
        raise ValueError(
            "Refusing to publish a dirty build; commit changes and rebuild first"
        )
    if not re.fullmatch(r"\d+\.\d+\.\d+", version) or not re.fullmatch(
        r"[0-9a-f]{40}", commit
    ):
        raise ValueError(
            "Build metadata must contain a numeric release version and full Git commit"
        )
    bundle = build_dir / "RESTMail.app"
    if json.loads((bundle / "Contents/Resources/release.json").read_text()) != metadata:
        raise ValueError(
            "App bundle metadata differs from the last build's release.json"
        )
    fetch_with_retries(["git", "fetch", "--all", "--prune", "--tags"])
    branches = output(
        "git",
        "for-each-ref",
        f"--contains={commit}",
        "--format=%(refname)",
        "refs/remotes/",
    )
    if not branches:
        raise ValueError(
            f"Build commit is not on a remote branch. Run: git push origin {commit}:refs/heads/desktop-release-{version}"
        )
    tag = f"desktop-v{version}"
    pages = json.loads(
        output("gh", "api", "--paginate", "--slurp", "repos/{owner}/{repo}/releases")
    )
    if any(release["tag_name"] == tag for page in pages for release in page):
        raise ValueError(f"Release {tag} already exists; refusing to overwrite it")
    matching = output("git", "tag", "--list", tag)
    if matching and output("git", "rev-parse", f"{tag}^{{commit}}") != commit:
        raise ValueError(f"Tag {tag} already points to a different commit")
    subprocess.run(
        ["codesign", "--verify", "--deep", "--strict", str(bundle)], check=True
    )
    with tempfile.TemporaryDirectory(prefix="restmail-publish-") as directory:
        staging = Path(directory)
        archive = staging / f"RESTMail-{version}-darwin-arm64.zip"
        subprocess.run(
            ["ditto", "-c", "-k", "--keepParent", str(bundle), str(archive)], check=True
        )
        manifest = staging / "release.json"
        manifest.write_text(json.dumps(metadata) + "\n")
        changelog = staging / "release-notes.txt"
        changelog.write_text(release_notes(commit, exclude_tag=tag) + "\n")
        subprocess.run(
            [
                "gh",
                "release",
                "create",
                tag,
                str(archive),
                str(manifest),
                "--target",
                commit,
                "--title",
                f"REST Mail desktop {version}",
                "--notes-file",
                str(changelog),
                "--latest=false",
            ],
            check=True,
        )
    print(f"Published REST Mail desktop {version} ({commit})")


def latest(install_dir):
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        raise ValueError(
            "Published macOS builds currently require an Apple Silicon Mac"
        )
    pages = json.loads(
        output("gh", "api", "--paginate", "--slurp", "repos/{owner}/{repo}/releases")
    )
    releases = [
        release
        for page in pages
        for release in page
        if not release["draft"]
        and not release["prerelease"]
        and re.fullmatch(r"desktop-v\d+\.\d+\.\d+", release["tag_name"])
    ]
    if not releases:
        raise ValueError("No published desktop releases found")
    release = max(releases, key=lambda item: item["published_at"])
    tag = release["tag_name"]
    version = tag.removeprefix("desktop-v")
    asset = f"RESTMail-{version}-darwin-arm64.zip"
    install_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".restmail-", dir=install_dir) as directory:
        staging = Path(directory)
        subprocess.run(
            ["gh", "release", "download", tag, "--pattern", asset, "--dir", directory],
            check=True,
        )
        archive = staging / asset
        with zipfile.ZipFile(archive) as package:
            for entry in package.infolist():
                path = PurePosixPath(entry.filename)
                if path.is_absolute() or ".." in path.parts or "\\" in entry.filename:
                    raise ValueError("Release archive contains an unsafe path")
                if (entry.external_attr >> 16) & 0o170000 == 0o120000:
                    raise ValueError(
                        "Release archive contains an unsupported symbolic link"
                    )
        subprocess.run(["ditto", "-x", "-k", str(archive), str(staging)], check=True)
        bundle = staging / "RESTMail.app"
        metadata = json.loads((bundle / "Contents/Resources/release.json").read_text())
        if metadata["version"] != version:
            raise ValueError("Release metadata does not match the downloaded tag")
        subprocess.run(
            ["codesign", "--verify", "--deep", "--strict", str(bundle)], check=True
        )
        destination = install_dir / "RESTMail.app"
        if destination.exists():
            shutil.rmtree(destination)
        bundle.rename(destination)
    print(f"Installed: {destination}")
    print(json.dumps(metadata, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("installed-notes", "latest"):
        command = commands.add_parser(name)
        command.add_argument("--install-dir", type=Path, required=True)
    commands.add_parser("notes")
    commands.add_parser("next-version")
    publishing = commands.add_parser("publish")
    publishing.add_argument("--build-dir", type=Path, required=True)
    stamp = commands.add_parser("stamp")
    stamp.add_argument("--version", required=True)
    stamp.add_argument("--commit", required=True)
    stamp.add_argument("--built", required=True)
    stamp.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "latest":
            latest(args.install_dir)
        elif args.command == "publish":
            publish(args.build_dir)
        elif args.command == "installed-notes":
            print(installed_notes(args.install_dir))
        elif args.command == "notes":
            print(release_notes())
        elif args.command == "next-version":
            print(next_version())
        else:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(
                json.dumps(
                    {
                        "app": "REST Mail",
                        "version": args.version,
                        "commit": args.commit,
                        "builtAt": args.built,
                    }
                )
                + "\n"
            )
    except (
        OSError,
        ValueError,
        KeyError,
        subprocess.CalledProcessError,
        zipfile.BadZipFile,
    ) as error:
        parser.exit(1, f"Release failed: {error}\n")


if __name__ == "__main__":
    main()
