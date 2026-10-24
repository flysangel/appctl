#!/usr/bin/env python3
# pvc-shells.py — 從 stdin 讀 podman 軌的 manifest，原樣輸出，再替 Deployment 與 DaemonSet 的每個 claimName 補一份 PVC 殼文件。
# 用法：kubectl kustomize <軌> | pvc-shells.py | sudo podman kube down --force -
#       pvc-shells.py --names：只輸出全部 claimName（含 kind: Pod 的），一行一個，排序，給刪除後確認卷都不在用。
# 理由：podman 4.9.3 的 kube down --force 只收 kind: Pod 與 PVC 文件的卷名，Deployment 與 DaemonSet 的卷會留著
# （rootless 實測：Deployment 的卷 down 之後還在，補上 PVC 殼才刪掉）。kind: Pod 的卷本來就收得到，不補，避免重複。
import sys

import yaml

TEMPLATED = ("Deployment", "DaemonSet")


def claims(doc):
    if doc.get("kind") == "Pod":
        spec = doc.get("spec") or {}
    elif doc.get("kind") in TEMPLATED:
        spec = ((doc.get("spec") or {}).get("template") or {}).get("spec") or {}
    else:
        return []
    return [v["persistentVolumeClaim"]["claimName"] for v in spec.get("volumes") or []
            if (v.get("persistentVolumeClaim") or {}).get("claimName")]


def main(argv):
    text = sys.stdin.read()
    docs = [d for d in yaml.safe_load_all(text) if d]
    if "--names" in argv[1:]:
        for name in sorted({c for d in docs for c in claims(d)}):
            print(name)
        return 0
    shells = sorted({c for d in docs if d.get("kind") in TEMPLATED for c in claims(d)})
    sys.stdout.write(text if text.endswith("\n") else text + "\n")
    for name in shells:
        sys.stdout.write(f"---\napiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: {name}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
