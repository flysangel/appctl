#!/usr/bin/env python3
"""check-taskfile — 驗各 repo 的 Taskfile 符合分類原則（~/.claude/rules/taskfile.md〈分類原則〉）。

用法：check-taskfile.py                 驗目前所在的 repo（git rev-parse --show-toplevel）
      check-taskfile.py <repo 路徑> [...] 驗指定路徑，例如 worktree
任一項失敗 exit 1。還沒套用分類原則的 repo 印 Info 後略過。
只依賴所在 repo，不讀任何設定檔，所以這支可以原樣複製到各 repo。

為什麼需要這支：分類原則靠人記，新增 task 時就會漏標角色或漏掉破壞性確認。
「套用」的判準是頂層 Taskfile 有 dev、ops、release 其中一個選單 task。
找檔案時跳過 . 開頭的目錄，也跳過含 .git 的子目錄（巢狀的子 repo 各自檢查）。
只用標準函式庫，所以只讀 Taskfile 固定的縮排結構，不解析完整 YAML。

驗以下幾條：
  T1 每個有 desc 的 task，desc 寫成一行，且開頭是角色標記 [開發]、[維運]、[發版]
  T2 desc 含【破壞性】時，要緊接在角色後面，且 task 有 prompt: 或豁免註解
     「# 確認由腳本負責：<腳本路徑>」
  T3 task 名稱以 : 切開，第一段要在 VERBS 裡。default 與頂層的選單 task 不驗
  T4 選單名稱 dev、ops、release 只能出現在頂層 Taskfile，且名稱要完全相同（dev:x 不行）
  T5 每個 include 都用區塊寫法，並含 flatten: true
"""
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import NamedTuple

ROLES = ("[開發]", "[維運]", "[發版]")
MENU = ("dev", "ops", "release")
VERBS = {
    "test": "離線測試",
    "lint": "離線靜態檢查",
    "check": "連網的唯讀查核",
    "smoke": "在真機上跑的測試",
    "update": "依釘選的版本更新上游內容",
    "bootstrap": "初始化本機環境",
    "clone": "把遠端 repo 複製到本機",
    "clean": "刪可重建的本機產物與快取",
    "reset": "還原成初始狀態，不可逆",
    "doctor": "檢查執行環境：命令存在與種類、hooksPath 來源，不安裝",
    "apply": "套設定到真實環境",
    "delete": "拆除真實環境的資源",
    "seed": "把帳號、設定、權重寫進執行中的服務",
}
DESTRUCTIVE = "【破壞性】"
EXEMPT_RE = re.compile(r"^\s*# 確認由腳本負責：\S", re.M)
TASKFILE_NAMES = ("taskfile.yml", "taskfile.yaml")

ENTRY_RE = re.compile(r"""^  ("[^"]*"|'[^']*'|[^\s'"#](?:(?!:(?:\s|$)).)*):(?:[ \t]+(.*))?$""")
COMMENT_RE = re.compile(r"(?:^|\s+)#.*$")


class Entry(NamedTuple):
    name: str
    value: str
    body: list[str]


def indent_of(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def is_content(line: str) -> bool:
    """非空白、不是整行註解。"""
    s = line.strip()
    return bool(s) and not s.startswith("#")


def section_entries(text: str, key: str) -> list[Entry]:
    """找第 0 欄的 `<key>:`，回傳底下縮排 2 格的每個項目。沒有這個 key 時回傳 []。"""
    lines = text.replace("\r", "").split("\n")
    head = re.compile(rf"^{re.escape(key)}:\s*(#.*)?$")
    start = next((i for i, ln in enumerate(lines) if head.match(ln)), None)
    if start is None:
        return []
    entries: list[Entry] = []
    for ln in lines[start + 1:]:
        if is_content(ln) and indent_of(ln) == 0:
            break
        m = ENTRY_RE.match(ln) if indent_of(ln) == 2 else None
        if m:
            name = m.group(1).strip("\"'")
            value = COMMENT_RE.sub("", m.group(2) or "").strip()
            entries.append(Entry(name, value, []))
        elif is_content(ln) and indent_of(ln) <= 2:
            entries.append(Entry("", "", []))  # 看不懂的項目，擋住後續行混進上一項
        elif entries:
            entries[-1].body.append(ln)
    return [e for e in entries if e.name]


def child_value(entry: Entry, key: str) -> str | None:
    """在 body 裡找縮排剛好 4 格的 `<key>:`，回傳冒號後的原文；找不到回傳 None。"""
    head = re.compile(rf"^ {{4}}{re.escape(key)}:(.*)$")
    for ln in entry.body:
        m = head.match(ln)
        if m:
            return m.group(1).strip()
    return None


def find_taskfiles(repo: Path) -> list[Path]:
    """走訪 repo，跳過 . 開頭的目錄與巢狀 git repo，收集 Taskfile.yml 與 Taskfile.yaml（檔名不分大小寫）。"""
    found = []
    for root, dirs, files in os.walk(repo):
        # 巢狀的子 repo 是獨立的 repo，要由它自己的檢查負責。
        dirs[:] = [d for d in dirs if not d.startswith(".") and not (Path(root) / d / ".git").exists()]
        found += [Path(root) / f for f in files if f.lower() in TASKFILE_NAMES]
    return sorted(found)


def top_taskfile(repo: Path) -> Path | None:
    for name in ("Taskfile.yml", "Taskfile.yaml"):
        if (repo / name).is_file():
            return repo / name
    return None


def is_applied(repo: Path) -> bool:
    """頂層 Taskfile 存在，且 tasks 底下有選單 task。"""
    top = top_taskfile(repo)
    if top is None:
        return False
    text = top.read_text(encoding="utf-8")
    return any(e.name in MENU for e in section_entries(text, "tasks"))


def unquote(value: str) -> str:
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def check_desc(rel: str, entry: Entry) -> list[str]:
    """T1、T2：驗一個 task 的 desc。"""
    raw = child_value(entry, "desc")
    if raw is None:
        return []
    if raw == "" or raw[0] in "|>":
        return [f"T1 {rel} {entry.name}：desc 要寫成一行"]
    desc = unquote(COMMENT_RE.sub("", raw) if raw[0] not in "\"'" else raw)
    role = next((r for r in ROLES if desc.startswith(r)), None)
    if role is None:
        return [f"T1 {rel} {entry.name}：desc 缺角色標記，開頭要是 {'、'.join(ROLES)} 其中之一"]
    if DESTRUCTIVE not in desc:
        return []
    errors = []
    if not desc[len(role):].startswith(DESTRUCTIVE):
        errors.append(f"T2 {rel} {entry.name}：{DESTRUCTIVE}要緊接在角色後面")
    if child_value(entry, "prompt") is None and not EXEMPT_RE.search("\n".join(entry.body)):
        errors.append(f"T2 {rel} {entry.name}：{DESTRUCTIVE}task 缺 prompt: 或豁免註解")
    return errors


def check_name(rel: str, entry: Entry, top: bool) -> list[str]:
    """T3、T4：驗一個 task 的名稱。"""
    verb = entry.name.split(":", 1)[0]
    if entry.name == "default":
        return []
    if verb in MENU:
        if top and entry.name == verb:
            return []
        return [f"T4 {rel} {entry.name}：{verb} 是選單保留名稱，只能出現在頂層 Taskfile"]
    if verb not in VERBS:
        return [f"T3 {rel} {entry.name}：動作 {verb} 不在 VERBS（{'、'.join(VERBS)}）"]
    return []


def check_file(path: Path, rel: str, top: bool) -> list[str]:
    """驗一份 Taskfile，回傳錯誤字串，格式「<T編號> <rel> <名稱>：<原因>」。"""
    text = path.read_text(encoding="utf-8")
    errors = []
    for entry in section_entries(text, "tasks"):
        errors += check_name(rel, entry, top)
        errors += check_desc(rel, entry)
    for entry in section_entries(text, "includes"):
        if entry.value or child_value(entry, "flatten") != "true":
            errors.append(f"T5 {rel} {entry.name}：include 缺 flatten: true，或沒用區塊寫法")
    return errors


def check_repo(repo: Path) -> list[str] | None:
    """沒套用回傳 None；有套用就驗每份 Taskfile。"""
    if not is_applied(repo):
        return None
    top = top_taskfile(repo)
    errors = []
    for path in find_taskfiles(repo):
        rel = path.relative_to(repo).as_posix()
        errors += check_file(path, rel, path == top)
    return errors


def current_repo() -> Path | None:
    """回傳目前所在 repo 的根；不在 git repo 裡時回 None。"""
    result = subprocess.run(["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True)
    return Path(result.stdout.strip()) if result.returncode == 0 else None


def main(argv: list[str]) -> int:
    if argv:
        targets = [Path(a).resolve() for a in argv]
    else:
        repo = current_repo()
        if repo is None:
            print("Error: 目前不在 git repo 裡，請帶 repo 路徑", file=sys.stderr)
            return 1
        targets = [repo]
    failed = False
    for repo in targets:
        if not repo.is_dir():
            print(f"Info: {repo} 不存在，跳過")
            continue
        errors = check_repo(repo)
        if errors is None:
            print(f"Info: {repo} 還沒套用 Taskfile 分類原則，略過")
        elif errors:
            failed = True
            print(f"Error: {repo} 有 {len(errors)} 項不符", file=sys.stderr)
            for e in errors:
                print(f"  {e}", file=sys.stderr)
        else:
            print(f"Success: {repo}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
