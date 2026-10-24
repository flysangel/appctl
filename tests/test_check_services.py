#!/usr/bin/env python3
# test_check_services.py — bin/check-services.py 自檢：同主機撞埠、互斥清單、namespace、cluster-admin 警告、渲染失敗。
# 每個案例在暫存目錄造假 repo，hostPort 照真 repo 的寫法放在 podman overlay 的 JSON6902 patch 裡。
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SCRIPT = os.path.join(ROOT, "bin", "check-services.py")

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
        ports:
        - containerPort: 8080
"""

PODMAN_PATCH = """\
patches:
  - target:
      kind: Deployment
      name: {name}
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/ports/0/hostPort
        value: {port}
"""

CRB = """\
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {name}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: {name}
  namespace: default
"""

LB = """\
apiVersion: v1
kind: Service
metadata:
  name: {name}
spec:
  type: LoadBalancer
  selector:
    app: {name}
  ports:
  - port: 22
"""


def write(path, body):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    open(path, "w").write(body)


def service(root, name, podman=None, k8s=False, ns="default", secret=False, cluster_admin=False, broken=False):
    """造 services/<name>/。podman 是 {軌名: hostPort}；k8s 為真時加 k8s 軌。"""
    d = os.path.join(root, "services", name)
    write(os.path.join(d, "base", "deployment.yaml"), DEPLOYMENT.format(name=name))
    write(os.path.join(d, "base", "kustomization.yaml"),
          "resources:\n  - deployment.yaml\n" if not broken else "resources:\n  - missing.yaml\n")
    extra = "  - ../secret\n" if secret else ""
    if secret:
        write(os.path.join(d, "secret", "kustomization.yaml"),
              "generatorOptions:\n  disableNameSuffixHash: true\nsecretGenerator:\n"
              f"  - name: {name}\n    envs:\n      - secret.env\n")
        write(os.path.join(d, "secret", "secret.env.example"), "PASS=changeme\n")
    for track, port in (podman or {}).items():
        write(os.path.join(d, track, "kustomization.yaml"),
              f"namespace: {ns}\nresources:\n  - ../base\n{extra}" + PODMAN_PATCH.format(name=name, port=port))
    if k8s:
        res = "  - ../base\n" + extra
        if cluster_admin:
            write(os.path.join(d, "k8s", "crb.yaml"), CRB.format(name=name))
            write(os.path.join(d, "k8s", "service.yaml"), LB.format(name=name))
            res += "  - crb.yaml\n  - service.yaml\n"
        write(os.path.join(d, "k8s", "kustomization.yaml"), f"namespace: {ns}\nresources:\n{res}")


def run(build, groups=None):
    with tempfile.TemporaryDirectory() as tmp:
        build(tmp)
        if groups is not None:
            write(os.path.join(tmp, "config", "port-groups.conf"), groups)
        r = subprocess.run([sys.executable, SCRIPT, tmp], capture_output=True, text=True)
        leaked = os.path.exists(os.path.join(tmp, "services", "a", "secret", "secret.env"))
        return r, leaked


def test_conflict_not_in_group_fails():
    r, _ = run(lambda t: (service(t, "a", podman={"podman": 8080}), service(t, "b", podman={"podman": 8080})), groups="")
    assert r.returncode == 1, r.stdout + r.stderr
    assert "a" in r.stderr and "b" in r.stderr and "8080" in r.stderr, r.stderr


def test_conflict_in_group_passes():
    r, _ = run(lambda t: (service(t, "a", podman={"podman": 8080}), service(t, "b", podman={"podman": 8080})),
               groups="# 互斥\na b\n")
    assert r.returncode == 0, r.stdout + r.stderr


def test_different_hosts_do_not_conflict():
    r, _ = run(lambda t: (service(t, "a", podman={"podman": 8080}), service(t, "b", podman={"podman-h1": 8080})))
    assert r.returncode == 0, r.stdout + r.stderr


def test_namespace_must_be_default():
    r, _ = run(lambda t: service(t, "a", k8s=True, ns="x"))
    assert r.returncode == 1, r.stdout + r.stderr
    assert "namespace" in r.stderr, r.stderr


def test_cluster_admin_lb_without_source_ranges_warns():
    r, _ = run(lambda t: service(t, "a", k8s=True, cluster_admin=True))
    assert r.returncode == 0, r.stdout + r.stderr
    assert "Info:" in r.stdout and "loadBalancerSourceRanges" in r.stdout, r.stdout


def test_render_failure_reports_and_continues():
    def build(t):
        service(t, "a", podman={"podman": 9000}, broken=True)
        service(t, "b", podman={"podman": 8080})
        service(t, "c", podman={"podman": 8080})
    r, _ = run(build, groups="")
    assert r.returncode == 1, r.stdout + r.stderr
    assert "a/podman" in r.stderr, r.stderr
    assert "8080" in r.stderr, r.stderr


def test_secret_service_renders_with_example_only():
    r, leaked = run(lambda t: service(t, "a", podman={"podman": 8080}, k8s=True, secret=True))
    assert r.returncode == 0, r.stdout + r.stderr
    assert not leaked, "假 repo 裡長出了 secret.env"


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
