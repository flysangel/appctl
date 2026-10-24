#!/usr/bin/env python3
# test_run_tests.py — bin/run-tests.py 自檢：自動收集測試檔，每支都要印 ALL <n> PASS 且結束碼為 0。
# 每個案例在暫存目錄 git init 一個假 repo，放 run-tests.py 的副本，不碰真 repo 的 tests/。
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
RUN_TESTS = os.path.join(ROOT, "bin", "run-tests.py")

PASSING = """\
def test_a():
    assert True


if __name__ == "__main__":
    test_a()
    print("ALL 1 PASS")
"""

NO_MAIN = """\
def test_a():
    assert True
"""

FAILING = """\
raise SystemExit(1)
"""

PASS_LINE_BAD_RC = """\
import sys
print("ALL 1 PASS")
sys.exit(3)
"""


def run_in_repo(files, gitignore=None, add=True):
    """在暫存的假 repo 放 files（相對路徑 → 內容）並跑 run-tests.py，回傳 CompletedProcess。"""
    with tempfile.TemporaryDirectory() as tmp:
        subprocess.run(["git", "init", "-q", tmp], check=True)
        os.makedirs(os.path.join(tmp, "bin"))
        os.makedirs(os.path.join(tmp, "tests"))
        shutil.copy(RUN_TESTS, os.path.join(tmp, "bin", "run-tests.py"))
        if gitignore is not None:
            open(os.path.join(tmp, ".gitignore"), "w").write(gitignore)
        for rel, body in files.items():
            open(os.path.join(tmp, rel), "w").write(body)
        if add:
            subprocess.run(["git", "-C", tmp, "add", "-A"], check=True)
        return subprocess.run(
            [sys.executable, os.path.join(tmp, "bin", "run-tests.py")],
            capture_output=True, text=True,
        )


def test_all_pass():
    r = run_in_repo({"tests/test_a.py": PASSING, "tests/test_b.py": PASSING})
    assert r.returncode == 0, r.stdout + r.stderr
    assert "Success:" in r.stdout, r.stdout


def test_missing_main_fails():
    r = run_in_repo({"tests/test_a.py": PASSING, "tests/test_nomain.py": NO_MAIN})
    assert r.returncode == 1, r.stdout + r.stderr
    assert "test_nomain.py" in r.stderr, r.stderr


def test_untracked_file_is_collected():
    r = run_in_repo({"tests/test_a.py": PASSING, "tests/test_new.py": FAILING}, add=False)
    assert r.returncode == 1, r.stdout + r.stderr
    assert "test_new.py" in r.stderr, r.stderr


def test_gitignored_file_is_skipped():
    r = run_in_repo(
        {"tests/test_a.py": PASSING, "tests/test_local.py": FAILING},
        gitignore="tests/test_local.py\n",
    )
    assert r.returncode == 0, r.stdout + r.stderr


def test_zero_files_fails():
    r = run_in_repo({})
    assert r.returncode == 1, r.stdout + r.stderr
    assert "Error:" in r.stderr, r.stderr


def test_pass_line_but_nonzero_rc_fails():
    r = run_in_repo({"tests/test_a.py": PASS_LINE_BAD_RC})
    assert r.returncode == 1, r.stdout + r.stderr
    assert "test_a.py" in r.stderr, r.stderr


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
