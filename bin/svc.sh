#!/bin/bash
# svc.sh — 部署控制。用法：bash bin/svc.sh <svc> <apply|delete> <podman…|k8s>（podman… 指 podman 開頭的軌）
# 渲染全由 kustomize 原生欄位（namespace: 與 images:）表達，本腳本不做任何字串替換。
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
svc="${1:?需傳服務名}"; verb="${2:?需傳 apply|delete}"; track="${3:?需傳 k8s 或 podman 開頭的軌}"
here="${root}/services/${svc}"
# 一道檢查同時涵蓋「服務名打錯」與「該服務無此軌」，訊息須對兩者都成立
[[ -d "${here}/${track}" ]] || { echo "Error: 找不到 ${here}/${track}/（服務名錯誤，或 ${svc} 無 ${track} 軌）" >&2; exit 1; }

# 機密檔缺席時以 .example 佔位值補上——沒有它 kustomize build 直接失敗，
# 連 delete 與純本地語法驗證都跑不動。
for base in secret.env config.yaml; do
  ex="${here}/secret/${base}.example"
  [[ -f "${ex}" && ! -f "${here}/secret/${base}" ]] || continue
  cp "${ex}" "${here}/secret/${base}"
  echo "Info: ${svc} 缺 ${base}，已從 .example 複製佔位值"
  # 用 if，讓迴圈的結束碼固定為 0。這樣之後把迴圈移到檔尾時，AND-list 條件為假也不會讓整支腳本以 1 退出。
  # 走 Info: 而非 Warning:——慣例不另設 Warn 級，警告也走 Info:
  # （見 `~/.claude/rules/terminal-output.md`〈營運腳本的狀態前綴〉）。要表達緊急，寫在訊息文字裡，不用前綴表達。
  if [[ "${verb}" == apply ]]; then
    echo "Info: ${svc} 目前用的是 .example 佔位值，不是真密碼"
    echo "Info: apply 出去的服務會連不上相依元件"
  fi
done

confirm() {
  # `|| true` 不可省：set -e 下 read 遇 EOF（無 tty、管線呼叫）回非零會直接中止腳本，
  # 走不到下一行的比對，取消訊息就永遠印不出來。放行後 ans 為空字串，比對照樣失敗。
  read -r -p "刪除 ${svc}（${track}），含資料卷、不可逆。輸入服務名確認：" ans || true
  # 取消走 Info: 而非 Error:（見 `~/.claude/rules/terminal-output.md`〈營運腳本的狀態前綴〉）。使用者主動放棄不是錯誤，
  # 且 Error: 要送 stderr，訊息就不會留在 stdout 上讓呼叫端看見。
  [[ "${ans}" == "${svc}" ]] || { echo "Info: 已取消（輸入的名稱與服務名不符）"; exit 1; }
}

# podman 開頭的軌（podman、podman-4060ti）都走 kube play：同一個服務依主機分軌，部署方式相同
case "${verb}:${track}" in
  apply:k8s)     kubectl apply -k "${here}/k8s" ;;
  apply:podman*) kubectl kustomize "${here}/${track}" | sudo podman kube play --replace - ;;
  # delete 直接吃 kustomize 產物，不做「不依賴機密檔」的防禦（單機工作流）
  delete:k8s)    confirm
                 kubectl delete -k "${here}/k8s" --ignore-not-found
                 # StatefulSet 的 volumeClaimTemplates PVC 不在 manifest 內（k8s 動態建立），
                 # 故不隨 delete 移除——這是 k8s 原生語意，需顯式清除。全 repo 僅 minio 命中。
                 # -n default：全部服務的 namespace 都是 default，與各軌 kustomization.yaml
                 # 的 namespace: 一致；吃 kubeconfig 當前 namespace 若 context 設過會靜默掃錯地方。
                 # 整條 pipeline 加容錯：這段只是刪除後的純提示，失敗不該讓已經成功的 delete
                 # 以非零退出，但也不能靜默吞掉——失敗時明講「請自行確認」。
                 { kubectl get pvc -n default -o name --ignore-not-found \
                     | { grep -- "-${svc}-" || true; } \
                     | { grep . && echo "Info: 上列為 StatefulSet 動態 PVC，delete 不移除；如需清除請執行 kubectl delete <名稱>" || true; } ; } \
                   || echo "Info: 無法列出 PVC，請自行以 kubectl get pvc 確認是否有 StatefulSet 動態卷殘留" ;;
  # podman 4.9.3：yaml 含 PVC 時 --force 會印 Volumes removed 後又 exit 125 no such volume，
  # volume 其實已刪，是假錯誤（用檔案參數 100% 重現，與 stdin 無關）。set -e 下不處理會誤中止。
  delete:podman*) confirm
                  # kustomize 先獨立跑：它失敗必須 fail loud，不能被下面的 || 一起吞掉。
                  # 原本 `kustomize | podman kube down || echo` 的 || 接的是整條 pipeline，
                  # pipefail 下 kustomize 失敗、機密檔缺席等任何原因都會被歸類成「已知假錯誤」
                  # 而吞掉，什麼都沒刪也回報成功（rc=0）。
                  manifest="$(kubectl kustomize "${here}/${track}")"
                  printf '%s\n' "${manifest}" | sudo podman kube down --force - \
                    || echo "Info: kube down 回非零；podman 4.9.3 對含 PVC 的 yaml 的已知假錯誤，volume 其實已刪，請以 podman volume ls 確認" ;;
  *) echo "Error: 用法 <apply|delete> <podman…|k8s>（podman… 指 podman 開頭的軌）" >&2; exit 1 ;;
esac
