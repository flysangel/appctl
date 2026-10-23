#!/bin/bash
# sync.sh — 透過 guacamole 的 REST API 建立連線設定（具冪等性，含 cleanup 與 nmap 存活驗證）。用法：bash sync.sh [sync|test]
# 設定讀取自同目錄的 sync.conf；對應表讀取自同目錄的 mapping.env（以 pipe 分隔，需恰好 10 欄）。屬 best-effort 營運工具。
# 每行的順序：nmap 定名 → 記進 map → user → group（取 gid）→ connection（取 cid）→ sharing profile → permission。
# gid 或 cid 取不到就跳過該行後半，但該行已記進 map，結尾的 cleanup 不會刪它。
# 全部 curl 都不看回應，沒有 errexit，過了前置檢查後一律以 0 結束；失敗只在下一步「取 id 失敗」時以 Info 浮現。
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mode="${1:-sync}"
for cmd in jq curl nmap; do command -v "$cmd" >/dev/null || { echo "Error: 缺 $cmd" >&2; exit 1; }; done
[[ -f "${here}/sync.conf" ]] || { echo "Error: ${here}/sync.conf 不存在（它進版控，從 repo 還原）" >&2; exit 1; }
# shellcheck source=/dev/null
source "${here}/sync.conf"
[[ -f "${here}/sync.secret.env" ]] || { echo "Error: ${here}/sync.secret.env 不存在（cp sync.secret.env.example sync.secret.env 後填）" >&2; exit 1; }
# shellcheck source=/dev/null
source "${here}/sync.secret.env"
: "${GUACAMOLE_SERVER:?sync.conf 缺 GUACAMOLE_SERVER}"; : "${GUACAMOLE_ADMIN:?}"; : "${GUACAMOLE_PASS:?sync.secret.env 缺 GUACAMOLE_PASS}"
: "${DATA_SOURCE:=mysql}"; : "${PASS_LENGTH:=12}"
API="${GUACAMOLE_SERVER}/api/session/data/${DATA_SOURCE}"

get_token() {
  token=$(curl -s -X POST "${GUACAMOLE_SERVER}/api/tokens" \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode "username=${GUACAMOLE_ADMIN}" --data-urlencode "password=${GUACAMOLE_PASS}" \
    | jq -r '.authToken')
  [[ -n "${token}" && "${token}" != null ]] || { echo "Error: 取 token 失敗（檢查 sync.conf 的 SERVER/ADMIN/PASS）" >&2; exit 1; }
}
users_json()  { curl -s "${API}/users?token=${token}"; }
groups_json() { curl -s "${API}/connectionGroups?token=${token}"; }
conns_json()  { curl -s "${API}/connections?token=${token}"; }
id_by_name()  { jq -r --arg n "$1" 'to_entries[]?|.value|select(.name==$n)|.identifier' | head -1; }

get_token
if [[ "${mode}" == "test" ]]; then echo "Success: token ok"; exit 0; fi
[[ -f "${here}/mapping.env" ]] || { echo "Error: ${here}/mapping.env 不存在（cp mapping.env.example mapping.env 後填）" >&2; exit 1; }

declare -A map_users map_groups map_conns

while IFS= read -r line || [[ -n "${line}" ]]; do
  [[ -z "${line}" || "${line}" == \#* ]] && continue
  [[ "$(awk -F'|' '{print NF}' <<<"${line}")" -eq 10 ]] || { echo "Error: mapping 行非 10 欄: ${line}" >&2; continue; }
  IFS='|' read -r userAccount userPassword connGroup connProtocol connIP connPort connAccount connPassword connDomain connAlias <<<"${line}"
  [[ -z "${userAccount}" ]] && continue
  if [[ -z "${connAlias}" ]]; then connName="【${connDomain}】${connAccount}@${connIP}"
  else connName="【${connDomain}】${connAccount}@${connIP}【${connAlias}】"; fi
  # 以 nmap 驗證目標是否存活：若目標 port 未回應 open，則在連線名稱加上 .X 後綴（.X 是連線識別的一部分）
  if ! nmap -Pn -T4 --host-timeout 10s --max-retries 1 -p "${connPort}" "${connIP}" 2>/dev/null \
       | awk -v p="${connPort}" '$1 ~ "^"p"/" && $2=="open"' | grep -q .; then
    connName="${connName}.X"
  fi
  map_users["${userAccount}"]=1; map_groups["${connGroup}"]=1; map_conns["${connName}"]=1

  if ! users_json | jq -e --arg u "${userAccount}" 'has($u)' >/dev/null; then
    if [[ -z "${userPassword}" ]]; then
      userPassword=$(tr -dc 'a-z0-9' </dev/urandom | head -c "${PASS_LENGTH}")
      [[ -f "${here}/random.csv" ]] || echo "User,Password" >"${here}/random.csv"
      echo "${userAccount},${userPassword}" >>"${here}/random.csv"
      chmod 600 "${here}/random.csv" 2>/dev/null || true
    fi
    curl -s -X POST "${API}/users?token=${token}" -H 'Content-Type: application/json' \
      --data-raw "$(jq -n --arg u "${userAccount}" --arg p "${userPassword}" '{username:$u,password:$p,attributes:{}}')" >/dev/null
    echo "Info: create user ${userAccount}"
  fi

  gid=$(groups_json | id_by_name "${connGroup}")
  if [[ -z "${gid}" ]]; then
    curl -s -X POST "${API}/connectionGroups?token=${token}" -H 'Content-Type: application/json' \
      --data-raw "$(jq -n --arg n "${connGroup}" '{parentIdentifier:"ROOT",name:$n,type:"ORGANIZATIONAL",attributes:{}}')" >/dev/null
    gid=$(groups_json | id_by_name "${connGroup}")
    echo "Info: create group ${connGroup}"
  fi
  [[ -z "${gid}" ]] && { echo "Info: group ${connGroup} 取 id 失敗，跳過此行"; continue; }

  # 協定參數寫死：rdp 帶 security nla 與 ignore-cert；ssh 帶配色與字級；其他值（含打錯字）走 fallback 只送四欄。connProtocol 不驗證。
  case "${connProtocol}" in
    rdp) params=$(jq -n --arg h "$connIP" --arg p "$connPort" --arg u "$connAccount" --arg w "$connPassword" --arg d "$connDomain" '{hostname:$h,port:$p,username:$u,password:$w,domain:$d,security:"nla","ignore-cert":"true"}') ;;
    ssh) params=$(jq -n --arg h "$connIP" --arg p "$connPort" --arg u "$connAccount" --arg w "$connPassword" '{hostname:$h,port:$p,username:$u,password:$w,"color-scheme":"black-white","font-size":"14"}') ;;
    *)   params=$(jq -n --arg h "$connIP" --arg p "$connPort" --arg u "$connAccount" --arg w "$connPassword" '{hostname:$h,port:$p,username:$u,password:$w}') ;;
  esac
  cid=$(conns_json | id_by_name "${connName}")
  payload=$(jq -n --arg parent "${gid}" --arg n "${connName}" --arg proto "${connProtocol}" --argjson params "${params}" \
    '{parentIdentifier:$parent,name:$n,protocol:$proto,parameters:$params,attributes:{"max-connections":"","max-connections-per-user":""}}')
  if [[ -n "${cid}" ]]; then
    curl -s -X PUT "${API}/connections/${cid}?token=${token}" -H 'Content-Type: application/json' --data-raw "${payload}" >/dev/null
  else
    curl -s -X POST "${API}/connections?token=${token}" -H 'Content-Type: application/json' --data-raw "${payload}" >/dev/null
    cid=$(conns_json | id_by_name "${connName}")
  fi
  echo "Info: sync connection ${connName}"
  [[ -z "${cid}" ]] && { echo "Info: connection ${connName} 無 id，跳過 sharing/permission"; continue; }

  for pr in "ro:true" "rw:"; do
    pn="${pr%%:*}"; pv="${pr#*:}"
    exists=$(curl -s "${API}/sharingProfiles?token=${token}" | jq -r --arg n "$pn" --arg c "${cid}" 'to_entries[]?|.value|select(.name==$n and .primaryConnectionIdentifier==$c)|.identifier' | head -1)
    [[ -n "${exists}" ]] && continue
    curl -s -X POST "${API}/sharingProfiles?token=${token}" -H 'Content-Type: application/json' \
      --data-raw "$(jq -n --arg c "${cid}" --arg n "$pn" --arg ro "$pv" '{primaryConnectionIdentifier:$c,name:$n,parameters:{"read-only":$ro},attributes:{}}')" >/dev/null
  done

  curl -s -X PATCH "${API}/users/${userAccount}/permissions?token=${token}" -H 'Content-Type: application/json' \
    --data-raw "[{\"op\":\"add\",\"path\":\"/connectionGroupPermissions/${gid}\",\"value\":\"READ\"},{\"op\":\"add\",\"path\":\"/connectionPermissions/${cid}\",\"value\":\"READ\"}]" >/dev/null
done <"${here}/mapping.env"

# 清除 mapping.env 之外多餘的項目（保留 admin 帳號與 ROOT 群組）
for u in $(users_json | jq -r 'keys[]'); do
  [[ "${u}" == "${GUACAMOLE_ADMIN}" ]] && continue
  [[ -z "${map_users[${u}]:-}" ]] && { curl -s -X DELETE "${API}/users/${u}?token=${token}" >/dev/null; echo "Info: delete user ${u}"; }
done
groups_json | jq -r 'to_entries[]?|.value|"\(.name)\t\(.identifier)"' | while IFS=$'\t' read -r gn gi; do
  [[ "${gn}" == "ROOT" ]] && continue
  [[ -z "${map_groups[${gn}]:-}" ]] && { curl -s -X DELETE "${API}/connectionGroups/${gi}?token=${token}" >/dev/null; echo "Info: delete group ${gn}"; }
done
conns_json | jq -r 'to_entries[]?|.value|"\(.name)\t\(.identifier)"' | while IFS=$'\t' read -r cn ci; do
  [[ -z "${map_conns[${cn}]:-}" ]] && { curl -s -X DELETE "${API}/connections/${ci}?token=${token}" >/dev/null; echo "Info: delete connection ${cn}"; }
done
echo "Info: sync 完成"
