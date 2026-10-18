#!/usr/bin/env python3
"""Create a local, passphrase-capable owner key without replacing any file."""
import argparse
from pathlib import Path
import os
import signal
import subprocess
import tempfile


def create_key(path):
    public = Path(str(path) + ".pub")
    for target in (path, public):
        if os.path.lexists(target):
            raise FileExistsError(f"Refusing to replace {target}")
    with tempfile.TemporaryDirectory(prefix=".restmail-owner-", dir=path.parent) as temporary:
        private = Path(temporary) / "key"
        # OpenSSH reads the passphrase from the owner's terminal, never an argument.
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-C", "REST Mail owner recovery",
                        "-f", str(private)], check=True)
        private.chmod(0o600)
        created = []
        try:
            # link() fails even if a target appears after the checks above.
            for source, target in ((private, path), (Path(str(private) + ".pub"), public)):
                os.link(source, target)
                created.append((source, target))
        except OSError:
            for source, target in reversed(created):
                if os.path.lexists(target) and os.path.samefile(source, target):
                    target.unlink()
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("filename", type=Path)
    args = parser.parse_args()

    def terminate(_signum, _frame):
        raise SystemExit(143)

    signal.signal(signal.SIGTERM, terminate)
    try:
        create_key(args.filename)
    except (OSError, subprocess.CalledProcessError) as error:
        parser.exit(2, f"Owner key was not created: {error}\n")
    except KeyboardInterrupt:
        parser.exit(130, "Owner key creation cancelled.\n")


if __name__ == "__main__":
    main()
