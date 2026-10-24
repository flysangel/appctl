#!/usr/bin/env python3
# run-tests.py — task test 的執行器：自動收集 tests/test_*.py，逐支執行，每支都要結束碼 0 且印 ALL <n> PASS。
# 用法：run-tests.py [repo 路徑]；不給時用本檔所在 repo。
# 清單用 git ls-files --cached --others --exclude-standard 取得：還沒 git add 的新測試也收得到，
# 被 .gitignore 擋掉的本機測試（例如 finetune）照樣排除。
# 「ALL <n> PASS」是唯一的通過證據：漏寫 __main__ 的測試檔零輸出、結束碼 0，只看結束碼會把它當成通過。
import os
import re
import subprocess
import sys

PASS_RE = re.compile(r"^ALL \d+ PASS$", re.M)


def collect(root):
    out = subprocess.run(
        ["git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard",
         "--", "tests/test_*.py"],
        capture_output=True, text=True, check=True,
    ).stdout
    # 排序讓執行順序固定，失敗時好對照
    return sorted({line for line in out.splitlines() if line})


def main(argv):
    root = os.path.abspath(argv[1]) if len(argv) > 1 else os.path.dirname(
        os.path.dirname(os.path.abspath(__file__)))
    files = collect(root)
    if not files:
        print(f"Error: {root}/tests 沒有任何 test_*.py", file=sys.stderr)
        return 1
    failed = []
    for rel in files:
        r = subprocess.run([sys.executable, os.path.join(root, rel)],
                           capture_output=True, text=True, cwd=root)
        sys.stdout.write(r.stdout)
        sys.stderr.write(r.stderr)
        if r.returncode != 0:
            failed.append(f"{rel}（結束碼 {r.returncode}）")
        elif not PASS_RE.search(r.stdout):
            failed.append(f"{rel}（沒有印 ALL <n> PASS，檔尾可能缺 __main__ 執行段）")
    if failed:
        print("Error: 測試未通過：", file=sys.stderr)
        for item in failed:
            print(f"  {item}", file=sys.stderr)
        return 1
    print(f"Success: {len(files)} 支測試全部通過")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
