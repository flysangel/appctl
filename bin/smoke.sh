#!/bin/bash
# smoke.sh — podman 軌煙測：apply、等 Running、探測、登入、再 apply、再探測、delete，逐個服務跑。
# 驅動：task smoke -- <svc>...
# 註記：逐項容錯，捨棄 -e；一個服務失敗記錄後繼續跑下一個，結尾才決定結束碼
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# delete 會刪掉卷：ncb 的卷是憑證，kadm 與 xadm-* 部署前要先確認網段隔離。這幾個要加旗標才跑
GUARDED=(ncb kadm xadm-lxqt xadm-xfce)
# 取 manifest 裡第一個工作負載的 pod 名：kind: Pod 是 metadata.name，其餘 kind 是 <名>-pod
POD_NAME_PY='
import sys, yaml
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get("kind") == "Pod":
        print(d["metadata"]["name"]); break
    if d and d.get("kind") in ("Deployment", "DaemonSet", "StatefulSet"):
        print(d["metadata"]["name"] + "-pod"); break
'

usage() {
  echo "Error: usage: smoke.sh [--allow-guarded] <svc>...（${GUARDED[*]} 要加 --allow-guarded 才跑）" >&2
}

is_guarded() {
  local svc="$1"
  local g

  for g in "${GUARDED[@]}"; do
    if [[ "${g}" == "${svc}" ]]; then
      return 0
    fi
  done
  return 1
}

wait_running() {
  local pod="$1"
  local timeout="$2"
  local waited=0
  local state

  while (( waited < timeout )); do
    state="$(sudo podman pod inspect "${pod}" --format '{{.State}}' 2>/dev/null || true)"
    if [[ "${state}" == "Running" ]]; then
      return 0
    fi
    sleep 2
    (( waited += 2 ))
  done
  echo "Error: ${pod} 等了 ${timeout} 秒還不是 Running（最後狀態：${state:-不存在}）" >&2
  return 1
}

http_code() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$1" || true
}

# 探一次；成功回 0。沒有探測的服務直接回 0
probe_once() {
  local svc="$1"
  local code health

  case "${svc}" in
    gmm)
      code="$(http_code http://127.0.0.1:8080/guacamole/)"
      [[ "${code}" == 2* || "${code}" == 3* ]]
      ;;
    grafana)
      health="$(curl -s --max-time 10 http://127.0.0.1:3000/api/health || true)"
      printf '%s' "${health}" | python3 -c 'import json,sys; sys.exit(json.load(sys.stdin).get("database") != "ok")' 2>/dev/null
      ;;
    quay)
      [[ "$(http_code http://127.0.0.1:8080/health/instance)" == 200 ]]
      ;;
    ncb)
      [[ "$(http_code http://127.0.0.1/)" == 301 ]]
      ;;
    *)
      return 0
      ;;
  esac
}

# pod 剛到 Running 時服務多半還沒聽埠，探測要重試到逾時
probe() {
  local svc="$1"
  local timeout="$2"
  local interval="${SMOKE_PROBE_INTERVAL:-5}"
  local waited=0

  case "${svc}" in
    gmm | grafana | quay | ncb) ;;
    *)
      echo "Info: ${svc} 沒有 HTTP 探測，只確認 pod 是 Running"
      return 0
      ;;
  esac
  until probe_once "${svc}"; do
    if (( waited >= timeout )); then
      echo "Error: ${svc} 的 HTTP 探測等了 ${timeout} 秒還沒通過，探測內容見 bin/smoke.sh 的 probe_once" >&2
      return 1
    fi
    sleep "${interval}"
    (( waited += interval > 0 ? interval : 1 ))
  done
  echo "Info: ${svc} HTTP 探測通過"
}

login() {
  local svc="$1"
  local conf="${ROOT}/services/gmm/sync.conf"
  local secret="${ROOT}/services/gmm/sync.secret.env"
  local admin pass token

  if [[ "${svc}" != gmm ]]; then
    return 0
  fi
  if [[ ! -f "${secret}" ]]; then
    echo "Info: 缺 ${secret}，略過 gmm 登入"
    return 0
  fi
  admin="$(sed -n 's/^GUACAMOLE_ADMIN=\([^[:space:]]*\).*/\1/p' "${conf}")"
  pass="$(sed -n 's/^GUACAMOLE_PASS=//p' "${secret}")"
  # 密碼經 stdin 餵給 curl（password@-），不進 argv
  token="$(printf '%s' "${pass}" | curl -s --max-time 10 -X POST http://127.0.0.1:8080/guacamole/api/tokens \
    --data-urlencode "username=${admin}" --data-urlencode "password@-" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("authToken",""))' 2>/dev/null || true)"
  [[ -n "${token}" ]] || { echo "Error: gmm 登入取不到 authToken" >&2; return 1; }
  echo "Info: gmm 登入成功"
}

check_once() {
  local svc="$1"
  local pod="$2"
  local timeout="$3"

  wait_running "${pod}" "${timeout}" || return 1
  probe "${svc}" "${timeout}" || return 1
  login "${svc}" || return 1
}

smoke_one() {
  local svc="$1"
  local pod timeout round

  pod="$(kubectl kustomize "${ROOT}/services/${svc}/podman" 2>/dev/null | python3 -c "${POD_NAME_PY}" || true)"
  [[ -n "${pod}" ]] || { echo "Error: ${svc} 渲染不出工作負載，取不到 pod 名" >&2; return 1; }
  # quay 第一次啟動要跑 migration，等比較久；SMOKE_TIMEOUT 可以覆寫
  timeout=120
  if [[ "${svc}" == quay ]]; then
    timeout=600
  fi
  timeout="${SMOKE_TIMEOUT:-${timeout}}"

  for round in 1 2; do
    echo "Info: ${svc} 第 ${round} 次 apply"
    # apply、等待或探測任一步失敗都清掉 pod，免得占著 hostPort 讓下一個服務連鎖失敗；卷留著
    if ! bash "${ROOT}/bin/svc.sh" "${svc}" apply podman || ! check_once "${svc}" "${pod}" "${timeout}"; then
      echo "Error: ${svc} 第 ${round} 次檢查失敗，清掉 ${pod}（卷留著）" >&2
      sudo podman pod rm -f "${pod}" >/dev/null 2>&1 || true
      return 1
    fi
  done

  # svc.sh 的 delete 會要求輸入服務名，並在刪完確認卷都不在
  bash "${ROOT}/bin/svc.sh" "${svc}" delete podman || return 1
  if sudo podman pod exists "${pod}"; then
    echo "Error: ${pod} 刪除後還在" >&2
    return 1
  fi
  echo "Success: ${svc} 煙測通過"
}

# main
allow_guarded=0
svcs=()
for arg in "$@"; do
  case "${arg}" in
    --allow-guarded) allow_guarded=1 ;;
    -*) usage; exit 1 ;;
    *) svcs+=("${arg}") ;;
  esac
done
if (( ${#svcs[@]} == 0 )); then
  usage
  exit 1
fi

fail=0
failed=()
for svc in "${svcs[@]}"; do
  if [[ ! -d "${ROOT}/services/${svc}/podman" ]]; then
    echo "Info: ${svc} 沒有 podman 軌，跳過"
    continue
  fi
  if is_guarded "${svc}" && (( allow_guarded == 0 )); then
    echo "Error: ${svc} 預設不跑：delete 會刪掉它的卷，要跑就加 --allow-guarded" >&2
    fail=1
    failed+=("${svc}")
    continue
  fi
  if ! smoke_one "${svc}"; then
    fail=1
    failed+=("${svc}")
  fi
done

if (( fail == 0 )); then
  echo "Success: 全部煙測通過"
else
  echo "Error: 煙測未通過：${failed[*]}" >&2
fi
exit "${fail}"
