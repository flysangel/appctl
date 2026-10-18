#!/usr/bin/env python3
"""check-docs — 驗 repo 的四大文件符合必要節與引用規則。

用法：check-docs.py                 驗目前所在的 repo（git rev-parse --show-toplevel）
      check-docs.py <repo 路徑> [...] 驗指定路徑，例如 worktree
任一項失敗 exit 1。

為什麼需要這支：四大文件的規則沒有程式在執法，每個 repo 都靠人眼對照很容易漏。
必要節清單內嵌在 REQUIRED，不讀任何規則檔，所以這支可以原樣複製到各 repo。
改規則時一起改這份。

驗以下幾條：
  C1 四份檔都存在
  C2 README、AGENTS、ARCHITECTURE 的 ## 序列符合必要節的任一套；一套裡的「…」吃掉相鄰必要節之間的任意個 ##；
     全部寫成「## N.」從 1 連號
  C3 PROGRESS 沒有 ## 節，也沒有編號清單
  C4 「見〈節名〉」「`路徑`〈節名〉」「[文字](路徑)〈節名〉」對得到節標題（## 到 ######，比對前去掉編號）。
     markdown 連結對文件所在目錄解析，其餘對本 repo 解析。
     「工作區根 repo 的 `路徑`」指向 repo 外面，驗不了：印 Info 後略過。
     repo 有 CLAUDE.md 時，也驗它的引用
  C5 沒有行號引用，例如 `foo.sh:12`
  C6 沒有本機絕對路徑：執行時 $HOME 開頭的路徑，加上 repo 主工作樹的路徑。
     /home/bigred 這類容器內路徑不擋。在 worktree 裡跑時，主工作樹的路徑也一起擋
"""
import os
import re
import subprocess
import sys
from pathlib import Path

DOCS = ("README.md", "AGENTS.md", "docs/ARCHITECTURE.md", "docs/PROGRESS.md")
# 一套必要節裡的通配：吃掉相鄰必要節之間的零到多個 ##；一套最多一個
WILDCARD = "…"
# 必要節：{檔名: [一套, ...]}，同一份文件列多套時符合任一套即可
REQUIRED: dict[str, list[list[str]]] = {
    "README.md": [
        ["安裝", "用法", WILDCARD, "貢獻", "授權"],
        # 舊版，過渡期；全部 repo 套用新版後刪掉
        ["前置需求", "上手", "常用操作", "文件導覽"],
    ],
    "AGENTS.md": [["30 秒定位", "動手前", "地雷", "改完要驗", "不要做的事"]],
    "docs/ARCHITECTURE.md": [["定位與邊界", "目錄結構", "依賴與資料流", "設計取捨", "已知的結構性限制"]],
}
FENCE_RE = re.compile(r"^```.*?^```[^\n]*$", re.M | re.S)
INLINE_RE = re.compile(r"`[^`\n]*`")
HEADING_RE = re.compile(r"^(#+)\s+(.+?)\s*$", re.M)
# 編號是「1.」「8.1」「9.2.」；「30 秒定位」的 30 是節名，不是編號
NUMBER_RE = re.compile(r"^(?:\d+(?:\.\d+)*\.|\d+(?:\.\d+)+)\s+")
ORDERED_RE = re.compile(r"^\s*\d+\.\s", re.M)
REF_RE = re.compile(r"(工作區根 repo 的 )?(?:`([^`\n]+)`|本檔|見)〈([^〉\n]+)〉")
MD_REF_RE = re.compile(r"\[[^\]\n]*\]\(([^)\s]+)\)〈([^〉\n]+)〉")
LINE_REF_RE = re.compile(r"[\w./-]+\.(?:md|sh|py|ya?ml|txt|conf|ps1|bat|toml):\d+")


def strip_fences(text: str) -> str:
    """去掉 fenced code。程式碼區塊裡的 # 與〈〉不算標題、也不算引用。"""
    return FENCE_RE.sub("", text)


def headings(text: str) -> list[tuple[int, str]]:
    """回傳 (層級, 原始標題文字)，不含程式碼區塊裡的 # 行。"""
    return [(len(m.group(1)), m.group(2)) for m in HEADING_RE.finditer(strip_fences(text))]


def titles(text: str, levels: range) -> list[str]:
    """回傳指定層級的標題文字，去掉開頭編號。"""
    return [NUMBER_RE.sub("", t) for level, t in headings(text) if level in levels]


def _matches(actual: list[str], sections: list[str]) -> bool:
    """## 標題從 1 連號，且去掉編號後符合一套必要節。sections 可含一個 WILDCARD。"""
    names = []
    for i, title in enumerate(actual, 1):
        if not title.startswith(f"{i}. "):
            return False
        names.append(title[len(f"{i}. "):])
    if WILDCARD not in sections:
        return names == sections
    at = sections.index(WILDCARD)
    head, tail = sections[:at], sections[at + 1:]
    return (len(head) + len(tail) <= len(names)
            and names[:len(head)] == head
            and names[len(names) - len(tail):] == tail)


def check_sections(rel: str, text: str, alternatives: list[list[str]]) -> list[str]:
    """C2：## 序列要符合任一套必要節，並寫成「## N.」從 1 連號。"""
    actual = [t for level, t in headings(text) if level == 2]
    if any(_matches(actual, sections) for sections in alternatives):
        return []
    return [f"C2 {rel}: ## 節應為下列任一套 {alternatives}（每節寫成「## N.」從 1 連號，"
            f"「{WILDCARD}」是零到多個其他節），實際為 {actual}"]


def check_progress(rel: str, text: str) -> list[str]:
    """C3：PROGRESS 只列待辦，不分節、不編號。"""
    errors = []
    if any(level == 2 for level, _ in headings(text)):
        errors.append(f"C3 {rel}: 不應有 ## 節")
    if ORDERED_RE.search(strip_fences(text)):
        errors.append(f"C3 {rel}: 不應有編號清單")
    return errors


def is_real_ref(m: re.Match, code_spans: list[tuple[int, int]]) -> bool:
    """引用落在 inline code 裡面就不算，例如 `見〈節名〉`。

    路徑寫法的引用，開頭的反引號必須正好是一段 inline code 的起點；
    否則是兩段 code 之間的文字被誤配成路徑。
    """
    path_given = m.group(2) is not None
    ref_start = m.start(2) - 1 if path_given else m.start()
    if any(start < ref_start < end for start, end in code_spans):
        return False
    return not path_given or ref_start in {start for start, _ in code_spans}


def check_refs(doc: Path, repo: Path) -> list[str]:
    """C4：引用的檔要存在，節名要對得到該檔的標題。「工作區根 repo 的」引用驗不了，印 Info 後略過。"""
    rel = doc.relative_to(repo).as_posix()
    text = strip_fences(doc.read_text(encoding="utf-8"))
    code_spans = [m.span() for m in INLINE_RE.finditer(text)]
    refs = []
    for m in REF_RE.finditer(text):
        if is_real_ref(m, code_spans):
            to_root, path, section = m.groups()
            if to_root:
                print(f"Info: {rel}：略過工作區根的引用〈{section}〉")
                continue
            refs.append((m.group(0), doc if path is None else repo / path, section))
    # markdown 連結的路徑相對於文件自己的目錄，例如 docs/ 底下寫 ../AGENTS.md
    for m in MD_REF_RE.finditer(text):
        if not any(start < m.start() < end for start, end in code_spans):
            refs.append((m.group(0), doc.parent / m.group(1), m.group(2)))
    errors = []
    for label, target, section in refs:
        if not target.is_file():
            errors.append(f"C4 {rel}: {label} 的檔案不存在")
        elif NUMBER_RE.sub("", section) not in titles(target.read_text(encoding="utf-8"), range(2, 7)):
            errors.append(f"C4 {rel}: {label} 對不到節標題")
    return errors


def main_checkout(path: Path) -> Path | None:
    """回傳 path 所屬 repo 的主工作樹；不是 git repo 時回 None。"""
    result = subprocess.run(
        ["git", "-C", str(path), "rev-parse", "--path-format=absolute", "--git-common-dir"],
        capture_output=True, text=True)
    if result.returncode != 0:
        return None
    return Path(result.stdout.strip()).parent


def leak_roots(repo: Path) -> set[str]:
    """C6 要擋的本機路徑：執行時 $HOME 開頭的路徑，加上 repo 的主工作樹。"""
    roots = {os.environ["HOME"] + "/"}
    main = main_checkout(repo)
    if main is not None:
        roots.add(str(main))
    return roots


def check_leaks(rel: str, text: str, roots: set[str]) -> list[str]:
    """C5、C6：不寫行號，不寫工作區根的本機絕對路徑。"""
    errors = [f"C5 {rel}: 行號引用 {m}" for m in LINE_REF_RE.findall(strip_fences(text))]
    errors += [f"C6 {rel}: 含本機絕對路徑 {root}" for root in sorted(roots) if root in text]
    return errors


def check_repo(repo: Path, required: dict[str, list[list[str]]]) -> list[str]:
    """驗一個 repo，回傳錯誤清單；每則以「C<n> <相對檔名>: 」開頭。"""
    roots = leak_roots(repo)
    errors = []
    for rel in DOCS:
        path = repo / rel
        if not path.is_file():
            errors.append(f"C1 {rel}: 檔案不存在")
            continue
        text = path.read_text(encoding="utf-8")
        if rel in required:
            errors += check_sections(rel, text, required[rel])
        else:
            errors += check_progress(rel, text)
        errors += check_refs(path, repo)
        errors += check_leaks(rel, text, roots)
    claude_md = repo / "CLAUDE.md"
    if claude_md.is_file():
        errors += check_refs(claude_md, repo)
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
        errors = check_repo(repo, REQUIRED)
        if errors:
            failed = True
            print(f"Error: {repo} 有 {len(errors)} 項不符", file=sys.stderr)
            for e in errors:
                print(f"  {e}", file=sys.stderr)
        else:
            print(f"Success: {repo}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
