#!/usr/bin/env python3
# test_gmm_sync.py — services/gmm/sync.sh 自檢：sync 模式在動資料前要求輸入服務名確認。
# 離線靠假 curl 與假 nmap 達成：假 curl 只把參數寫進紀錄檔，取 token 時回假 token，其餘回 {}。
# 紀錄檔的行數就是 curl 被呼叫的次數，只有一行代表腳本只取了 token，沒有動任何資料。
import os
import shutil
import subprocess
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SYNC_SH = os.path.join(ROOT, "services", "gmm", "sync.sh")

FAKE_CURL = """\
#!/bin/bash
printf '%s\\n' "$*" >> "{log}"
case "$*" in
  */api/tokens*) printf '%s\\n' '{{"authToken":"t"}}' ;;
  *) printf '%s\\n' '{{}}' ;;
esac
"""


def run_sync(*args, stdin=""):
    """在 tmp 目錄跑一份 sync.sh 副本，回傳 (結果, curl 呼叫清單)。"""
    with tempfile.TemporaryDirectory() as tmp:
        d = os.path.join(tmp, "gmm")
        os.makedirs(d)
        shutil.copy(SYNC_SH, os.path.join(d, "sync.sh"))
        open(os.path.join(d, "sync.conf"), "w").write(
            "GUACAMOLE_SERVER=http://stub\nGUACAMOLE_ADMIN=a\n"
        )
        open(os.path.join(d, "sync.secret.env"), "w").write("GUACAMOLE_PASS=p\n")
        open(os.path.join(d, "mapping.env"), "w").write(
            "u1|pw|g1|ssh|127.0.0.1|22|acct|pass|dom|\n"
        )
        stub = os.path.join(tmp, "stub")
        os.makedirs(stub)
        log = os.path.join(tmp, "curl.log")
        open(os.path.join(stub, "curl"), "w").write(FAKE_CURL.format(log=log))
        open(os.path.join(stub, "nmap"), "w").write("#!/bin/bash\n")
        for name in ("curl", "nmap"):
            os.chmod(os.path.join(stub, name), 0o755)
        e = dict(os.environ)
        e["PATH"] = stub + os.pathsep + os.environ["PATH"]
        r = subprocess.run(
            ["bash", os.path.join(d, "sync.sh"), *args],
            capture_output=True, text=True, env=e, input=stdin,
        )
        calls = open(log).read().splitlines() if os.path.exists(log) else []
        return r, calls


def test_sync_without_tty_aborts():
    r, calls = run_sync(stdin="")
    assert r.returncode != 0, r.stdout + r.stderr
    assert "已取消" in r.stdout, r.stdout + r.stderr
    assert len(calls) == 1, calls


def test_sync_wrong_name_aborts():
    r, calls = run_sync(stdin="wrong\n")
    assert r.returncode != 0, r.stdout + r.stderr
    assert len(calls) == 1, calls


def test_test_mode_skips_confirm():
    r, calls = run_sync("test", stdin="")
    assert r.returncode == 0, r.stdout + r.stderr
    assert "Success: token ok" in r.stdout, r.stdout
    assert "確認" not in r.stdout, r.stdout


def test_sync_right_name_proceeds():
    r, calls = run_sync(stdin="gmm\n")
    assert len(calls) > 1, calls


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
