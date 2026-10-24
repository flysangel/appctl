#!/usr/bin/env python3
# test_check_secret.py — bin/check-secret.py 自檢：quay 的 secret.env、config.yaml 與 deployment.yaml 三處一致性。
# 每個案例在暫存目錄造假 quay 服務；所有輸出都不能含 fixture 的密碼字串。
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))
SCRIPT = os.path.join(ROOT, "bin", "check-secret.py")

DB_PW = "dbpw1234fixture"
REDIS_PW = "redispw5678fixture"

SECRET_ENV = f"""\
# 註解
DB_PASSWORD={DB_PW}
REDIS_PASSWORD={REDIS_PW}
"""

CONFIG = """\
BUILDLOGS_REDIS:
    host: 127.0.0.1
    password: "{redis1}"
    port: 6379
DB_URI: postgresql://{user}:{db}@127.0.0.1:5432/{name}
USER_EVENTS_REDIS:
    host: 127.0.0.1
    password: "{redis2}"
    port: 6379
"""

DEPLOYMENT = """\
apiVersion: apps/v1
kind: Deployment
metadata:
  name: quay
spec:
  template:
    spec:
      containers:
      - name: quay-app
        image: example.io/quay:1
      - name: postgresql
        image: example.io/pg:1
        env:
        - name: POSTGRES_USER
          value: quayuser
        - name: POSTGRES_DB
          value: quay
"""


def config(user="quayuser", db=DB_PW, name="quay", redis1=REDIS_PW, redis2=REDIS_PW):
    return CONFIG.format(user=user, db=db, name=name, redis1=redis1, redis2=redis2)


def run(secret_env=SECRET_ENV, config_yaml=None, example_env="DB_PASSWORD=changeme\n", example_conf="x: 1\n"):
    if config_yaml is None:
        config_yaml = config()
    with tempfile.TemporaryDirectory() as tmp:
        d = os.path.join(tmp, "services", "quay")
        os.makedirs(os.path.join(d, "secret"))
        os.makedirs(os.path.join(d, "base"))
        open(os.path.join(d, "base", "deployment.yaml"), "w").write(DEPLOYMENT)
        open(os.path.join(d, "secret", "secret.env.example"), "w").write(example_env)
        open(os.path.join(d, "secret", "config.yaml.example"), "w").write(example_conf)
        if secret_env is not None:
            open(os.path.join(d, "secret", "secret.env"), "w").write(secret_env)
        if config_yaml is not False:
            open(os.path.join(d, "secret", "config.yaml"), "w").write(config_yaml)
        r = subprocess.run([sys.executable, SCRIPT, "quay", tmp], capture_output=True, text=True)
    out = r.stdout + r.stderr
    for value in (DB_PW, REDIS_PW, "otherpw"):
        assert value not in out, f"輸出含機密值：{out}"
    return r


def test_consistent_passes():
    r = run()
    assert r.returncode == 0, r.stdout + r.stderr


def test_db_password_mismatch_fails():
    r = run(config_yaml=config(db="otherpw"))
    assert r.returncode == 1, r.stdout + r.stderr
    assert "DB_PASSWORD" in r.stderr, r.stderr


def test_redis_mismatch_fails():
    r = run(config_yaml=config(redis2="otherpw"))
    assert r.returncode == 1, r.stdout + r.stderr
    assert "USER_EVENTS_REDIS" in r.stderr, r.stderr


def test_db_user_mismatch_fails():
    r = run(config_yaml=config(user="someone"))
    assert r.returncode == 1, r.stdout + r.stderr
    assert "POSTGRES_USER" in r.stderr, r.stderr


def test_placeholder_fails():
    r = run(example_env=SECRET_ENV, example_conf=config())
    assert r.returncode == 1, r.stdout + r.stderr
    assert "佔位值" in r.stderr, r.stderr


def test_missing_file_fails():
    r = run(config_yaml=False)
    assert r.returncode == 1, r.stdout + r.stderr
    assert "config.yaml" in r.stderr, r.stderr


def test_unknown_service_fails():
    r = subprocess.run([sys.executable, SCRIPT, "grafana", ROOT], capture_output=True, text=True)
    assert r.returncode == 1, r.stdout + r.stderr
    assert "沒有一致性規則" in r.stderr, r.stderr


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_") and callable(f)]
    for n, f in fns:
        f()
        print(f"[OK] {n}")
    print(f"ALL {len(fns)} PASS")
