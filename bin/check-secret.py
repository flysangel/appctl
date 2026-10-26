#!/usr/bin/env python3
# check-secret.py — 讀機密真值檔，比對同一個值在不同檔案裡是否一致；只印鍵名與一致與否，不印值。
# 用法：check-secret.py <svc> [repo 路徑]；目前只有 quay 有規則。
# quay 的 DB 與 redis 密碼同時寫在 secret.env 與 config.yaml，帳號與庫名同時寫在 deployment.yaml 與 DB_URI。
# 不一致時渲染與 apply 都成功，quay 起得來卻連不上 DB。
import os
import sys
from urllib.parse import urlsplit

import yaml


def parse_env(path):
    """照 kustomize envs 的語意：跳過空行與 # 行，用第一個 = 切開。"""
    values = {}
    for line in open(path):
        line = line.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value
    return values


def container_env(deployment, name):
    doc = yaml.safe_load(open(deployment))
    for c in doc["spec"]["template"]["spec"]["containers"]:
        if c["name"] == name:
            return {e["name"]: e.get("value") for e in c.get("env") or []}
    return {}


def check_quay(svc_dir):
    secret = os.path.join(svc_dir, "secret")
    env_path = os.path.join(secret, "secret.env")
    conf_path = os.path.join(secret, "config.yaml")
    errors = []
    for path in (env_path, conf_path):
        if not os.path.isfile(path):
            errors.append(f"缺 {os.path.relpath(path, svc_dir)}")
    if errors:
        return [], errors
    env = parse_env(env_path)
    conf = yaml.safe_load(open(conf_path)) or {}
    pg = container_env(os.path.join(svc_dir, "base", "deployment.yaml"), "postgresql")
    uri = urlsplit(conf.get("DB_URI", ""))
    pairs = [
        ("secret.env 的 DB_PASSWORD", env.get("DB_PASSWORD"), "config.yaml 的 DB_URI 密碼", uri.password),
        ("secret.env 的 REDIS_PASSWORD", env.get("REDIS_PASSWORD"),
         "config.yaml 的 BUILDLOGS_REDIS.password", (conf.get("BUILDLOGS_REDIS") or {}).get("password")),
        ("secret.env 的 REDIS_PASSWORD", env.get("REDIS_PASSWORD"),
         "config.yaml 的 USER_EVENTS_REDIS.password", (conf.get("USER_EVENTS_REDIS") or {}).get("password")),
        ("deployment.yaml 的 POSTGRES_USER", pg.get("POSTGRES_USER"), "config.yaml 的 DB_URI 帳號", uri.username),
        ("deployment.yaml 的 POSTGRES_DB", pg.get("POSTGRES_DB"), "config.yaml 的 DB_URI 庫名", uri.path.lstrip("/")),
    ]
    ok, bad = [], []
    for left, lv, right, rv in pairs:
        line = f"{left} 對 {right}"
        if lv is not None and lv == rv:
            ok.append(f"{line}：一致")
        else:
            bad.append(f"{line}：不一致" if lv is not None and rv is not None else f"{line}：有一邊沒有值")
    return ok, bad


RULES = {"quay": check_quay}


def main(argv):
    if len(argv) < 2:
        print("Error: usage: check-secret.py <svc> [repo 路徑]", file=sys.stderr)
        return 1
    svc = argv[1]
    root = os.path.abspath(argv[2]) if len(argv) > 2 else os.path.dirname(
        os.path.dirname(os.path.abspath(__file__)))
    if svc not in RULES:
        print(f"Error: {svc} 沒有一致性規則（有規則的服務：{'、'.join(sorted(RULES))}）", file=sys.stderr)
        return 1
    ok, bad = RULES[svc](os.path.join(root, "services", svc))
    for line in ok:
        print(f"Info: {line}")
    for line in bad:
        print(f"Error: {line}", file=sys.stderr)
    if bad:
        return 1
    print(f"Success: {svc} 的機密在各檔之間一致")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
