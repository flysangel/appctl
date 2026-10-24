#!/usr/bin/env python3
# test_check_manifests.py — bin/check-manifests.py 自檢：image 完整參照的判準，以及 CLI 掃描範圍。
import importlib.util
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SCRIPT = os.path.join(ROOT, "bin", "check-manifests.py")

DIGEST = "sha256:" + "a" * 64


def load():
    spec = importlib.util.spec_from_file_location("check_manifests", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def run_cli(files):
    """在暫存的假 repo 放 files（相對路徑 → 內容）並跑 check-manifests.py，回傳 CompletedProcess。"""
    with tempfile.TemporaryDirectory() as tmp:
        for rel, body in files.items():
            path = os.path.join(tmp, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            open(path, "w").write(body)
        return subprocess.run([sys.executable, SCRIPT, tmp], capture_output=True, text=True)


def pod(image, key="containers"):
    return f"apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\nspec:\n  {key}:\n  - name: c\n    image: {image}\n"


def test_image_problem():
    problem = load().image_problem
    assert problem("quay.io/flysangel/grafana/grafana:13.1.1") is None
    assert problem("grafana-grafana") is not None
    assert problem("quay.io/flysangel/x/y") is not None
    assert problem("quay.io/flysangel/x/y:latest") is not None
    assert problem("localhost:5000/foo") is not None
    assert problem("localhost:5000/foo:1") is None
    assert problem(f"quay.io/flysangel/minio/minio@{DIGEST}") is None
    assert problem(f"quay.io/flysangel/minio/minio:v1@{DIGEST}") is None
    assert problem("quay.io/x/y@sha256:abc") is not None


def test_cli_rejects_images_key():
    r = run_cli({
        "services/a/base/pod.yaml": pod("quay.io/x/y:1"),
        "services/a/base/kustomization.yaml": "resources:\n  - pod.yaml\nimages:\n  - name: y\n    newTag: \"1\"\n",
    })
    assert r.returncode == 1, r.stdout + r.stderr
    assert "images:" in r.stderr, r.stderr


def test_cli_skips_secret_dir():
    r = run_cli({
        "services/a/base/pod.yaml": pod("quay.io/x/y:1"),
        "services/a/secret/config.yaml": "image: bad\n",
    })
    assert r.returncode == 0, r.stdout + r.stderr


def test_cli_finds_nested_image():
    r = run_cli({"services/a/base/pod.yaml": pod("shortname", key="initContainers")})
    assert r.returncode == 1, r.stdout + r.stderr
    assert "services/a/base/pod.yaml" in r.stderr and "shortname" in r.stderr, r.stderr


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
