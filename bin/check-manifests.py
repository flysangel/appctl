#!/usr/bin/env python3
# check-manifests.py — 離線檢查 services/ 底下的 YAML 原始檔：每個 image 都是完整參照，kustomization 不准有 images:。
# 用法：check-manifests.py [repo 路徑]；不給時用本檔所在 repo。pre-push 傳入 git archive 展開的樹。
# 只讀原始檔、不渲染：manifest 直接寫完整參照後，原始檔的 image 就是部署出去的值，所以不需要機密檔。
# 跳過 services/*/secret/：那裡是機密範本與真值，不含 image。
import os
import re
import sys

import yaml

DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")


def image_problem(ref):
    """合格回 None，不合格回原因。"""
    if "/" not in ref:
        return "沒有 registry 主機"
    host = ref.split("/", 1)[0]
    if not ("." in host or ":" in host or host == "localhost"):
        return "第一段不是 registry 主機"
    digest = DIGEST_RE.search(ref)
    if "@" in ref and not digest:
        return "digest 格式不對（要 sha256: 加 64 個十六進位字元）"
    name = DIGEST_RE.sub("", ref)
    # tag 只看最後一個 / 之後：localhost:5000/foo 的 : 是埠號，不是 tag
    last = name.rsplit("/", 1)[1]
    tag = last.split(":", 1)[1] if ":" in last else None
    if tag == "latest":
        return "不准用 latest"
    if tag is None and not digest:
        return "沒有 tag 也沒有 digest"
    return None


def images_in(node):
    if isinstance(node, dict):
        for key, value in node.items():
            if key == "image" and isinstance(value, str):
                yield value
            else:
                yield from images_in(value)
    elif isinstance(node, list):
        for item in node:
            yield from images_in(item)


def yaml_files(services):
    for dirpath, dirnames, filenames in os.walk(services):
        rel = os.path.relpath(dirpath, services).split(os.sep)
        if len(rel) >= 2 and rel[1] == "secret":
            dirnames[:] = []
            continue
        for name in sorted(filenames):
            if name.endswith((".yaml", ".yml")):
                yield os.path.join(dirpath, name)


def main(argv):
    root = os.path.abspath(argv[1]) if len(argv) > 1 else os.path.dirname(
        os.path.dirname(os.path.abspath(__file__)))
    services = os.path.join(root, "services")
    errors = []
    for path in yaml_files(services):
        rel = os.path.relpath(path, root)
        try:
            docs = [d for d in yaml.safe_load_all(open(path)) if d is not None]
        except yaml.YAMLError as e:
            errors.append(f"{rel}：YAML 解析失敗（{e.__class__.__name__}）")
            continue
        for doc in docs:
            if os.path.basename(path) == "kustomization.yaml" and isinstance(doc, dict) and "images" in doc:
                errors.append(f"{rel}：不准有 images:，image 直接寫完整參照在 manifest")
            for ref in images_in(doc):
                problem = image_problem(ref)
                if problem:
                    errors.append(f"{rel}：{ref} {problem}")
    if errors:
        print("Error: manifest 的 image 檢查未通過：", file=sys.stderr)
        for e in errors:
            print(f"  {e}", file=sys.stderr)
        return 1
    print("Success: services/ 的 image 都是完整參照")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
