#!/bin/bash
# install.sh — 從 GitHub Release 下載 appctl，驗 sha256 後裝到 /usr/local/bin
# 驅動：curl -sSLf https://raw.githubusercontent.com/flysangel/appctl/main/install.sh | sudo bash [-s -- <版本>]
# 註記：APPCTL_DOWNLOAD_BASE 與 APPCTL_INSTALL_DIR 給測試用；設了 APPCTL_INSTALL_DIR 就略過 root 檢查，因為目的目錄由呼叫端負責
set -euo pipefail

ASSET="appctl-linux-amd64"
DOWNLOAD_BASE="${APPCTL_DOWNLOAD_BASE:-https://github.com/flysangel/appctl/releases}"
INSTALL_DIR="${APPCTL_INSTALL_DIR:-/usr/local/bin}"
# 給 trap 用
WORK_DIR=""

cleanup() {
  if [[ -n "${WORK_DIR}" ]]; then
    rm -rf "${WORK_DIR}"
  fi
}

# main
version="${1:-}"

if [[ -n "${version}" ]]; then
  [[ "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || { echo "Error: 版本格式不對：${version}（要像 v0.1.0 或 v0.1.0-rc.1）" >&2; exit 1; }
fi

[[ "$(uname -m)" == "x86_64" ]] || { echo "Error: 只提供 linux amd64 的 binary，目前是 $(uname -m)" >&2; exit 1; }

if [[ -z "${APPCTL_INSTALL_DIR:-}" ]]; then
  [[ "${EUID}" -eq 0 ]] || { echo "Error: 要用 root 執行，例如 curl -sSLf <網址> | sudo bash" >&2; exit 1; }
fi

for cmd in curl sha256sum install mktemp; do
  command -v "${cmd}" >/dev/null 2>&1 || { echo "Error: 找不到指令 ${cmd}" >&2; exit 1; }
done

if [[ -n "${version}" ]]; then
  url_base="${DOWNLOAD_BASE}/download/${version}"
else
  url_base="${DOWNLOAD_BASE}/latest/download"
fi

WORK_DIR="$(mktemp -d)"
trap cleanup EXIT

echo "Info: 下載 ${url_base}/${ASSET}"
curl -fsSL -o "${WORK_DIR}/${ASSET}" "${url_base}/${ASSET}" || { echo "Error: 下載 ${ASSET} 失敗" >&2; exit 1; }
curl -fsSL -o "${WORK_DIR}/${ASSET}.sha256" "${url_base}/${ASSET}.sha256" || { echo "Error: 下載 ${ASSET}.sha256 失敗" >&2; exit 1; }

(cd "${WORK_DIR}" && sha256sum -c "${ASSET}.sha256" >/dev/null 2>&1) || { echo "Error: sha256 驗證失敗，不安裝" >&2; exit 1; }

# 先寫同目錄的暫名再 mv，讓目的檔不會停在寫到一半的狀態
install -d "${INSTALL_DIR}"
install -m 0755 "${WORK_DIR}/${ASSET}" "${INSTALL_DIR}/.appctl.new"
mv -f "${INSTALL_DIR}/.appctl.new" "${INSTALL_DIR}/appctl"

echo "Success: 已安裝 ${INSTALL_DIR}/appctl"
