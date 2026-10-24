#!/usr/bin/env python3
# test_smoke.py — bin/smoke.sh 自檢：參數守衛、危險服務預設拒跑、沒有 podman 軌時跳過、失敗後清 pod 並繼續、步驟順序。
# 每個案例造一個假 repo：bin/ 放 smoke.sh 副本與假 svc.sh，PATH 前面放假 sudo，三者共用一份紀錄檔。
import os
import shutil
import subprocess
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SMOKE = os.path.join(ROOT, "bin", "smoke.sh")

DEPLOYMENT = """\
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {name}
spec:
  selector:
    matchLabels:
      app: {name}
  template:
    metadata:
      labels:
        app: {name}
    spec:
      containers:
      - name: c
        image: example.io/x:1
"""

# 假 svc.sh：記下參數；FAIL_APPLY 列出的服務 apply 時失敗
FAKE_SVC = """\
#!/bin/bash
printf 'svc %s\\n' "$*" >> "{log}"
if [[ "$2" == apply && " ${{FAIL_APPLY:-}} " == *" $1 "* ]]; then exit 1; fi
exit 0
"""

# 假 sudo：記下參數；pod inspect 一律 Running，pod exists 一律回 1（已刪）
FAKE_SUDO = """\
#!/bin/bash
printf 'sudo %s\\n' "$*" >> "{log}"
case "$*" in
  "podman pod inspect "*) echo Running ;;
  "podman pod exists "*) exit 1 ;;
esac
exit 0
"""


def run(args, services=("a", "b"), fail_apply="", no_podman=()):
    with tempfile.TemporaryDirectory() as tmp:
        repo = os.path.join(tmp, "repo")
        os.makedirs(os.path.join(repo, "bin"))
        shutil.copy(SMOKE, os.path.join(repo, "bin", "smoke.sh"))
        log = os.path.join(tmp, "calls.log")
        open(os.path.join(repo, "bin", "svc.sh"), "w").write(FAKE_SVC.format(log=log))
        for name in services:
            track = "k8s" if name in no_podman else "podman"
            d = os.path.join(repo, "services", name, track)
            os.makedirs(d)
            open(os.path.join(d, "deployment.yaml"), "w").write(DEPLOYMENT.format(name=name))
            open(os.path.join(d, "kustomization.yaml"), "w").write("resources:\n  - deployment.yaml\n")
        stub = os.path.join(tmp, "stub")
        os.makedirs(stub)
        open(os.path.join(stub, "sudo"), "w").write(FAKE_SUDO.format(log=log))
        os.chmod(os.path.join(stub, "sudo"), 0o755)
        env = dict(os.environ, PATH=stub + os.pathsep + os.environ["PATH"], FAIL_APPLY=fail_apply)
        r = subprocess.run(["bash", os.path.join(repo, "bin", "smoke.sh"), *args],
                           capture_output=True, text=True, env=env, input="")
        calls = open(log).read().splitlines() if os.path.exists(log) else []
        return r, calls


def test_no_args_fails():
    r, calls = run([])
    assert r.returncode == 1, r.stdout + r.stderr
    assert "usage" in r.stderr, r.stderr
    assert calls == [], calls


def test_privileged_refused_without_flag():
    r, calls = run(["ncb"], services=("ncb",))
    assert r.returncode == 1, r.stdout + r.stderr
    assert not [c for c in calls if c.startswith("svc ")], calls


def test_no_podman_track_is_skipped_with_reason():
    r, calls = run(["kadm", "a"], services=("kadm", "a"), no_podman=("kadm",))
    assert r.returncode == 0, r.stdout + r.stderr
    assert "沒有 podman 軌" in r.stdout, r.stdout
    assert not [c for c in calls if c.startswith("svc kadm")], calls


def test_apply_failure_cleans_and_continues():
    r, calls = run(["a", "b"], fail_apply="a")
    assert r.returncode == 1, r.stdout + r.stderr
    assert "sudo podman pod rm -f a-pod" in calls, calls
    assert "svc b delete podman" in calls, calls


def test_all_pass_rc0():
    r, calls = run(["a", "b"])
    assert r.returncode == 0, r.stdout + r.stderr
    a = [c for c in calls if c.startswith("svc a") or "a-pod" in c]
    assert a[0] == "svc a apply podman", a
    assert a[-2] == "svc a delete podman", a
    assert a[-1] == "sudo podman pod exists a-pod", a
    assert a.count("svc a apply podman") == 2, a
    assert sum(1 for c in a if c.startswith("sudo podman pod inspect a-pod")) == 2, a


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
