#!/bin/bash
# doctor.sh — 檢查本 repo 的執行環境：命令存在與種類、core.hooksPath 的來源
# 註記：逐項容錯，捨棄 -e；一項沒過不中斷其餘項，讓使用者一次看到全部缺項
set -uo pipefail

fail=0

# 必要命令：need <命令> <種類說明> <種類樣式> <取樣指令...>；樣式比對取樣指令的輸出
need() {
  local cmd="$1"
  local kind="$2"
  local pattern="$3"
  local out

  shift 3
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "Error: 找不到 ${cmd}（${kind}）" >&2
    fail=1
    return 0
  fi
  out="$("$@" 2>&1 || true)"
  if [[ ! "${out}" =~ ${pattern} ]]; then
    echo "Error: ${cmd} 不是 ${kind}" >&2
    fail=1
    return 0
  fi
  printf 'Info: %s 是 %s\n' "${cmd}" "${kind}"
}

# 選用命令：want <命令> <用途>；只有部分 task 用到，缺了只提示
want() {
  local cmd="$1"
  local usage="$2"

  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "Info: 找不到 ${cmd}，${usage} 跑不了；其他 task 不受影響"
  fi
}

# main
# go-task 的 --version 只印版本號，認不出種類，改看 --help 的開頭說明。
need task 'go-task' 'Falls back to the "default" task' task --help
need kubectl 'kubectl' '^Client Version: ' kubectl version --client
need python3 'Python 3' '^Python 3\.' python3 --version
need shellcheck 'ShellCheck' 'ShellCheck' shellcheck --version
need gitleaks 'gitleaks' 'Gitleaks scans code' gitleaks --help

# task test 與 bin/manifest-diff.py 都 import yaml
if command -v python3 >/dev/null 2>&1 && ! python3 -c 'import yaml' >/dev/null 2>&1; then
  echo "Error: python3 缺 PyYAML，task test 跑不了" >&2
  fail=1
fi

want skopeo 'task check:image'
want podman 'podman 軌的 apply、delete'
want sudo 'podman 軌的 apply、delete'
want nmap 'gmm 的 task seed'
want curl 'task smoke'

# local 或 worktree 作用域的 core.hooksPath 會讓 git 不經過帳號層派工 hook，機密掃描靜默失效
scopes="$(git config --show-scope --get-all core.hooksPath || true)"
if [[ "${scopes}" =~ (^|$'\n')(local|worktree)$'\t' ]]; then
  echo "Error: core.hooksPath 有 local 或 worktree 來源，帳號層的機密掃描不會跑。修法：" >&2
  echo "  git config --local --unset core.hooksPath" >&2
  fail=1
fi

if [[ "${fail}" -eq 0 ]]; then
  echo "Success: 執行環境齊全"
fi
exit "${fail}"
