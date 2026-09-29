#!/usr/bin/env python3
# test_manifest_diff.py — bin/manifest-diff.py 自檢：相同、hostPath 正規化、真差異、白名單、
# --include-secrets（Secret 納入比對＋新舊制 type: 差異正規化）共五類。
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
TOOL = os.path.join(HERE, "..", "bin", "manifest-diff.py")
ROOT = os.path.abspath(os.path.join(HERE, ".."))
DIFF = os.path.join(ROOT, "bin", "manifest-diff.py")

POD_PVC = """\
apiVersion: v1
kind: Secret
metadata: { name: demo }
stringData: { K: v }
---
apiVersion: v1
kind: Pod
metadata: { name: demo }
spec:
  containers:
  - name: c
    image: x
  volumes:
  - name: data
    persistentVolumeClaim: { claimName: demo-data }
"""

POD_HOSTPATH = """\
apiVersion: v1
kind: Pod
metadata: { name: demo }
spec:
  volumes:
  - name: data
    hostPath: { path: /some/where/volume/data, type: DirectoryOrCreate }
  containers:
  - name: c
    image: x
"""


def _run(a, b, *args):
    with tempfile.TemporaryDirectory() as tmp:
        pa, pb = os.path.join(tmp, "a.yaml"), os.path.join(tmp, "b.yaml")
        open(pa, "w").write(a)
        open(pb, "w").write(b)
        return subprocess.run([sys.executable, TOOL, pa, pb, *args],
                              capture_output=True, text=True)


def test_identical_modulo_secret_and_key_order():
    r = _run(POD_PVC, POD_PVC.replace("stringData: { K: v }", "stringData: { K: other }"), "--track", "k8s")
    assert r.returncode == 0 and "零漂移" in r.stdout  # Secret doc 整份剔除，其值不參與比對


def test_hostpath_normalized_on_podman_track():
    r = _run(POD_HOSTPATH, POD_PVC, "--track", "podman")
    assert r.returncode == 0, r.stdout + r.stderr  # hostPath→claimName demo-data 正規化後相同


def test_real_diff_fails():
    r = _run(POD_PVC, POD_PVC.replace("image: x", "image: y"), "--track", "k8s")
    assert r.returncode == 1 and "image" in r.stdout


def test_allowlist_passes():
    r = _run(POD_PVC, POD_PVC.replace("image: x", "image: y"), "--track", "k8s",
             "--allow", "Pod/demo/spec/containers/0/image")
    assert r.returncode == 0


def test_new_side_still_hostpath_is_caught():
    # 核心守門目的：new 端遷移失敗仍是 hostPath（未轉 PVC）→ 必須抓到漂移、exit 1，不得洗白
    r = _run(POD_HOSTPATH, POD_HOSTPATH, "--track", "podman")
    assert r.returncode == 1, f"new 端殘留 hostPath 應判漂移，卻回報零漂移（偽陰性）\nstdout={r.stdout}\nstderr={r.stderr}"


def test_missing_key_caught():
    # _walk 聯集掃鍵：new 端多一個 key 要抓到
    r = _run(POD_PVC, POD_PVC.replace("image: x", "image: x\n    imagePullPolicy: IfNotPresent"), "--track", "k8s")
    assert r.returncode == 1, f"new 端多鍵應判漂移\nstdout={r.stdout}\nstderr={r.stderr}"


# 舊制產物：kubectl create secret generic --dry-run=client -o yaml 不輸出 type:
OLD = """\
apiVersion: v1
kind: Secret
metadata:
  name: demo
  namespace: default
data:
  PASS: aHVudGVyMg==
"""

# 新制產物：secretGenerator 輸出 type: Opaque
NEW = """\
apiVersion: v1
kind: Secret
metadata:
  name: demo
  namespace: default
data:
  PASS: aHVudGVyMg==
type: Opaque
"""

NEW_DIFFERENT_VALUE = NEW.replace("aHVudGVyMg==", "d3JvbmdwYXNz")


def run_diff(old, new, *extra):
    with tempfile.TemporaryDirectory() as tmp:
        a = os.path.join(tmp, "old.yaml"); open(a, "w").write(old)
        b = os.path.join(tmp, "new.yaml"); open(b, "w").write(new)
        return subprocess.run(
            ["python3", DIFF, a, b, "--track", "k8s", *extra],
            capture_output=True, text=True
        )


def test_secrets_skipped_by_default():
    # 沒有旗標時 Secret 完全不參與比對，值不同也照樣通過
    r = run_diff(OLD, NEW_DIFFERENT_VALUE)
    assert r.returncode == 0, r.stdout + r.stderr


def test_include_secrets_tolerates_type_opaque_only():
    # 加旗標後 Secret 參與比對；type: 的有無是舊新制的已知合法差異，須正規化掉
    r = run_diff(OLD, NEW, "--include-secrets")
    assert r.returncode == 0, r.stdout + r.stderr
    assert "Success" in r.stdout


def test_include_secrets_catches_value_drift():
    r = run_diff(OLD, NEW_DIFFERENT_VALUE, "--include-secrets")
    assert r.returncode == 1, r.stdout + r.stderr
    assert "PASS" in r.stdout, r.stdout


# 新制對長 base64 值輸出字面區塊純量（多行、含換行），舊制輸出單行——
# 同一個值的兩種序列化外觀，API server 端已實證正規化為同一結果（quay-conf 案例）。
NEW_WRAPPED = NEW.replace(
    "  PASS: aHVudGVyMg==\n",
    "  PASS: |\n    aHVud\n    GVyMg\n    ==\n",
)
NEW_WRAPPED_DIFFERENT_VALUE = NEW_DIFFERENT_VALUE.replace(
    "  PASS: d3JvbmdwYXNz\n",
    "  PASS: |\n    d3Jv\n    bmdw\n    YXNz\n",
)


def test_include_secrets_base64_line_wrap_normalization():
    # 換行包裝是無害的序列化差異，去空白正規化後應判定零漂移……
    r = run_diff(OLD, NEW_WRAPPED, "--include-secrets")
    assert r.returncode == 0, r.stdout + r.stderr
    assert "Success" in r.stdout
    # ……但正規化只能去空白，不能連值都洗掉：內容真的不同時，就算也包裝成多行，照樣要抓到漂移
    r2 = run_diff(OLD, NEW_WRAPPED_DIFFERENT_VALUE, "--include-secrets")
    assert r2.returncode == 1, r2.stdout + r2.stderr
    assert "PASS" in r2.stdout, r2.stdout


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
