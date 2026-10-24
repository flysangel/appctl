#!/usr/bin/env python3
# test_pvc_shells.py — bin/pvc-shells.py 自檢：替 Deployment 與 DaemonSet 的 claimName 補 PVC 殼，讓 kube down --force 刪得到卷。
# podman 4.9.3 實測：kube down --force 只收 kind: Pod 與 PVC 文件的卷名，Deployment 的卷會留著。
import os
import subprocess
import sys

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SCRIPT = os.path.join(ROOT, "bin", "pvc-shells.py")


def workload(kind, name, claims):
    vols = [{"name": f"v{i}", "persistentVolumeClaim": {"claimName": c}} for i, c in enumerate(claims)]
    spec = {"containers": [{"name": "c", "image": "example.io/x:1"}], "volumes": vols}
    if kind == "Pod":
        return {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name}, "spec": spec}
    return {"apiVersion": "apps/v1", "kind": kind, "metadata": {"name": name},
            "spec": {"template": {"spec": spec}}}


def run(docs, *args):
    text = yaml.safe_dump_all(docs)
    r = subprocess.run([sys.executable, SCRIPT, *args], input=text, capture_output=True, text=True)
    assert r.returncode == 0, r.stderr
    return r.stdout


def pvcs(out):
    return [d["metadata"]["name"] for d in yaml.safe_load_all(out) if d and d.get("kind") == "PersistentVolumeClaim"]


def test_deployment_claims_get_shells():
    out = run([workload("Deployment", "a", ["a-data", "a-db"])])
    assert sorted(pvcs(out)) == ["a-data", "a-db"], out
    assert "kind: Deployment" in out, "原本的文件要原樣輸出"


def test_pod_claims_not_duplicated():
    out = run([workload("Pod", "p", ["p-data"])])
    assert pvcs(out) == [], out


def test_duplicate_names_once():
    out = run([workload("Deployment", "a", ["shared"]), workload("DaemonSet", "b", ["shared"])])
    assert pvcs(out) == ["shared"], out


def test_names_mode():
    out = run([workload("Deployment", "a", ["z-data"]), workload("Pod", "p", ["a-data"])], "--names")
    assert out.splitlines() == ["a-data", "z-data"], out


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
