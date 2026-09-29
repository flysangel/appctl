#!/bin/bash
# sync.sh — 種 open-webui 的第一位 admin，寫入每個模型的系統提示詞、常駐橫幅與語音開關，皆具冪等性。用法：bash sync.sh [sync|test]
# 註記：逐項容錯，捨棄 -e；每個模型、橫幅與語音開關的寫入失敗都累積在 fail，不擋其餘項目
# test 模式只驗證，不建帳號也不改設定。
# 驗證四項：admin 能 signin、每個模型的系統提示詞與 system-prompt.txt 相同、橫幅與 banner.txt 相同、語音開關與 VOICE_ENABLED 相同。
# signin／signup 成功時的 HTTP 狀態碼依 open-webui v0.11.0 實作為 200（未來若 open-webui 改為 201，需同步更新此處判斷）。
# 設定讀取自同目錄的 sync.conf。屬 best-effort 營運工具。token／密碼絕不可回顯輸出（因此勿加上 set -x）。
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mode="${1:-sync}"
case "${mode}" in
  sync|test) ;;
  *) echo "Error: 用法 bash sync.sh [sync|test]" >&2; exit 1 ;;
esac
for cmd in jq curl; do command -v "${cmd}" >/dev/null || { echo "Error: 缺 ${cmd}" >&2; exit 1; }; done
[[ -f "${here}/sync.conf" ]] || { echo "Error: ${here}/sync.conf 不存在（cp sync.conf.example sync.conf 後填）" >&2; exit 1; }
# shellcheck source=/dev/null
source "${here}/sync.conf"
[[ -f "${here}/sync.secret.env" ]] || { echo "Error: ${here}/sync.secret.env 不存在（cp sync.secret.env.example sync.secret.env 後填）" >&2; exit 1; }
# shellcheck source=/dev/null
source "${here}/sync.secret.env"
[[ -s "${here}/system-prompt.txt" ]] || { echo "Error: ${here}/system-prompt.txt 不存在或是空檔" >&2; exit 1; }
[[ -s "${here}/banner.txt" ]] || { echo "Error: ${here}/banner.txt 不存在或是空檔" >&2; exit 1; }
: "${OPENWEBUI_URL:?sync.conf 缺 OPENWEBUI_URL}"; : "${ADMIN_EMAIL:?}"; : "${ADMIN_PASSWORD:?sync.secret.env 缺 ADMIN_PASSWORD}"
: "${ADMIN_NAME:=admin}"
: "${VOICE_ENABLED:=false}"
case "${VOICE_ENABLED}" in
  true|false) ;;
  *) echo "Error: sync.conf 的 VOICE_ENABLED 只能是 true 或 false（目前是 ${VOICE_ENABLED}）" >&2; exit 1 ;;
esac
# 去除結尾斜線，避免組出 ${OPENWEBUI_URL}/api 時變成雙斜線 //api
OPENWEBUI_URL="${OPENWEBUI_URL%/}"
system_prompt="$(<"${here}/system-prompt.txt")"
banner_text="$(<"${here}/banner.txt")"

tmp="$(mktemp)" || { echo "Error: mktemp 失敗" >&2; exit 1; }
trap 'rm -f "${tmp}"' EXIT

# 送出 POST JSON 請求：HTTP 狀態碼經 stdout 回傳，回應內容則寫入 ${tmp}；token／密碼不會被印出
# 參數：$1=path  $2=json
post() {
  curl -s -o "${tmp}" -w '%{http_code}' -X POST "${OPENWEBUI_URL}$1" \
    -H 'Content-Type: application/json' --data-raw "$2"
}
has_token() { [[ -n "$(jq -r '.token // empty' "${tmp}" 2>/dev/null)" ]]; }

# 帶 admin token 呼叫 API，回傳方式同 post()。ADMIN_TOKEN 由主流程在 signin／signup 後設定
# 參數：$1=GET|POST  $2=path  $3=json（選填）
api() {
  local args=(-s -o "${tmp}" -w '%{http_code}' -X "$1" "${OPENWEBUI_URL}$2" -H "Authorization: Bearer ${ADMIN_TOKEN}")
  if [[ "$#" -ge 3 ]]; then
    args+=(-H 'Content-Type: application/json' --data-raw "$3")
  fi
  curl "${args[@]}"
}

# main
signin_json="$(jq -n --arg e "${ADMIN_EMAIL}" --arg p "${ADMIN_PASSWORD}" '{email:$e,password:$p}')"
signup_json="$(jq -n --arg n "${ADMIN_NAME}" --arg e "${ADMIN_EMAIL}" --arg p "${ADMIN_PASSWORD}" '{name:$n,email:$e,password:$p}')"

# 1）先嘗試 signin：若 admin 帳號已存在且密碼相符，即視為已完成，不重建（冪等）
code="$(post /api/v1/auths/signin "${signin_json}")"
if [[ "${code}" == "200" ]] && has_token; then
  echo "Info: admin 已存在（signin ok）：${ADMIN_EMAIL}"
elif [[ "${mode}" == "test" ]]; then
  echo "Error: admin 尚未建立或憑證不符（signin HTTP ${code}）；test 模式不建立" >&2
  exit 1
else
  # 2）signin 不通過時嘗試 signup：建立第一位 admin（適用於實例尚未對外開放註冊的時機）
  code="$(post /api/v1/auths/signup "${signup_json}")"
  if [[ "${code}" == "200" ]] && has_token; then
    echo "Info: 首位 admin 種子成功（signup）：${ADMIN_EMAIL}"
  else
    # signin 與 signup 皆失敗 → 可能是實例已被他人搶先建立首位 admin、signup 功能被關閉、或憑證不符 → 明確報錯而非悄悄放行（用於偵測「被搶註」的情境）
    msg="$(jq -r '.detail // .message // empty' "${tmp}" 2>/dev/null)"
    echo "Error: 無法種子 admin（signin 不符且 signup HTTP ${code}：${msg:-無訊息}）。" >&2
    echo "       可能：實例已被他人搶註首 admin（安全！）、ENABLE_SIGNUP 擋下 bootstrap、或憑證不符。" >&2
    exit 1
  fi
fi
# 全域：api() 讀這個值帶進每個請求
ADMIN_TOKEN="$(jq -r '.token' "${tmp}")"

# 3）替每個模型寫入系統提示詞。寫在模型紀錄的 params.system，open-webui 送出前會插成第一則 system 訊息；
#    全域的 DEFAULT_MODEL_PARAMS 行不通：v0.11.0 只把其中的 system 記進 metadata，不送給模型（2026-09-18 抓包實測）
code="$(api GET /api/models)"
[[ "${code}" == "200" ]] || { echo "Error: 讀不到模型清單（HTTP ${code}）" >&2; exit 1; }
# arena-model 是 open-webui 內建的對戰模式，隨機轉給其他模型，本身不送模型請求，不必寫提示詞
model_ids="$(jq -r '.data[] | select(.owned_by != "arena") | .id' "${tmp}")"
[[ -n "${model_ids}" ]] || { echo "Error: open-webui 沒看到任何模型，先確認到 litellm 的連線" >&2; exit 1; }

fail=0
while read -r model_id; do
  id_query="$(jq -rn --arg v "${model_id}" '$v | @uri')"
  code="$(api GET "/api/v1/models/model?id=${id_query}")"
  if [[ "${code}" == "200" && "$(jq -r '.params.system // empty' "${tmp}")" == "${system_prompt}" ]]; then
    echo "Info: 系統提示詞已是最新：${model_id}"
    continue
  fi
  if [[ "${mode}" == "test" ]]; then
    echo "Error: 系統提示詞與 system-prompt.txt 不同：${model_id}（test 模式不寫入）" >&2
    fail=1
    continue
  fi
  # 已有紀錄就只換 params.system，其餘欄位原樣送回，存取設定維持不變。
  # access_grants 不能省：v0.11.0 的更新端點會重新驗證表單，省略時變成 null 而回 HTTP 500（2026-09-18 實測）
  case "${code}" in
    200)
      body="$(jq --arg s "${system_prompt}" '{id, base_model_id, name, meta, is_active, access_grants: (.access_grants // []), params: ((.params // {}) + {system: $s})}' "${tmp}")"
      code="$(api POST /api/v1/models/model/update "${body}")"
      ;;
    404)
      body="$(jq -n --arg id "${model_id}" --arg s "${system_prompt}" '{id: $id, base_model_id: null, name: $id, meta: {}, params: {system: $s}}')"
      code="$(api POST /api/v1/models/create "${body}")"
      ;;
  esac
  if [[ "${code}" == "200" ]]; then
    echo "Info: 已寫入系統提示詞：${model_id}"
  else
    echo "Error: 寫入系統提示詞失敗：${model_id}（HTTP ${code}）" >&2
    fail=1
  fi
done <<<"${model_ids}"

# 4）常駐橫幅告知 prompt 會被保存。橫幅是 PersistentConfig，env 只在全新資料庫當種子（open-webui v0.11.0），所以一律由這裡寫入。
#    只管 id 為 banner_id 的那一則，admin 另加的橫幅原樣保留。橫幅規格只在 banner_json 定義一次，比對與寫入共用
banner_id="prompt-retention"
banner_json="$(jq -n --arg id "${banner_id}" --arg c "${banner_text}" '{id: $id, type: "info", title: "", content: $c, dismissible: false}')"
code="$(api GET /api/v1/configs/banners)"
[[ "${code}" == "200" ]] || { echo "Error: 讀不到橫幅設定（HTTP ${code}）" >&2; exit 1; }
if [[ "$(jq -r --argjson b "${banner_json}" 'any(.[]; del(.timestamp) == $b)' "${tmp}")" == "true" ]]; then
  echo "Info: 橫幅已是最新"
elif [[ "${mode}" == "test" ]]; then
  echo "Error: 橫幅與 banner.txt 不同或不存在（test 模式不寫入）" >&2
  fail=1
else
  body="$(jq --argjson b "${banner_json}" --argjson t "$(date +%s)" \
    '{banners: ([.[] | select(.id != $b.id)] + [$b + {timestamp: $t}])}' "${tmp}")"
  code="$(api POST /api/v1/configs/banners "${body}")"
  if [[ "${code}" == "200" ]]; then
    echo "Info: 已寫入橫幅"
  else
    echo "Error: 寫入橫幅失敗（HTTP ${code}）" >&2
    fail=1
  fi
fi

# 5）語音開關：預設使用者權限的語音輸入（stt）、朗讀（tts）、通話（call）三項照 VOICE_ENABLED 設。
#    實驗室不提供語音：語音轉文字放進去之後，卡上沒有空間給朗讀。open-webui 內建 CPU 的 whisper 與瀏覽器朗讀，不關的話按鈕照樣出現。
#    這組權限在 v0.11.0 是 PersistentConfig；admin 不受權限限制，仍看得到也用得了語音
voice_keys='["stt", "tts", "call"]'
code="$(api GET /api/v1/users/default/permissions)"
[[ "${code}" == "200" ]] || { echo "Error: 讀不到預設使用者權限（HTTP ${code}）" >&2; exit 1; }
if [[ "$(jq -r --argjson k "${voice_keys}" --argjson v "${VOICE_ENABLED}" '[.chat[$k[]]] | all(. == $v)' "${tmp}")" == "true" ]]; then
  echo "Info: 語音開關已是 ${VOICE_ENABLED}"
elif [[ "${mode}" == "test" ]]; then
  echo "Error: 語音開關與 VOICE_ENABLED=${VOICE_ENABLED} 不同（test 模式不寫入）" >&2
  fail=1
else
  body="$(jq --argjson k "${voice_keys}" --argjson v "${VOICE_ENABLED}" 'reduce $k[] as $x (.; .chat[$x] = $v)' "${tmp}")"
  code="$(api POST /api/v1/users/default/permissions "${body}")"
  if [[ "${code}" == "200" ]]; then
    echo "Info: 已把語音開關設成 ${VOICE_ENABLED}"
  else
    echo "Error: 寫入預設使用者權限失敗（HTTP ${code}）" >&2
    fail=1
  fi
fi

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "Success: admin、每個模型的系統提示詞、常駐橫幅、語音開關都已就緒（${mode}）"
