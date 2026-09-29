#!/usr/bin/env python3
# test_litellm_config.py — litellm 設定檔入庫的契約：k8s 與 podman-4060ti 兩軌渲染出 ConfigMap litellm-config，
# 內含 base/config.yaml 原文；Secret litellm 只帶 secret.env 的鍵，不再夾帶 config.yaml。
# 服務目錄先複製到暫存目錄，機密一律用 .example 佔位值，不讀本機的真值檔。
# kubectl kustomize 是純本地操作，不連叢集。
import os
import shutil
import subprocess
import tempfile

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SVC_DIR = os.path.join(ROOT, "services", "litellm")
BASE_CONFIG = os.path.join(SVC_DIR, "base", "config.yaml")
TRACKS = ("k8s", "podman-4060ti")


def render(track):
    """把 services/litellm 複製到暫存目錄，機密換成 .example 佔位值後渲染指定軌，回傳 {(kind, name): doc}。"""
    with tempfile.TemporaryDirectory() as tmp:
        d = os.path.join(tmp, "litellm")
        shutil.copytree(SVC_DIR, d)
        secret = os.path.join(d, "secret")
        for name in os.listdir(secret):
            if not name.endswith(".example"):
                continue
            # 真值檔不管在不在都以佔位值覆蓋：測試結果不隨本機機密而變
            shutil.copy(os.path.join(secret, name), os.path.join(secret, name[: -len(".example")]))
        r = subprocess.run(["kubectl", "kustomize", os.path.join(d, track)],
                           capture_output=True, text=True)
        assert r.returncode == 0, track + r.stderr
        return {(doc["kind"], doc["metadata"]["name"]): doc
                for doc in yaml.safe_load_all(r.stdout) if doc}


def test_base_config_is_tracked_not_ignored():
    assert os.path.isfile(BASE_CONFIG), "services/litellm/base/config.yaml 不存在"
    # check-ignore 回 1 代表沒有被任何 .gitignore 規則排除
    r = subprocess.run(["git", "-C", ROOT, "check-ignore", "-q", BASE_CONFIG])
    assert r.returncode == 1, "base/config.yaml 被 .gitignore 排除"
    # ls-files --error-unmatch 直接斷言它確實受版控（在 git 索引裡），不只是沒被忽略
    r = subprocess.run(["git", "-C", ROOT, "ls-files", "--error-unmatch", BASE_CONFIG],
                       capture_output=True, text=True)
    assert r.returncode == 0, "base/config.yaml 不受版控：" + r.stderr


def test_configmap_carries_config_yaml():
    for track in TRACKS:
        docs = render(track)
        cm = docs.get(("ConfigMap", "litellm-config"))
        assert cm is not None, track + " 沒有 ConfigMap litellm-config：" + str(sorted(docs))
        assert list(cm.get("data", {})) == ["config.yaml"], track + str(list(cm.get("data", {})))
        want = open(BASE_CONFIG, encoding="utf-8").read()
        assert cm["data"]["config.yaml"] == want, track + " 的 config.yaml 與 base/config.yaml 不同"


def test_secret_has_no_config_yaml():
    for track in TRACKS:
        docs = render(track)
        s = docs.get(("Secret", "litellm"))
        assert s is not None, track + " 沒有 Secret litellm"
        keys = sorted(s.get("data", {}))
        assert "config.yaml" not in keys, track + " 的 Secret litellm 仍含 config.yaml：" + str(keys)
        assert "LITELLM_MASTER_KEY" in keys, track + str(keys)


def test_deployment_mounts_configmap():
    for track in TRACKS:
        docs = render(track)
        spec = docs[("Deployment", "litellm")]["spec"]["template"]["spec"]
        vol = {v["name"]: v for v in spec["volumes"]}["config"]
        assert vol.get("configMap", {}).get("name") == "litellm-config", track + str(vol)
        assert "secret" not in vol, track + str(vol)


# 沒有這段，Taskfile 的 `python3 tests/test_litellm_config.py` 會零輸出、rc=0。
# 例外刻意不 catch：第一支失敗即中止並以非零退出。
if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
