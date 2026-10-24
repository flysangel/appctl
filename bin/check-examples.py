#!/usr/bin/env python3
# check-examples.py — 確認 services/*/secret/ 底下每份 .example 範本都沒被 .gitignore 擋掉。
# 用法：check-examples.py [repo 路徑]；不給時用本檔所在 repo。
# 範本被擋掉時，新 clone 或新 worktree 就沒有範本，機密補位與 check-services.py 的渲染都會壞。
# 要靠 git check-ignore，所以只在 task lint 跑；pre-push 用 git archive 展開的樹沒有 .git，不能跑。
import glob
import os
import subprocess
import sys


def main(argv):
    root = os.path.abspath(argv[1]) if len(argv) > 1 else os.path.dirname(
        os.path.dirname(os.path.abspath(__file__)))
    paths = sorted(os.path.relpath(p, root)
                   for p in glob.glob(os.path.join(root, "services", "*", "secret", "**", "*.example"), recursive=True))
    if not paths:
        print("Success: 沒有機密範本要檢查")
        return 0
    # 結束碼 1 代表沒有任何一個被擋，0 代表有，其他是錯誤
    r = subprocess.run(["git", "-C", root, "check-ignore", "--no-index", "--stdin"],
                       input="\n".join(paths) + "\n", capture_output=True, text=True)
    if r.returncode not in (0, 1):
        print(f"Error: git check-ignore 失敗：{r.stderr.strip()}", file=sys.stderr)
        return 1
    ignored = [line for line in r.stdout.splitlines() if line]
    if ignored:
        print("Error: 下列機密範本被 .gitignore 擋掉，不會進版控：", file=sys.stderr)
        for p in ignored:
            print(f"  {p}", file=sys.stderr)
        return 1
    print(f"Success: {len(paths)} 份機密範本都會進版控")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
