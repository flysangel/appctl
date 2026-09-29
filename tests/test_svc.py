#!/usr/bin/env python3
# test_svc.py — bin/svc.sh 自檢：軌守衛、動詞守衛、刪除確認、機密檔 .example fallback 四類，
# 外加 kustomize 原生渲染的契約（短名解析、裸 $VAR 存活、Secret 固定名）。
# 離線靠 run() 把 KUBECONFIG 指向不存在的路徑達成：kubectl kustomize 是純本地操作不受影響，
# kubectl apply／delete 等需連叢集的操作會連線失敗，不會對真實叢集建立或動到任何資源。
import os
import shutil
import subprocess
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SVC_SH = os.path.join(ROOT, "bin", "svc.sh")
ASSERT_IMAGES = os.path.join(ROOT, "bin", "assert-images.sh")

# 新制 fixture：namespace 為字面值、image 為短名，值由 kustomization 的 images: 注入。
KUSTOMIZATION = """\
namespace: default
resources:
  - pod.yaml
images:
  - name: demo
    newName: example.io/demo
    newTag: "v1"
"""

POD = """\
apiVersion: v1
kind: Pod
metadata:
  name: demo
spec:
  containers:
  - name: c
    image: demo
    args: ["--user \\"$BARE_VAR\\""]
"""

# podman 軌名：通用的 podman 與依主機分出的 podman-4060ti。svc.sh 與 assert-images.sh 都要認得 podman 開頭的軌
PODMAN_TRACKS = ("podman", "podman-4060ti")

SECRET_KUSTOMIZATION = """\
generatorOptions:
  disableNameSuffixHash: true
secretGenerator:
  - name: demo
    envs:
      - secret.env
"""


def make_svc(tmp, svc="demo", tracks=("k8s",), kustomization=None, pod=None,
             secret_env=None, secret_env_example=None, config_yaml_example=None):
    """在 tmp 下造出 services/<svc>/ 與 bin/svc.sh 結構，回傳 (root, 服務目錄, 腳本路徑)。

    secret_env／secret_env_example：寫進 services/<svc>/secret/ 的真值檔與範本檔。
    config_yaml_example：quay 型檔案機密的範本，同樣落在 secret/ 下。
    給任一 secret 參數時會一併產生 secret/kustomization.yaml 並在各軌加 resources: [../secret]。
    """
    root = os.path.join(tmp, "repo")
    d = os.path.join(root, "services", svc)
    os.makedirs(d, exist_ok=True)
    has_secret = any(x is not None for x in (secret_env, secret_env_example, config_yaml_example))
    body = kustomization if kustomization is not None else KUSTOMIZATION
    if has_secret:
        body = body.replace("resources:\n", "resources:\n  - ../secret\n")
    for t in tracks:
        td = os.path.join(d, t)
        os.makedirs(td, exist_ok=True)
        open(os.path.join(td, "kustomization.yaml"), "w").write(body)
        open(os.path.join(td, "pod.yaml"), "w").write(pod if pod is not None else POD)
    if has_secret:
        sd = os.path.join(d, "secret")
        os.makedirs(sd, exist_ok=True)
        open(os.path.join(sd, "kustomization.yaml"), "w").write(SECRET_KUSTOMIZATION)
        if secret_env is not None:
            open(os.path.join(sd, "secret.env"), "w").write(secret_env)
        if secret_env_example is not None:
            open(os.path.join(sd, "secret.env.example"), "w").write(secret_env_example)
        if config_yaml_example is not None:
            open(os.path.join(sd, "config.yaml.example"), "w").write(config_yaml_example)
    bindir = os.path.join(root, "bin")
    os.makedirs(bindir, exist_ok=True)
    script = os.path.join(bindir, "svc.sh")
    shutil.copy(SVC_SH, script)
    shutil.copy(ASSERT_IMAGES, os.path.join(bindir, "assert-images.sh"))
    return root, d, script


def run(script, *args, stdin="", env_path=None):
    e = dict(os.environ)
    # 測試必須離線：apply／delete 分支會跑 kubectl apply／delete，指向真叢集會動到真資源。
    e["KUBECONFIG"] = "/nonexistent-test-kubeconfig"
    if env_path is not None:
        e["PATH"] = env_path
    return subprocess.run(
        ["bash", script, *args], capture_output=True, text=True, env=e, input=stdin
    )


def stub_sudo(tmp):
    """在 tmp/stub 放一支假 sudo，回傳 (PATH 值, 紀錄檔路徑)。

    podman 軌走 `sudo podman kube play`，真跑會在本機起容器。假 sudo 只把參數與 stdin 寫進紀錄檔。
    測試讀紀錄檔，就知道腳本交出的子命令與渲染產物。
    """
    stub = os.path.join(tmp, "stub")
    os.makedirs(stub)
    log = os.path.join(tmp, "sudo.log")
    p = os.path.join(stub, "sudo")
    open(p, "w").write(f'#!/bin/bash\nprintf "%s\\n" "$*" >> "{log}"\ncat >> "{log}"\n')
    os.chmod(p, 0o755)
    return stub + os.pathsep + os.environ["PATH"], log


def kustomize(path):
    return subprocess.run(
        ["kubectl", "kustomize", path], capture_output=True, text=True
    )


# ---------- 守衛類 ----------

def test_missing_track_dir_fails():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, tracks=("k8s",))
        r = run(script, "demo", "apply", "podman")
        assert r.returncode == 1, r.stdout + r.stderr
        assert "Error:" in r.stderr


def test_typo_service_name_message_covers_both_causes():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp)
        r = run(script, "demoo", "apply", "k8s")
        assert r.returncode == 1
        # 訊息必須對「服務名打錯」與「無此軌」兩種成因都成立
        assert "服務名錯誤" in r.stderr, r.stderr


def test_bad_verb_and_bad_track_fail():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, tracks=("k8s", "podman"))
        assert run(script, "demo", "restart", "k8s").returncode == 1
        r = run(script, "demo", "apply", "docker")
        assert r.returncode == 1


def test_podman_prefixed_track_apply_goes_to_kube_play():
    # 軌名是 podman，或依主機分出的 podman-4060ti 這類 podman 開頭的軌，都要交給 kube play
    for track in PODMAN_TRACKS:
        with tempfile.TemporaryDirectory() as tmp:
            root, d, script = make_svc(tmp, tracks=(track,))
            path, log = stub_sudo(tmp)
            r = run(script, "demo", "apply", track, env_path=path)
            assert r.returncode == 0, track + r.stdout + r.stderr
            got = open(log).read()
            assert got.startswith("podman kube play --replace -\n"), got
            assert "image: example.io/demo:v1" in got, got


def test_podman_prefixed_track_delete_goes_to_kube_down():
    for track in PODMAN_TRACKS:
        with tempfile.TemporaryDirectory() as tmp:
            root, d, script = make_svc(tmp, tracks=(track,))
            path, log = stub_sudo(tmp)
            r = run(script, "demo", "delete", track, stdin="demo\n", env_path=path)
            assert r.returncode == 0, track + r.stdout + r.stderr
            got = open(log).read()
            assert got.startswith("podman kube down --force -\n"), got
            assert "image: example.io/demo:v1" in got, got


def test_delete_without_tty_aborts():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp)
        r = run(script, "demo", "delete", "k8s", stdin="")
        assert r.returncode == 1
        assert "已取消" in r.stdout, r.stdout


def test_delete_wrong_name_aborts():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp)
        r = run(script, "demo", "delete", "k8s", stdin="wrong\n")
        assert r.returncode == 1
        assert "已取消" in r.stdout, r.stdout


# ---------- 機密檔 fallback（K10） ----------

def test_secret_env_created_from_example_when_missing():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, secret_env_example="PASS=changeme\n")
        real = os.path.join(d, "secret", "secret.env")
        assert not os.path.exists(real)
        r = run(script, "demo", "apply", "k8s")
        assert os.path.exists(real), r.stdout + r.stderr
        assert open(real).read() == "PASS=changeme\n"
        assert "Info:" in r.stdout and "secret.env" in r.stdout


def test_existing_secret_env_not_overwritten():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, secret_env="PASS=real\n",
                                   secret_env_example="PASS=changeme\n")
        run(script, "demo", "apply", "k8s")
        assert open(os.path.join(d, "secret", "secret.env")).read() == "PASS=real\n"


def test_config_yaml_created_from_example_when_missing():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, secret_env="PASS=real\n",
                                   config_yaml_example="DB_URI: changeme\n")
        real = os.path.join(d, "secret", "config.yaml")
        run(script, "demo", "apply", "k8s")
        assert os.path.exists(real)
        assert open(real).read() == "DB_URI: changeme\n"


def test_apply_warns_when_fallback_used():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, secret_env_example="PASS=changeme\n")
        r = run(script, "demo", "apply", "k8s")
        assert "不是真密碼" in r.stdout, r.stdout


def test_delete_does_not_warn_on_fallback():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, secret_env_example="PASS=changeme\n")
        r = run(script, "demo", "delete", "k8s", stdin="wrong\n")
        assert "不是真密碼" not in r.stdout, r.stdout


def test_no_fallback_when_no_example():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp)
        run(script, "demo", "apply", "k8s")
        assert not os.path.exists(os.path.join(d, "secret"))


# ---------- 渲染契約 ----------

def test_short_name_resolves_to_full_reference():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp)
        r = kustomize(os.path.join(d, "k8s"))
        assert r.returncode == 0, r.stderr
        assert "image: example.io/demo:v1" in r.stdout, r.stdout


def test_bare_var_survives_render():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp)
        r = kustomize(os.path.join(d, "k8s"))
        assert "$BARE_VAR" in r.stdout, "裸 $BARE_VAR 被動到了（機密外洩路徑）"


def test_images_name_mismatch_is_silent_no_op():
    # 記錄 K18 新守衛存在的理由：name 對不上時 kustomize rc=0 且產物留短名。
    bad = KUSTOMIZATION.replace("name: demo", "name: typo")
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, kustomization=bad)
        r = kustomize(os.path.join(d, "k8s"))
        assert r.returncode == 0, "kustomize 竟然報錯了——本測試的前提要重新檢視"
        assert "image: demo" in r.stdout, r.stdout


def test_secret_generator_name_has_no_hash_suffix():
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, secret_env="PASS=real\n")
        r = kustomize(os.path.join(d, "k8s"))
        assert r.returncode == 0, r.stderr
        assert "name: demo\n" in r.stdout, r.stdout
        assert "type: Opaque" in r.stdout


def test_missing_secret_env_breaks_render():
    # 記錄 K10 存在的理由：沒有 fallback 時 kustomize 直接 rc≠0。
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, secret_env_example="PASS=changeme\n")
        r = kustomize(os.path.join(d, "k8s"))
        assert r.returncode != 0, "缺 secret.env 時 kustomize 竟然成功了"


# ---------- assert-images.sh 新守衛（K18） ----------

def _run_assert(root, svc):
    return subprocess.run(
        ["bash", os.path.join(root, "bin", "assert-images.sh"), svc],
        capture_output=True, text=True
    )


def test_assert_images_rejects_unresolved_short_name():
    bad = KUSTOMIZATION.replace("name: demo", "name: typo")
    with tempfile.TemporaryDirectory() as tmp:
        root, d, script = make_svc(tmp, kustomization=bad)
        r = _run_assert(root, "demo")
        assert r.returncode == 1, r.stdout + r.stderr
        assert "未解析為完整參照" in r.stderr, r.stderr


def test_assert_images_renders_podman_prefixed_tracks():
    # 服務只有 podman 或 podman-4060ti 軌時也要渲染到。沒渲染到會落到「零個 image」。
    # 這裡用打錯的短名，讓渲染產物帶出未解析的 image。看錯誤訊息就分得出有沒有渲染到。
    bad = KUSTOMIZATION.replace("name: demo", "name: typo")
    for track in PODMAN_TRACKS:
        with tempfile.TemporaryDirectory() as tmp:
            root, d, script = make_svc(tmp, tracks=(track,), kustomization=bad)
            r = _run_assert(root, "demo")
            assert r.returncode == 1, track + r.stdout + r.stderr
            assert "未解析為完整參照" in r.stderr, track + r.stderr


def test_assert_images_rejects_zero_images():
    empty = "namespace: default\nresources: []\n"
    with tempfile.TemporaryDirectory() as tmp:
        root = os.path.join(tmp, "repo")
        td = os.path.join(root, "services", "demo", "k8s")
        os.makedirs(td)
        open(os.path.join(td, "kustomization.yaml"), "w").write(empty)
        bindir = os.path.join(root, "bin")
        os.makedirs(bindir)
        shutil.copy(ASSERT_IMAGES, os.path.join(bindir, "assert-images.sh"))
        r = _run_assert(root, "demo")
        assert r.returncode == 1, r.stdout + r.stderr
        assert "零個 image" in r.stderr, r.stderr


# 沒有這段，Taskfile 的 `python3 tests/test_svc.py` 會零輸出、rc=0——一支測試都沒跑卻報成功。
# 例外刻意不 catch：讓它自然往上拋，第一支失敗即中止並以非零退出，rc 才是可信的判據。
if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
