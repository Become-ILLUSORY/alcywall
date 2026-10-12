#!/usr/bin/env python3
"""Push a local git tree to GitHub via the Git Data API.

Plain `git push` over HTTPS is throttled in this sandbox (TLS resets /
"operation too slow"), while `gh api` works fine. So: upload every file as a
blob, assemble a tree, commit, and move the branch ref.

An empty repository rejects the blobs API with HTTP 409, so the first file is
seeded through the Contents API to create the initial commit; everything else
then rides on one tree/commit/ref update.
"""
import base64
import json
import os
import subprocess
import sys

REPO = "Become-ILLUSORY/alcywall"
BRANCH = "main"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def gh(method, path, payload=None):
    cmd = ["gh", "api", "-X", method, path]
    if payload is not None:
        cmd += ["--input", "-"]
    proc = subprocess.run(
        cmd,
        input=json.dumps(payload) if payload is not None else None,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise SystemExit(f"gh api {method} {path} failed:\n{proc.stderr}")
    out = proc.stdout.strip()
    return json.loads(out) if out else {}


def tracked_files():
    out = subprocess.run(
        ["git", "ls-files"], cwd=ROOT, capture_output=True, text=True, check=True
    ).stdout
    return [line for line in out.splitlines() if line.strip()]


def read_b64(path):
    with open(os.path.join(ROOT, path), "rb") as fh:
        return base64.b64encode(fh.read()).decode()


def branch_tip():
    """Current commit sha of the branch, or None when it does not exist."""
    proc = subprocess.run(
        ["gh", "api", f"/repos/{REPO}/git/ref/heads/{BRANCH}"],
        capture_output=True, text=True,
    )
    if proc.returncode != 0:
        return None
    return json.loads(proc.stdout)["object"]["sha"]


def main():
    files = tracked_files()
    if not files:
        raise SystemExit("no tracked files — run `git add -A` first")

    tip = branch_tip()
    if tip is None:
        # Empty repo: seed one file so the Git Data API starts working.
        seed = "README.md" if "README.md" in files else files[0]
        print(f"empty repository — seeding {seed} via the contents API…")
        gh("PUT", f"/repos/{REPO}/contents/{seed}", {
            "message": "chore: bootstrap repository",
            "content": read_b64(seed),
            "branch": BRANCH,
        })
        files = [f for f in files if f != seed]
        tip = branch_tip()
        print(f"initial commit: {tip}")

    print(f"uploading {len(files)} file(s) as blobs…")
    entries = []
    for path in files:
        blob = gh("POST", f"/repos/{REPO}/git/blobs",
                  {"content": read_b64(path), "encoding": "base64"})
        entries.append({"path": path, "mode": "100644", "type": "blob",
                        "sha": blob["sha"]})
        print(f"  {path}")

    message = subprocess.run(
        ["git", "log", "-1", "--pretty=%B"], cwd=ROOT,
        capture_output=True, text=True, check=True
    ).stdout.strip()

    print("creating tree…")
    tree = gh("POST", f"/repos/{REPO}/git/trees",
              {"base_tree": tip, "tree": entries})

    print("creating commit…")
    commit = gh("POST", f"/repos/{REPO}/git/commits",
                {"message": message, "tree": tree["sha"], "parents": [tip]})
    print(f"commit: {commit['sha']}")

    gh("PATCH", f"/repos/{REPO}/git/refs/heads/{BRANCH}",
       {"sha": commit["sha"], "force": True})

    print(f"\npushed to https://github.com/{REPO}/tree/{BRANCH}")


if __name__ == "__main__":
    sys.exit(main())
