#!/usr/bin/env python3
# manifest-diff.py — 遷移零漂移比對器：舊制 render 基線 vs 新制 kustomize 產物的語意比對。
# 正規化：預設剔除 Secret doc（--include-secrets 可納入比對，吸收舊新制 type: 欄位的差異，
# 並對 data 各值去空白以吸收長 base64 值的 YAML 換行包裝差異）、
# podman 軌把基線 hostPath 卷改寫為 PVC 引用（claimName=<workload名>-<卷名>，卷名取 hostPath 尾段），
# 對應新制「全面 named volume」拍板。差異以點路徑列出；--allow 白名單放行預期差異。
# 用法：manifest-diff.py <baseline.yaml> <new.yaml> --track <podman|k8s> [--allow 路徑]...
import argparse
import sys

import yaml


def _norm_podman(doc):
    for vol in (doc.get("spec", {}).get("volumes") or []):
        hp = vol.pop("hostPath", None)
        if hp:
            volname = hp["path"].rstrip("/").split("/")[-1]
            vol["persistentVolumeClaim"] = {"claimName": f"{doc['metadata']['name']}-{volname}"}
    return doc


def _load(path, track, normalize=False, include_secrets=False):
    docs = [d for d in yaml.safe_load_all(open(path)) if d]
    if include_secrets:
        # 舊制 kubectl create secret --dry-run=client -o yaml 不輸出 type:，
        # 新制 secretGenerator 輸出 type: Opaque——兩者語意相同（k8s 預設即 Opaque）。
        for d in docs:
            if d.get("kind") == "Secret":
                d.setdefault("type", "Opaque")
                # kustomize 對超過寬度門檻的長 base64 值輸出字面區塊純量（`config.yaml: |`），
                # 換行變成字串內容的一部分；kubectl create secret --dry-run=client -o yaml
                # 則輸出單行。兩者對 API server 而言是同一個值——已用
                # `kubectl apply --dry-run=server -o json` 對 quay-conf 實證：server 端回傳的
                # data 一律正規化為無空白單行，解碼後明文 sha256 相同。故比對前把 data 每個值
                # 的空白字元（含換行）全部去掉，否則這類無害的序列化差異會被誤判成漂移。
                for k, v in (d.get("data") or {}).items():
                    if isinstance(v, str):
                        d["data"][k] = "".join(v.split())
    else:
        docs = [d for d in docs if d.get("kind") != "Secret"]
    if track == "podman" and normalize:
        docs = [_norm_podman(d) for d in docs]
    return sorted(docs, key=lambda d: (d.get("kind", ""), d.get("metadata", {}).get("name", "")))


def _walk(a, b, path, out):
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(set(a) | set(b)):
            _walk(a.get(k), b.get(k), f"{path}/{k}", out)
    elif isinstance(a, list) and isinstance(b, list):
        if len(a) != len(b):
            out.append(f"{path}（清單長度 {len(a)}≠{len(b)}）")
            return
        for i, (x, y) in enumerate(zip(a, b)):
            _walk(x, y, f"{path}/{i}", out)
    elif a != b:
        out.append(path)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("baseline")
    ap.add_argument("new")
    ap.add_argument("--track", required=True, choices=["podman", "k8s"])
    ap.add_argument("--allow", action="append", default=[])
    ap.add_argument("--include-secrets", action="store_true",
                    help="把 kind: Secret 納入比對（遷移機密產生機制時必用）")
    ns = ap.parse_args()
    base = _load(ns.baseline, ns.track, normalize=True, include_secrets=ns.include_secrets)
    new = _load(ns.new, ns.track, normalize=False, include_secrets=ns.include_secrets)
    diffs = []
    for a, b in zip(base, new):
        _walk(a, b, f"{a.get('kind')}/{a.get('metadata', {}).get('name')}", diffs)
    if len(base) != len(new):
        diffs.append(f"doc 數 {len(base)}≠{len(new)}")
    real = [d for d in diffs if d not in ns.allow]
    if real:
        print("Error: 漂移：", file=sys.stderr)
        for d in real:
            print(d)
        return 1
    print("Success: 語意零漂移")
    return 0


if __name__ == "__main__":
    sys.exit(main())
