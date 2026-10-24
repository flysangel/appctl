#!/usr/bin/env python3
# test_gmm_sync.py — services/gmm/sync.py 自檢：mapping 解析、連線命名、同步計畫，以及對假 guacamole 的端對端行為。
# 端對端案例在 thread 裡起一個假的 guacamole REST API，記下每個請求；PATH 前面放假 nmap，記下 argv 並固定回 open。
# 腳本經 sync.sh 轉派執行，連轉派一起測。
import importlib.util
import json
import os
import shutil
import stat
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
GMM = os.path.join(ROOT, "services", "gmm")

ADMIN_PASS = "adminpw-fixture"
CONN_PASS = "connpw-fixture"
ROW = f"u1|pw1|g1|ssh|127.0.0.1|22|acct|{CONN_PASS}|dom|\n"

FAKE_NMAP = """\
#!/bin/bash
printf '%s\\n' "$*" >> "{log}"
printf '%s\\n' "PORT   STATE SERVICE" "${{@: -2:1}}/tcp open  x"
"""


def load():
    spec = importlib.util.spec_from_file_location("gmm_sync", os.path.join(GMM, "sync.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


# ---------- 純函式 ----------

def test_parse_mapping_rejects_bad_line():
    mod = load()
    try:
        mod.parse_mapping(ROW + "a|b|c|d|e|f|g|h|i\n")
    except mod.MappingError as e:
        assert e.line_no == 2, e.line_no
    else:
        raise AssertionError("9 欄的行沒有被擋下")


def test_parse_mapping_skips_comments_and_blank():
    rows = load().parse_mapping("# 註解\n\n" + ROW)
    assert len(rows) == 1 and rows[0].userAccount == "u1", rows


def test_conn_name():
    mod = load()
    row = mod.parse_mapping("u|p|g|rdp|1.2.3.4|3389|acct|pw|dom|\n")[0]
    assert mod.conn_name(row, True) == "【dom】acct@1.2.3.4"
    assert mod.conn_name(row, False) == "【dom】acct@1.2.3.4.X"
    row = mod.parse_mapping("u|p|g|rdp|1.2.3.4|3389|acct|pw|dom|a\n")[0]
    assert mod.conn_name(row, True) == "【dom】acct@1.2.3.4【a】"


def test_plan_keeps_admin_and_root():
    mod = load()
    rows = mod.parse_mapping(ROW)
    state = mod.State(users={"guacadmin", "u_old", "u1"}, groups={"ROOT": "0", "g_old": "5"}, connections={})
    plan = mod.build_plan(rows, {0: True}, state, "guacadmin")
    assert plan.delete_users == ["u_old"], plan
    assert plan.delete_groups == ["g_old"], plan
    assert plan.create_groups == ["g1"], plan


def test_plan_existing_connection_is_put():
    mod = load()
    rows = mod.parse_mapping(ROW)
    state = mod.State(users=set(), groups={}, connections={"【dom】acct@127.0.0.1": "7"})
    plan = mod.build_plan(rows, {0: True}, state, "guacadmin")
    assert plan.update_connections == ["【dom】acct@127.0.0.1"], plan
    assert plan.create_connections == [], plan


# ---------- 假 guacamole ----------

class Fake:
    def __init__(self, users=(), groups=None, connections=None, fail_user_post=False):
        self.users = {u: {"username": u} for u in users}
        self.groups = dict(groups or {})
        self.connections = dict(connections or {})
        self.profiles = {}
        self.requests = []
        self.fail_user_post = fail_user_post
        self.next_id = 100


def make_handler(fake):
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def _reply(self, code, body=None):
            data = json.dumps(body if body is not None else {}).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def _handle(self):
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length).decode() if length else ""
            fake.requests.append((self.command, self.path, dict(self.headers), raw))
            path = urlsplit(self.path).path
            base = "/guacamole/api/session/data/mysql/"
            if path == "/guacamole/api/tokens":
                return self._reply(200, {"authToken": "tok"})
            if not path.startswith(base):
                return self._reply(404)
            parts = path[len(base):].split("/")
            kind = parts[0]
            body = json.loads(raw) if raw and self.command != "PATCH" else None
            fake.next_id += 1
            nid = str(fake.next_id)
            if kind == "users":
                if self.command == "GET":
                    return self._reply(200, fake.users)
                if self.command == "POST":
                    if fake.fail_user_post:
                        return self._reply(500)
                    fake.users[body["username"]] = {"username": body["username"]}
                    return self._reply(200, body)
                if self.command == "DELETE":
                    fake.users.pop(parts[1], None)
                    return self._reply(204)
                return self._reply(204)
            store = {"connectionGroups": fake.groups, "connections": fake.connections,
                     "sharingProfiles": fake.profiles}.get(kind)
            if store is None:
                return self._reply(404)
            if self.command == "GET":
                return self._reply(200, {k: dict(v, identifier=k) for k, v in store.items()})
            if self.command == "POST":
                store[nid] = body
                return self._reply(200, dict(body, identifier=nid))
            if self.command == "PUT":
                store[parts[1]] = body
                return self._reply(204)
            if self.command == "DELETE":
                store.pop(parts[1], None)
                return self._reply(204)
            return self._reply(405)

        do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = _handle

    return H


def run_sync(*args, stdin="gmm\n", mapping=ROW, fake=None, random_csv=None):
    """在 tmp 目錄跑一份 sync.sh 與 sync.py 副本，回傳 (結果, 假伺服器, nmap argv 清單, random.csv 內容與權限)。"""
    fake = fake or Fake()
    server = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(fake))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory() as tmp:
            d = os.path.join(tmp, "gmm")
            os.makedirs(d)
            for name in ("sync.sh", "sync.py"):
                shutil.copy(os.path.join(GMM, name), os.path.join(d, name))
            port = server.server_address[1]
            open(os.path.join(d, "sync.conf"), "w").write(
                f"GUACAMOLE_SERVER=http://127.0.0.1:{port}/guacamole   # 註解\nGUACAMOLE_ADMIN=guacadmin\n"
            )
            open(os.path.join(d, "sync.secret.env"), "w").write(f"GUACAMOLE_PASS={ADMIN_PASS}\n")
            if mapping is not None:
                open(os.path.join(d, "mapping.env"), "w").write(mapping)
            if random_csv is not None:
                open(os.path.join(d, "random.csv"), "w").write(random_csv)
            stub = os.path.join(tmp, "stub")
            os.makedirs(stub)
            log = os.path.join(tmp, "nmap.log")
            open(os.path.join(stub, "nmap"), "w").write(FAKE_NMAP.format(log=log))
            os.chmod(os.path.join(stub, "nmap"), 0o755)
            env = dict(os.environ, PATH=stub + os.pathsep + os.environ["PATH"])
            r = subprocess.run(["bash", os.path.join(d, "sync.sh"), *args],
                               capture_output=True, text=True, env=env, input=stdin)
            nmap = open(log).read().splitlines() if os.path.exists(log) else []
            csv_path = os.path.join(d, "random.csv")
            csv = None
            if os.path.exists(csv_path):
                csv = (open(csv_path).read(), stat.S_IMODE(os.stat(csv_path).st_mode))
            return r, fake, nmap, csv
    finally:
        server.shutdown()


def mutations(fake):
    return [(m, p) for m, p, _, _ in fake.requests if m != "GET" and not p.endswith("/api/tokens")]


# ---------- 端對端 ----------

def test_token_in_header_not_url():
    r, fake, _, _ = run_sync()
    assert r.returncode == 0, r.stdout + r.stderr
    for method, path, headers, _ in fake.requests:
        assert "token=" not in path, path
        if not path.endswith("/api/tokens"):
            assert headers.get("Guacamole-Token") == "tok", (method, path)


def test_cancel_makes_no_mutation():
    r, fake, _, _ = run_sync(stdin="x\n")
    assert r.returncode == 1, r.stdout + r.stderr
    assert "已取消" in r.stdout, r.stdout
    assert mutations(fake) == [], mutations(fake)


def test_no_tty_makes_no_mutation():
    r, fake, _, _ = run_sync(stdin="")
    assert r.returncode == 1, r.stdout + r.stderr
    assert mutations(fake) == [], mutations(fake)


def test_bad_mapping_aborts_before_cleanup():
    fake = Fake(users=["stale"])
    r, fake, _, _ = run_sync(mapping=ROW + "bad|line\n", fake=fake)
    assert r.returncode == 1, r.stdout + r.stderr
    assert not [m for m in mutations(fake) if m[0] == "DELETE"], mutations(fake)


def test_empty_mapping_aborts():
    fake = Fake(users=["stale"])
    r, fake, _, _ = run_sync(mapping="# 只有註解\n\n", fake=fake)
    assert r.returncode == 1, r.stdout + r.stderr
    assert "Error:" in r.stderr, r.stderr
    assert mutations(fake) == [], mutations(fake)


def test_api_failure_nonzero():
    r, _, _, _ = run_sync(mapping="u1||g1|ssh|127.0.0.1|22|acct|pw|dom|\n", fake=Fake(fail_user_post=True))
    assert r.returncode == 1, r.stdout + r.stderr


def test_random_csv_appends():
    r, _, _, csv = run_sync(mapping="newuser||g1|ssh|127.0.0.1|22|acct|pw|dom|\n",
                            random_csv="User,Password\nold,oldpw\n")
    assert r.returncode == 0, r.stdout + r.stderr
    body, mode = csv
    lines = body.splitlines()
    assert lines[:2] == ["User,Password", "old,oldpw"], lines
    assert len(lines) == 3 and lines[2].startswith("newuser,"), lines
    assert mode == 0o600, oct(mode)


def test_secrets_not_in_nmap_argv():
    r, _, nmap, _ = run_sync()
    assert nmap, "nmap 沒有被呼叫"
    for line in nmap:
        assert ADMIN_PASS not in line and CONN_PASS not in line, line


def test_plan_printed_before_confirm():
    r, _, _, _ = run_sync(stdin="x\n", fake=Fake(users=["stale"]))
    out = r.stdout
    assert "stale" in out and out.index("stale") < out.index("已取消"), out


def test_test_mode_checks_mapping():
    r, fake, _, _ = run_sync("test", stdin="", mapping="a|b|c\n")
    assert r.returncode == 1, r.stdout + r.stderr
    assert mutations(fake) == [], mutations(fake)


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
