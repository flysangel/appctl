#!/usr/bin/env python3
# test_render_diff.py — bin/render-diff.sh 自檢：比對某個 revision 與工作樹的渲染產物，真值檔不落進暫存目錄。
# 每個案例 git init 一個假 repo，放 render-diff.sh 與 manifest-diff.py 的副本，與一個帶 secret.env.example 的服務。
import os
import shutil
import subprocess
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))

DEPLOYMENT = """\
apiVersion: apps/v1
kind: Deployment
metadata:
  name: s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: s
  template:
    metadata:
      labels:
        app: s
    spec:
      containers:
      - name: c
        image: example.io/x:1
"""

FILES = {
    "services/s/base/deployment.yaml": DEPLOYMENT,
    "services/s/base/kustomization.yaml": "resources:\n  - deployment.yaml\n",
    "services/s/k8s/kustomization.yaml": "namespace: default\nresources:\n  - ../base\n  - ../secret\n",
    "services/s/secret/kustomization.yaml": (
        "generatorOptions:\n  disableNameSuffixHash: true\n"
        "secretGenerator:\n  - name: s\n    envs:\n      - secret.env\n"),
    "services/s/secret/secret.env.example": "REAL=changeme\n",
    ".gitignore": "services/*/secret/secret.env\nservices/*/sync.secret.env\n",
}


def git(repo, *args):
    subprocess.run(["git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", *args],
                   check=True, capture_output=True)


def run(rev="HEAD", change=None, real_secret=None, keep=False):
    with tempfile.TemporaryDirectory() as tmp:
        repo = os.path.join(tmp, "repo")
        os.makedirs(os.path.join(repo, "bin"))
        for name in ("render-diff.sh", "manifest-diff.py"):
            shutil.copy(os.path.join(ROOT, "bin", name), os.path.join(repo, "bin", name))
        for rel, body in FILES.items():
            path = os.path.join(repo, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            open(path, "w").write(body)
        git(repo, "init", "-q")
        git(repo, "add", "-A")
        git(repo, "commit", "-qm", "init")
        if change:
            p = os.path.join(repo, "services/s/base/deployment.yaml")
            body = open(p).read().replace(*change)
            open(p, "w").write(body)
        if real_secret:
            # secret.env 會被 .example 補位蓋掉；sync.secret.env 不會，它是檢查「被擋的檔不複製」的關鍵
            open(os.path.join(repo, "services/s/secret/secret.env"), "w").write(real_secret)
            open(os.path.join(repo, "services/s/sync.secret.env"), "w").write(real_secret)
        scratch = os.path.join(tmp, "tmpdir")
        os.makedirs(scratch)
        env = dict(os.environ, TMPDIR=scratch)
        if keep:
            env["RENDER_DIFF_KEEP"] = "1"
        r = subprocess.run(["bash", os.path.join(repo, "bin", "render-diff.sh"), "s", "k8s", rev],
                           capture_output=True, text=True, env=env)
        left = []
        for dirpath, _, names in os.walk(scratch):
            for n in names:
                left.append(open(os.path.join(dirpath, n), errors="replace").read())
        return r, left


def test_no_change_rc0():
    r, _ = run()
    assert r.returncode == 0, r.stdout + r.stderr


def test_change_rc1():
    r, _ = run(change=("replicas: 1", "replicas: 2"))
    assert r.returncode == 1, r.stdout + r.stderr
    assert "replicas" in r.stdout + r.stderr, r.stdout + r.stderr


def test_bad_rev_rc2():
    r, _ = run(rev="nope")
    assert r.returncode == 2, r.stdout + r.stderr
    assert "Error:" in r.stderr, r.stderr


def test_real_secret_not_copied():
    r, left = run(real_secret="REAL=xyz-real-value\n", keep=True)
    assert r.returncode == 0, r.stdout + r.stderr
    assert left, "RENDER_DIFF_KEEP=1 時暫存目錄應該留著"
    assert not any("xyz-real-value" in body for body in left), "真值落進暫存目錄"


def test_tempdir_removed():
    r, left = run()
    assert left == [], "暫存目錄沒有清掉"


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
