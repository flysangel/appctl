#!/usr/bin/env python3
# sync.py — 透過 guacamole 的 REST API，讓 user、group、connection 與 mapping.env 一致（冪等，含清理與 nmap 存活驗證）。
# 用法：python3 sync.py [sync|test]；由同目錄的 sync.sh 轉派。設定讀同目錄的 sync.conf 與 sync.secret.env，對應表讀 mapping.env。
# 流程：取 token → 解析 mapping（任一行壞掉或 0 筆就中止）→ 逐行 nmap → 讀現況 → 算計畫並印出 → 輸入服務名確認 → 執行。
# 動資料前一定先確認；沒有 tty 時 input() 讀到 EOF，視為取消。每次 API 呼叫都檢查狀態碼，有失敗就以 1 結束。
# token 放 Guacamole-Token 標頭，密碼只在 HTTP body 與記憶體裡，不進任何程式的 argv。
# 語意照 README〈營運同步〉：只保留 admin 帳號與 ROOT 群組；已存在的 user 不改密碼；sharing profile 已存在就不動；
# permission 只加不減；connection 已存在就整份 PUT。
import json
import os
import secrets
import string
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field

HERE = os.path.dirname(os.path.abspath(__file__))
FIELDS = ("userAccount", "userPassword", "connGroup", "connProtocol", "connIP",
          "connPort", "connAccount", "connPassword", "connDomain", "connAlias")


class MappingError(Exception):
    def __init__(self, line_no, reason):
        super().__init__(f"mapping.env 第 {line_no} 行{reason}")
        self.line_no = line_no


class ApiError(Exception):
    pass


@dataclass
class Row:
    userAccount: str
    userPassword: str
    connGroup: str
    connProtocol: str
    connIP: str
    connPort: str
    connAccount: str
    connPassword: str
    connDomain: str
    connAlias: str


@dataclass
class State:
    users: set
    groups: dict
    connections: dict


@dataclass
class Plan:
    create_users: list = field(default_factory=list)
    create_groups: list = field(default_factory=list)
    create_connections: list = field(default_factory=list)
    update_connections: list = field(default_factory=list)
    delete_users: list = field(default_factory=list)
    delete_groups: list = field(default_factory=list)
    delete_connections: list = field(default_factory=list)


def read_conf(path):
    """讀 bash 可 source 的 KEY=VALUE 檔：值可加引號；不加引號時到第一個空白為止，之後的 # 是註解。"""
    values = {}
    for line in open(path):
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, rest = line.split("=", 1)
        if rest[:1] in ("'", '"') and rest.find(rest[0], 1) > 0:
            value = rest[1:rest.find(rest[0], 1)]
        else:
            value = rest.split()[0] if rest.split() else ""
        values[key.strip()] = value
    return values


def parse_mapping(text):
    rows = []
    for no, line in enumerate(text.splitlines(), 1):
        if not line.strip() or line.startswith("#"):
            continue
        parts = line.split("|")
        if len(parts) != len(FIELDS):
            raise MappingError(no, f"有 {len(parts)} 欄，要恰好 {len(FIELDS)} 欄")
        row = Row(*parts)
        # 沒有 user 的行整行略過，照舊版行為
        if row.userAccount:
            rows.append(row)
    return rows


def conn_name(row, alive):
    name = f"【{row.connDomain}】{row.connAccount}@{row.connIP}"
    if row.connAlias:
        name += f"【{row.connAlias}】"
    return name if alive else name + ".X"


def build_plan(rows, alive, state, admin):
    plan = Plan()
    names = [conn_name(r, alive[i]) for i, r in enumerate(rows)]
    users = {r.userAccount for r in rows}
    groups = {r.connGroup for r in rows}
    for u in sorted(users - state.users):
        plan.create_users.append(u)
    for g in sorted(groups - set(state.groups)):
        plan.create_groups.append(g)
    for n in dict.fromkeys(names):
        (plan.update_connections if n in state.connections else plan.create_connections).append(n)
    plan.delete_users = sorted(u for u in state.users - users if u != admin)
    plan.delete_groups = sorted(g for g in set(state.groups) - groups if g != "ROOT")
    plan.delete_connections = sorted(set(state.connections) - set(names))
    return plan


def probe(row):
    """nmap 只拿到 IP 與埠，argv 裡沒有任何密碼。"""
    r = subprocess.run(["nmap", "-Pn", "-T4", "--host-timeout", "10s", "--max-retries", "1",
                        "-p", row.connPort, row.connIP], capture_output=True, text=True)
    for line in r.stdout.splitlines():
        cols = line.split()
        if len(cols) >= 2 and cols[0].startswith(f"{row.connPort}/") and cols[1] == "open":
            return True
    return False


class Client:
    def __init__(self, server, data_source):
        self.server = server.rstrip("/")
        self.api = f"{self.server}/api/session/data/{data_source}"
        self.token = None

    def _send(self, req):
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                body = resp.read().decode()
        except urllib.error.HTTPError as e:
            raise ApiError(f"{req.get_method()} {urllib.parse.urlsplit(req.full_url).path} 回 {e.code}") from None
        except urllib.error.URLError as e:
            raise ApiError(f"{req.get_method()} {urllib.parse.urlsplit(req.full_url).path} 連不上：{e.reason}") from None
        return json.loads(body) if body.strip() else None

    def login(self, user, password):
        data = urllib.parse.urlencode({"username": user, "password": password}).encode()  # gitleaks:allow
        req = urllib.request.Request(f"{self.server}/api/tokens", data=data, method="POST",
                                     headers={"Content-Type": "application/x-www-form-urlencoded"})
        token = (self._send(req) or {}).get("authToken")
        if not token:
            raise ApiError("取不到 authToken")
        self.token = token

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Guacamole-Token": self.token}
        if data is not None:
            headers["Content-Type"] = "application/json"
        return self._send(urllib.request.Request(f"{self.api}/{path}", data=data, method=method, headers=headers))


def by_name(listing):
    return {v.get("name"): k for k, v in (listing or {}).items()}


def read_state(client):
    return State(users=set(client.call("GET", "users") or {}),
                 groups=by_name(client.call("GET", "connectionGroups")),
                 connections=by_name(client.call("GET", "connections")))


def print_plan(plan):
    print("Info: 同步計畫")
    for label, items in (("建立 user", plan.create_users), ("建立 group", plan.create_groups),
                         ("建立 connection", plan.create_connections), ("覆寫 connection", plan.update_connections),
                         ("刪除 user", plan.delete_users), ("刪除 group", plan.delete_groups),
                         ("刪除 connection", plan.delete_connections)):
        print(f"  {label}：{'、'.join(items) if items else '無'}")


def confirm():
    try:
        ans = input("刪除 mapping.env 以外的 user、group、connection，不可逆。輸入服務名確認：")
    except EOFError:
        ans = ""
    return ans.strip() == "gmm"


def params(row):
    base = {"hostname": row.connIP, "port": row.connPort, "username": row.connAccount, "password": row.connPassword}  # gitleaks:allow
    # 協定參數寫死：rdp 帶 security nla 與 ignore-cert；ssh 帶配色與字級；其他值只送四欄
    if row.connProtocol == "rdp":
        base.update({"domain": row.connDomain, "security": "nla", "ignore-cert": "true"})
    elif row.connProtocol == "ssh":
        base.update({"color-scheme": "black-white", "font-size": "14"})
    return base


def record_password(user, password):
    path = os.path.join(HERE, "random.csv")
    new = not os.path.exists(path)
    with open(path, "a") as f:
        if new:
            f.write("User,Password\n")
        f.write(f"{user},{password}\n")
    os.chmod(path, 0o600)


def sync_row(client, row, name, state, pass_length):
    if row.userAccount not in state.users:
        password = row.userPassword
        if not password:
            alphabet = string.ascii_lowercase + string.digits
            password = "".join(secrets.choice(alphabet) for _ in range(pass_length))
            record_password(row.userAccount, password)
        client.call("POST", "users", {"username": row.userAccount, "password": password, "attributes": {}})  # gitleaks:allow
        state.users.add(row.userAccount)
        print(f"Info: create user {row.userAccount}")
    gid = state.groups.get(row.connGroup)
    if gid is None:
        client.call("POST", "connectionGroups", {"parentIdentifier": "ROOT", "name": row.connGroup,
                                                 "type": "ORGANIZATIONAL", "attributes": {}})
        state.groups = by_name(client.call("GET", "connectionGroups"))
        gid = state.groups.get(row.connGroup)
        print(f"Info: create group {row.connGroup}")
    if gid is None:
        raise ApiError(f"group {row.connGroup} 取不到 id")
    payload = {"parentIdentifier": gid, "name": name, "protocol": row.connProtocol, "parameters": params(row),
               "attributes": {"max-connections": "", "max-connections-per-user": ""}}
    cid = state.connections.get(name)
    if cid is not None:
        client.call("PUT", f"connections/{cid}", payload)
    else:
        client.call("POST", "connections", payload)
        state.connections = by_name(client.call("GET", "connections"))
        cid = state.connections.get(name)
    print(f"Info: sync connection {name}")
    if cid is None:
        raise ApiError(f"connection {name} 取不到 id")
    profiles = client.call("GET", "sharingProfiles") or {}
    for pname, read_only in (("ro", "true"), ("rw", "")):
        if any(p.get("name") == pname and p.get("primaryConnectionIdentifier") == cid for p in profiles.values()):
            continue
        client.call("POST", "sharingProfiles", {"primaryConnectionIdentifier": cid, "name": pname,
                                                "parameters": {"read-only": read_only}, "attributes": {}})
    client.call("PATCH", f"users/{urllib.parse.quote(row.userAccount)}/permissions", [
        {"op": "add", "path": f"/connectionGroupPermissions/{gid}", "value": "READ"},
        {"op": "add", "path": f"/connectionPermissions/{cid}", "value": "READ"},
    ])


def cleanup(client, plan):
    """刪掉計畫裡的項目。group 與 connection 重新列一次，同名的每一筆都刪，照舊版逐筆刪的語意。"""
    targets = [("users", item, urllib.parse.quote(item)) for item in plan.delete_users]
    try:
        for kind, names in (("connectionGroups", set(plan.delete_groups)),
                            ("connections", set(plan.delete_connections))):
            if names:
                listing = client.call("GET", kind) or {}
                targets += [(kind, v.get("name"), k) for k, v in sorted(listing.items()) if v.get("name") in names]
    except ApiError as e:
        print(f"Error: 清理前重新列出項目失敗：{e}", file=sys.stderr)
        return 1
    failed = 0
    for kind, item, ident in targets:
        try:
            client.call("DELETE", f"{kind}/{ident}")
            print(f"Info: delete {kind} {item}")
        except ApiError as e:
            print(f"Error: delete {kind} {item} 失敗：{e}", file=sys.stderr)
            failed += 1
    return failed


def main(argv):
    mode = argv[1] if len(argv) > 1 else "sync"
    if mode not in ("sync", "test"):
        print("Error: usage: sync.py [sync|test]", file=sys.stderr)
        return 1
    for name, hint in (("sync.conf", "這個檔在版控裡，用 git restore 還原"),
                       ("sync.secret.env", "這個檔在版控裡，用 git restore 還原")):
        if not os.path.isfile(os.path.join(HERE, name)):
            print(f"Error: {os.path.join(HERE, name)} 不存在（{hint}）", file=sys.stderr)
            return 1
    conf = read_conf(os.path.join(HERE, "sync.conf"))
    conf.update(read_conf(os.path.join(HERE, "sync.secret.env")))
    for key in ("GUACAMOLE_SERVER", "GUACAMOLE_ADMIN", "GUACAMOLE_PASS"):
        if not conf.get(key):
            print(f"Error: sync.conf 或 sync.secret.env 缺 {key}", file=sys.stderr)
            return 1
    mapping_path = os.path.join(HERE, "mapping.env")
    if not os.path.isfile(mapping_path):
        print(f"Error: {mapping_path} 不存在（這個檔在版控裡，用 git restore 還原）", file=sys.stderr)
        return 1
    try:
        rows = parse_mapping(open(mapping_path).read())
    except MappingError as e:
        print(f"Error: {e}，整批中止，什麼都不改", file=sys.stderr)
        return 1
    client = Client(conf["GUACAMOLE_SERVER"], conf.get("DATA_SOURCE") or "mysql")
    try:
        client.login(conf["GUACAMOLE_ADMIN"], conf["GUACAMOLE_PASS"])
    except ApiError as e:
        print(f"Error: 取 token 失敗（檢查 sync.conf 的 SERVER、ADMIN 與 sync.secret.env 的 PASS）：{e}", file=sys.stderr)
        return 1
    if mode == "test":
        print(f"Success: token ok，mapping.env 有 {len(rows)} 筆")
        return 0
    if not rows:
        print("Error: mapping.env 沒有任何一筆連線，照這份清單同步會刪光所有資料，中止", file=sys.stderr)
        return 1
    alive = {i: probe(r) for i, r in enumerate(rows)}
    try:
        state = read_state(client)
    except ApiError as e:
        print(f"Error: 讀取現況失敗：{e}", file=sys.stderr)
        return 1
    plan = build_plan(rows, alive, state, conf["GUACAMOLE_ADMIN"])
    print_plan(plan)
    if not confirm():
        print("Info: 已取消（輸入的名稱與服務名不符）")
        return 1
    failed = 0
    pass_length = int(conf.get("PASS_LENGTH") or 12)
    for i, row in enumerate(rows):
        try:
            sync_row(client, row, conn_name(row, alive[i]), state, pass_length)
        except ApiError as e:
            print(f"Error: {row.userAccount} 這一行同步失敗：{e}", file=sys.stderr)
            failed += 1
    failed += cleanup(client, plan)
    if failed:
        print(f"Error: sync 有 {failed} 處失敗，見上方 Error:", file=sys.stderr)
        return 1
    print("Success: sync 完成")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
