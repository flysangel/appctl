# app-services 進度

## 1. 目前狀態

- 標準驗證路徑：`task doctor` → `task lint` → `task test`。真環境驗證照 `AGENTS.md`〈完成定義〉的 L3，要部署服務。
- 綠的層：`task doctor`、`task lint`、`task test`。
- 已跑過 harness-adopt，`bin/check-docs.py` 與 `.githooks/` 是範本 repo 的新契約副本。
- 已套用 `~/.claude/rules/directories.md`，例外列在 `docs/ARCHITECTURE.md`〈目錄結構〉。
- 已套用 `~/.claude/rules/taskfile.md`〈分類原則〉，`bin/check-taskfile.py` 驗過。
- 未驗證：ncb 的憑證順序、「拆 pod 留卷」的手動做法，見〈待辦〉。podman `delete` 補 PVC 殼的修法只在 rootless 實測過，rootful 還沒跑。`services/gmm/sync.py` 與 `task smoke` 還沒在真環境跑過。
- 主工作樹的 quay `secret.env` 和 `.example` 內容相同，`task lint:secret SVC=quay` 判為佔位值。
- blocker：沒有。

## 2. 進行中

- 這個分支做什麼：保留 kustomize，用 Python 補離線檢查、gmm 同步與煙測，修 podman delete 刪卷。計畫與設計只存在主工作樹的 `docs/superpowers/`。
- 做到哪：計畫 Task 1 到 8 完成，整個分支的 code review 跑過，必要修正已 commit。
- 下一步：使用者決定要不要跑三個真環境驗證（gmm seed、rootful delete、`task smoke`），再 merge 回 main。

## 3. 待辦

還沒做的工程與維運待辦，一行一條。做完或不再成立就刪掉，紀錄寫在 commit 訊息，查 `git log`。有觸發條件的，句尾括號寫什麼時候做。

- minio 與 juicefs 改用 tag 釘映像：先等 images 替 `minio/minio` 同步版本 tag（列在 images 的 `docs/PROGRESS.md`，mirror 現在只有 `latest`），再改用 `command grep -rln 'sha256:' services/` 列出的每份 manifest
- 決定 quay `secret.env` 的 `SECRET_KEY`、`DATABASE_SECRET_KEY` 去留：沒有容器引用它們，quay 讀的是 `config.yaml` 內的同名鍵，兩處各自漂移。拿掉，或讓 `bin/check-secret.py` 也比對這兩鍵
- 補 `services/quay/sync.sh` 種第一位 superuser（等 `/health/instance` 回 200 後 `POST /api/v1/user/initialize`），讓 `task seed SVC=quay` 接手 `README.md`〈quay 的第一位 superuser〉的手動步驟
- `services/quay/k8s/ingress.yaml` 補 `nginx.ingress.kubernetes.io/proxy-body-size`：ingress-nginx 文件的預設 body 上限是 1m，推映像層會 413；infra-k8s 的 ingress addon 沒有全域設定
- quay 補 readinessProbe（`GET /health/instance`）：第一次啟動要跑 migration，`task apply` 回來時 quay 還沒就緒
- grafana 的 admin 密碼納入 `secret/secret.env`（`GF_SECURITY_ADMIN_PASSWORD`），不再用上游預設 admin/admin。做完改 `README.md`〈grafana 的第一次登入〉
- 決定 ncb 的 certbot 容器去留：`server_name` 是 `ncb.local`，ACME 不簽，手放的憑證沒有 renewal 設定檔，`renew` 迴圈等於沒做事。拿掉容器，或改成公網網域（公網 DNS 不在本 repo 範圍）。做完改 `docs/ARCHITECTURE.md`〈已知的結構性限制〉ncb 那條與 `README.md`〈ncb 的憑證〉
- 安裝 `yamllint`，對全部 `kustomization.yaml` 跑一次語法檢查

**以下有觸發條件才做：**

- 把 `[[ … ]] && { …; }` 獨立語句改成 `if`，位置用 `git grep -n -E '^\s*\[\[.*\]\]\s*&&\s*\{' -- '*.sh'` 查（要替某支腳本加 errexit 時）
- `bin/assert-images.sh` 補兩件：任一軌 `kubectl kustomize` 失敗時要回非零（現在另一軌有產出就印 `Success`）；錯誤訊息分開講 kubectl 與 skopeo（改這支檔時）
- `.gitignore` 刪掉沒有對應目錄的複數 `secrets/` 規則、`build/` 與「留給遷移期」的註解；單數 `secret/` 的放行規則改成也涵蓋子目錄（改這支檔時）
- `tests/test_manifest_diff.py` 合併重複的 helper（`run_diff` 與 `_run`），寫死的 `"python3"` 改成 `sys.executable`（改這支檔時）
- 同 pod 多容器的服務（quay、gmm、grafana）都沒有啟動順序控制：quay-app 先起來時連不上 redis、guacamole 等 mariadb 建 schema、grafana 連不上 3306，都靠重啟自癒。補就緒等待或 readinessProbe（上游改變啟動時序，或不再自癒時）
- gmm 接 AD 時開 LDAP：映像有 LDAP 擴充，在 `services/gmm/base/deployment.yaml` 加 `LDAP_*` env，`LDAP_SEARCH_BIND_PASSWORD` 走 `secret/secret.env`（要接 AD 時）
- gmm 的 guacd 一直以 `GUACD_LOG_LEVEL=debug` 跑，正式環境改 `info`（log 量成問題時）
- ncb 的 `default.conf` 對所有請求固定送 `Connection "upgrade"`，沒有 `proxy_buffering off`。改成 `map $http_upgrade` 決定 Connection 值，並補 `proxy_buffering off`（上游回 400，或串流回應卡住時）
- 實測 ncb 的憑證順序：先放自簽讓 nginx 起來、再 `certonly --webroot`、再 `nginx -s reload`。現在 README 寫的是這個順序但沒跑過；結果寫回 `README.md`〈ncb 的憑證〉（下次改 ncb 啟動流程時）
- 驗「拆 pod 留卷」的手動做法：`kubectl kustomize services/<svc>/podman | sudo podman kube down -` 不帶 `--force` 是否留下具名卷。成立就寫進 `README.md`〈拆除與暫停〉（有人要暫停又不能用 `pod stop` 時）
