#!/bin/bash
# render-diff.sh — 比對某個 revision 與工作樹的渲染產物，用來確認改共用寫法時部署結果沒變。
# 用法：bash bin/render-diff.sh <svc> <軌> <REV>；結束碼 0 沒有差異，1 有差異，2 是參數、revision 或渲染錯誤。
# 註記：兩邊都只帶版控內與未追蹤但沒被 .gitignore 擋的檔，機密只用 .example 補位，真值檔不進暫存目錄；
#       所以比對不含 Secret 的值。RENDER_DIFF_KEEP=1 時留下暫存目錄（給測試檢查用）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 給 trap 用
W=""

cleanup() {
  if [[ -n "${W}" && "${RENDER_DIFF_KEEP:-}" != 1 ]]; then
    rm -rf "${W}"
  fi
}

# 把 .example 複製成去掉副檔名的真名，讓 secretGenerator 讀得到
fill_examples() {
  local dir="$1"
  local ex

  while read -r -d '' ex; do
    cp "${ex}" "${ex%.example}"
  done < <(find "${dir}" -name '*.example' -print0)
}

render() {
  local dir="$1"
  local out="$2"

  if ! kubectl kustomize "${dir}" >"${out}" 2>"${out}.err"; then
    echo "Error: kubectl kustomize ${dir#"${W}/"} 失敗：$(head -1 "${out}.err")" >&2
    exit 2
  fi
}

# main
if (( $# != 3 )); then
  echo "Error: usage: render-diff.sh <svc> <軌> <REV>" >&2
  exit 2
fi
svc="$1"
track="$2"
rev="$3"
case "${track}" in
  podman*) diff_track=podman ;;
  k8s) diff_track=k8s ;;
  *) echo "Error: 軌要是 k8s 或 podman 開頭" >&2; exit 2 ;;
esac
if ! git -C "${ROOT}" rev-parse --quiet --verify "${rev}^{commit}" >/dev/null; then
  echo "Error: 找不到 revision ${rev}" >&2
  exit 2
fi

W="$(mktemp -d)"
trap cleanup EXIT
mkdir -p "${W}/old" "${W}/new"

if ! git -C "${ROOT}" archive "${rev}" "services/${svc}" | tar -x -C "${W}/old"; then
  echo "Error: ${rev} 沒有 services/${svc}" >&2
  exit 2
fi
# 工作樹那一邊只取沒被 .gitignore 擋的檔，真值檔與 sync 工具的本機檔都不會被複製
while read -r -d '' f; do
  mkdir -p "${W}/new/$(dirname "${f}")"
  cp "${ROOT}/${f}" "${W}/new/${f}"
done < <(git -C "${ROOT}" ls-files -z --cached --others --exclude-standard -- "services/${svc}")

fill_examples "${W}/old"
fill_examples "${W}/new"
render "${W}/old/services/${svc}/${track}" "${W}/old.yaml"
render "${W}/new/services/${svc}/${track}" "${W}/new.yaml"

if [[ "${RENDER_DIFF_KEEP:-}" == 1 ]]; then
  echo "Info: 暫存目錄留在 ${W}"
fi
python3 "${ROOT}/bin/manifest-diff.py" "${W}/old.yaml" "${W}/new.yaml" --track "${diff_track}"
