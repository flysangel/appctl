#!/usr/bin/env python3
# check-services.py — 服務層離線驗證：overlay 的 namespace、同主機 hostPort 撞埠、cluster-admin 服務的 LB 來源網段。
# 用法：check-services.py [repo 路徑]；不給時用本檔所在 repo。pre-push 傳入 git archive 展開的樹。
# hostPort 寫在 podman overlay 的 JSON6902 patch 裡，要渲染才看得到；展開的樹沒有機密真值檔，
# 所以把 services/ 複製到暫存目錄，只帶 .example 並改成真名，再跑 kubectl kustomize。真值檔不複製，Secret 不讀。
# 主機鍵：podman 軌是預設主機，podman-<host> 軌是 <host>。互斥清單在 config/port-groups.conf。
import os
import shutil
import subprocess
import sys
import tempfile

import yaml

WORKLOADS = ("Deployment", "DaemonSet", "StatefulSet")


def load_groups(root):
    path = os.path.join(root, "config", "port-groups.conf")
    if not os.path.isfile(path):
        return []
    groups = []
    for line in open(path):
        line = line.strip()
        if line and not line.startswith("#"):
            groups.append(set(line.split()))
    return groups


def copy_services(src, dst):
    """複製 services/，secret/ 底下只帶 kustomization.yaml 與 .example，再把 .example 複製成真名。"""
    def ignore(dirpath, names):
        if "secret" not in os.path.relpath(dirpath, src).split(os.sep):
            return []
        return [n for n in names
                if os.path.isfile(os.path.join(dirpath, n))
                and not n.endswith(".example") and n != "kustomization.yaml"]
    shutil.copytree(src, dst, ignore=ignore)
    for dirpath, _, names in os.walk(dst):
        for n in names:
            if n.endswith(".example"):
                shutil.copy(os.path.join(dirpath, n), os.path.join(dirpath, n[:-len(".example")]))


def tracks(svc_dir):
    return sorted(t for t in os.listdir(svc_dir)
                  if os.path.isdir(os.path.join(svc_dir, t)) and (t == "k8s" or t.startswith("podman")))


def host_of(track):
    return "default" if track == "podman" else track[len("podman-"):]


def pod_specs(doc):
    if doc.get("kind") == "Pod":
        yield doc.get("spec") or {}
    elif doc.get("kind") in WORKLOADS:
        yield ((doc.get("spec") or {}).get("template") or {}).get("spec") or {}


def host_ports(docs):
    for doc in docs:
        for spec in pod_specs(doc):
            for c in (spec.get("containers") or []) + (spec.get("initContainers") or []):
                for p in c.get("ports") or []:
                    if "hostPort" in p:
                        yield p["hostPort"], p.get("protocol", "TCP")


def check_namespace(svc, track, overlay, errors):
    doc = yaml.safe_load(open(os.path.join(overlay, "kustomization.yaml"))) or {}
    if doc.get("namespace") != "default":
        errors.append(f"{svc}/{track}：kustomization.yaml 的 namespace 是 {doc.get('namespace')!r}，要寫 default")


def warn_cluster_admin(svc, docs):
    admin = any(d.get("kind") == "ClusterRoleBinding" and (d.get("roleRef") or {}).get("name") == "cluster-admin"
                for d in docs)
    if not admin:
        return
    for d in docs:
        spec = d.get("spec") or {}
        if d.get("kind") == "Service" and spec.get("type") == "LoadBalancer" and not spec.get("loadBalancerSourceRanges"):
            print(f"Info: {svc} 綁 cluster-admin，LoadBalancer Service {d['metadata']['name']} "
                  "沒有 loadBalancerSourceRanges，對外開放範圍只靠網段隔離")


def main(argv):
    root = os.path.abspath(argv[1]) if len(argv) > 1 else os.path.dirname(
        os.path.dirname(os.path.abspath(__file__)))
    groups = load_groups(root)
    errors = []
    used = {}
    with tempfile.TemporaryDirectory() as tmp:
        services = os.path.join(tmp, "services")
        copy_services(os.path.join(root, "services"), services)
        for svc in sorted(os.listdir(services)):
            svc_dir = os.path.join(services, svc)
            if not os.path.isdir(svc_dir):
                continue
            for track in tracks(svc_dir):
                overlay = os.path.join(svc_dir, track)
                check_namespace(svc, track, overlay, errors)
                r = subprocess.run(["kubectl", "kustomize", overlay], capture_output=True, text=True)
                if r.returncode != 0:
                    first = (r.stderr.strip().splitlines() or ["（沒有訊息）"])[0]
                    errors.append(f"{svc}/{track}：kubectl kustomize 失敗：{first}")
                    continue
                docs = [d for d in yaml.safe_load_all(r.stdout) if d]
                if track == "k8s":
                    warn_cluster_admin(svc, docs)
                    continue
                for port, proto in host_ports(docs):
                    used.setdefault((host_of(track), port, proto), set()).add(svc)
    for (host, port, proto), svcs in sorted(used.items(), key=lambda kv: (kv[0][0], kv[0][1], kv[0][2])):
        if len(svcs) > 1 and not any(svcs <= g for g in groups):
            errors.append(f"主機 {host} 的 {port}/{proto} 有 {'、'.join(sorted(svcs))} 同時使用，"
                          "而且不在 config/port-groups.conf 的互斥清單裡")
    if errors:
        print("Error: 服務層檢查未通過：", file=sys.stderr)
        for e in errors:
            print(f"  {e}", file=sys.stderr)
        return 1
    print("Success: 服務層檢查通過")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
