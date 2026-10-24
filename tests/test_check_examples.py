#!/usr/bin/env python3
# test_check_examples.py — bin/check-examples.py 自檢：services/*/secret/ 底下每份 .example 都沒被 .gitignore 擋掉。
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SCRIPT = os.path.join(ROOT, "bin", "check-examples.py")


def run(files, gitignore):
    with tempfile.TemporaryDirectory() as tmp:
        subprocess.run(["git", "init", "-q", tmp], check=True)
        open(os.path.join(tmp, ".gitignore"), "w").write(gitignore)
        for rel in files:
            path = os.path.join(tmp, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            open(path, "w").write("K=v\n")
        return subprocess.run([sys.executable, SCRIPT, tmp], capture_output=True, text=True)


# 照真 repo 的寫法：secret/ 全擋，只放行第一層的 .example
GITIGNORE = "services/*/secret/**\n!services/*/secret/*.example\n"


def test_ignored_example_fails():
    r = run(["services/a/secret/secret.env.example", "services/a/secret/sub/x.example"], GITIGNORE)
    assert r.returncode == 1, r.stdout + r.stderr
    assert "services/a/secret/sub/x.example" in r.stderr, r.stderr


def test_tracked_examples_pass():
    r = run(["services/a/secret/secret.env.example"], GITIGNORE)
    assert r.returncode == 0, r.stdout + r.stderr


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
