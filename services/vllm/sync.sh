#!/bin/bash
# sync.sh — 把 vllm 容器要載入的模型權重下載進具名卷（冪等：hf download 跳過已下載的檔）。用法：TRACK=<軌> bash sync.sh [sync|test]
# 註記：逐項容錯，捨棄 -e；一個模型失敗不擋其餘模型，失敗由迴圈累積在 fail
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mode="${1:-sync}"
# 模型清單取自指定軌的渲染產物，各軌載的模型可以不同。
# 所以不給預設軌，免得靜默下載錯的模型。
track="${TRACK:?需傳 TRACK（用法：task seed SVC=vllm TRACK=<軌>，唯讀檢查用 task check:seed）}"

case "${mode}" in
  sync|test) ;;
  *) echo "Error: 用法 TRACK=<軌> bash sync.sh [sync|test]" >&2; exit 1 ;;
esac
[[ -d "${here}/${track}" ]] || { echo "Error: 找不到 ${here}/${track}/（vllm 沒有 ${track} 軌）" >&2; exit 1; }
for cmd in kubectl python3 sudo; do
  command -v "${cmd}" >/dev/null || { echo "Error: 缺 ${cmd}" >&2; exit 1; }
done

# 從指定軌的渲染產物列出要下載的權重，一行一組：映像、模型、卷名、掛載點。
# 模型清單不另寫一份：跟實際部署的 args 同一個來源。改模型改 base/deployment.yaml，或該軌 overlay 的 args。
# 判斷哪個容器要下載：args 第一項不是旗標（vllm serve 的第一個引數就是模型名），而且恰好掛一個 PVC 卷當權重卷。
list_downloads() {
  kubectl kustomize "${here}/${track}" | python3 -c '
import sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if not doc or doc.get("kind") != "Deployment" or doc["metadata"]["name"] != "vllm":
        continue
    spec = doc["spec"]["template"]["spec"]
    claims = {v["name"]: v["persistentVolumeClaim"]["claimName"]
              for v in spec.get("volumes", []) if "persistentVolumeClaim" in v}
    for c in spec["containers"]:
        args = c.get("args") or []
        mounts = [(claims[m["name"]], m["mountPath"])
                  for m in c.get("volumeMounts", []) if m["name"] in claims]
        if args and not args[0].startswith("-") and len(mounts) == 1:
            print(c["image"], args[0], *mounts[0])
'
}

# main
# 先用命令替換收下清單：直接接進程序替換的話，kubectl 或 python 失敗時 rc 會被吞掉
rows="$(list_downloads)" || { echo "Error: 渲染 ${here}/${track} 或抽取模型清單失敗" >&2; exit 1; }
[[ -n "${rows}" ]] || { echo "Error: 渲染產物裡找不到要下載的模型" >&2; exit 1; }

fail=0
# 迴圈內的 podman run 會吃 stdin，改用檔案描述子 3 餵資料
while read -r -u 3 image model volume mount_path; do
  hf_cmd=(sudo podman run --rm --entrypoint hf -v "${volume}:${mount_path}")
  if [[ "${mode}" == "test" ]]; then
    # 離線模式下 hf download 只查快取，快取沒有就失敗，不會連外（映像內 hf 1.30.0，2026-09-18 實測空卷回 rc=1）
    if "${hf_cmd[@]}" -e HF_HUB_OFFLINE=1 "${image}" download "${model}" >/dev/null 2>&1; then
      echo "Info: 模型已在卷 ${volume}：${model}"
    else
      echo "Error: 模型不在卷 ${volume}：${model}（test 模式不下載）" >&2
      fail=1
    fi
    continue
  fi
  echo "Info: 下載 ${model} 進卷 ${volume}（首次較久，已下載的檔會跳過）"
  if ! "${hf_cmd[@]}" "${image}" download "${model}" >/dev/null; then
    echo "Error: 下載失敗：${model}" >&2
    fail=1
  fi
done 3<<<"${rows}"

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "Success: 模型權重都在卷裡（${mode}）"
