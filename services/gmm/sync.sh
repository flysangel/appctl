#!/bin/bash
# sync.sh — gmm 營運同步的入口，轉派給同目錄的 sync.py。用法：bash sync.sh [sync|test]
# 驅動：task seed SVC=gmm、task check:seed SVC=gmm
# 註記：Taskfile 派工到 services/<svc>/sync.sh，ls services/*/sync.sh 也拿它當營運工具清單，所以入口留在 bash
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

exec python3 "${here}/sync.py" "$@"
