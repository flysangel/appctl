#!/bin/bash
# assert-images.sh — 兩道檢查：渲染後每個 image 都是完整參照，且該參照於來源 registry 仍存在（skopeo inspect）。
# 完整參照的離線檢查正本在 bin/check-manifests.py（lint 與 pre-push 都跑）；這裡的判準只是第二道防線。
# 用法：bash bin/assert-images.sh <SVC>
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
svc="${1:?需傳 SVC（services/ 下目錄名，如 grafana）}"
[[ -d "${here}/services/${svc}" ]] || { echo "Error: ${here}/services/${svc} 不存在" >&2; exit 1; }
command -v skopeo >/dev/null || { echo "Error: skopeo not installed" >&2; exit 1; }

rc=0; n=0
while read -r ref; do
  n=$((n+1))
  # 完整參照的判準：含 registry/repo 分隔的 `/` 加 tag，或帶 digest。短名兩者皆無。
  case "${ref}" in
    */*:*|*@sha256:*) ;;
    *) echo "Error: ${svc} 的 ${ref} 不是完整參照" >&2; rc=1; continue ;;
  esac
  if skopeo inspect --no-tags "docker://${ref}" >/dev/null 2>&1; then
    echo "OK   ${ref}"
  else
    echo "Error: ${ref} 在來源 registry 不存在" >&2
    rc=1
  fi
# 抽取正則含 `-?`：kustomize v5.7.1 依字母序重排 container 欄位，沒有 args/command/env
# 的 container 會讓 image 成為 list item 首鍵，渲染成 `- image: foo`，此時 $2 是 `image:`
# 不是值。所以改用 awk 的 $NF 取最後一欄，兩種形狀（`image: foo` 與 `- image: foo`）都取得到值。
# 軌目錄是 k8s 加上所有 podman 開頭的軌（podman、podman-4060ti）：同一個服務會依主機分軌。
# podman* 刻意留在引號外讓 bash 展開；沒有符合的目錄時保留字面值，由 -d 檢查略過。
done < <(for d in "${here}/services/${svc}/k8s" "${here}/services/${svc}"/podman*; do
           [[ -d "${d}" ]] || continue
           kubectl kustomize "${d}"
         done | grep -oE '^[[:space:]]*-?[[:space:]]*image:[[:space:]]*\S+' | awk '{print $NF}' | sort -u)

# 零命中代表渲染沒產出任何 image，不能當成通過（舊版的假綠燈：
# service.conf 沒有 *_IMAGE 鍵時 while 一次都不跑、rc 維持 0，仍印 Success）。
[[ "${n}" -gt 0 ]] || { echo "Error: ${svc} 渲染產物零個 image" >&2; rc=1; }
[[ "${rc}" -eq 0 ]] && echo "Success: ${svc} 所有 image 皆為完整參照且存在於來源 registry"
exit "${rc}"
