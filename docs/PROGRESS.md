# app-services 進度

## 1. 目前狀態

- 標準驗證路徑：`task doctor` → `task lint` → `task test`。真環境驗證照 `AGENTS.md`〈完成定義〉的 L3，要部署服務。
- 綠的層：`task doctor`、`task lint`、`task test`。
- 已跑過 harness-adopt，`bin/check-docs.py` 與 `.githooks/` 是範本 repo 的新契約副本。
- 已套用 `~/.claude/rules/directories.md`，例外列在 `docs/ARCHITECTURE.md`〈目錄結構〉。
- 已套用 `~/.claude/rules/taskfile.md`〈分類原則〉，`bin/check-taskfile.py` 驗過。
- 未驗證：ncb 的憑證順序、`bin/svc.sh` 對 podman `delete` 的 exit 125 容錯、「拆 pod 留卷」的手動做法，見〈待辦〉。
- blocker：沒有。

## 2. 待辦

還沒做的工程與維運待辦，一行一條。做完或不再成立就刪掉，紀錄寫在 commit 訊息，查 `git log`。有觸發條件的，句尾括號寫什麼時候做。

- minio 與 juicefs 改用 tag 釘映像：先等 images 替 `minio/minio` 同步版本 tag（列在 images 的 `docs/PROGRESS.md`，mirror 現在只有 `latest`），再改用 `command grep -rln 'digest: sha256:' services/` 列出的每份 `kustomization.yaml`
- 加一道檢查，比對 quay 兩處的值：`secret.env` 的 `DB_PASSWORD`、`REDIS_PASSWORD` 對 `config.yaml` 的 `DB_URI`、`BUILDLOGS_REDIS`、`USER_EVENTS_REDIS`；`deployment.yaml` 的 `POSTGRES_USER`、`POSTGRES_DB` 對 `DB_URI` 的帳號與庫名。不一致時渲染與 `apply` 都成功，quay 起得來但連不上 DB；檢查不能印出值
- 決定 quay `secret.env` 的 `SECRET_KEY`、`DATABASE_SECRET_KEY` 去留：沒有容器引用它們，quay 讀的是 `config.yaml` 內的同名鍵，兩處各自漂移。拿掉，或讓上面那條檢查也比對這兩鍵
- `services/gmm/sync.sh` 把 `GUACAMOLE_PASS` 放進 curl 的 `--data-urlencode`、把 token 放進 `?token=`、把目標主機密碼放進 `--data-raw`，三者都進 argv，`ps` 看得到，違反 `AGENTS.md`〈不要做的事〉。改成 `Guacamole-Token` 標頭（映像內的 guacamole 1.6.0 支援），密碼與 JSON 改經 stdin 或 `curl -K -` 餵
- `services/gmm/sync.sh` 不看任何 API 回應，永遠以 0 結束。改成檢查每次 curl 的 HTTP 狀態碼，失敗計數後以非零結束；mapping 行解析失敗時中止、不進 cleanup；`sync.sh test` 加 `mapping.env` 存在與欄數檢查。做完改 `AGENTS.md`〈會刪資料的動作，先問使用者〉gmm 那條與 `README.md`〈營運同步（`task seed`）〉最後一條
- 補 `services/quay/sync.sh` 種第一位 superuser（等 `/health/instance` 回 200 後 `POST /api/v1/user/initialize`），讓 `task seed SVC=quay` 接手 `README.md`〈quay 的第一位 superuser〉的手動步驟
- `services/quay/k8s/ingress.yaml` 補 `nginx.ingress.kubernetes.io/proxy-body-size`：ingress-nginx 文件的預設 body 上限是 1m，推映像層會 413；infra-k8s 的 ingress addon 沒有全域設定
- quay 補 readinessProbe（`GET /health/instance`）：第一次啟動要跑 migration，`task apply` 回來時 quay 還沒就緒
- grafana 的 admin 密碼納入 `secret/secret.env`（`GF_SECURITY_ADMIN_PASSWORD`），不再用上游預設 admin/admin。做完改 `README.md`〈grafana 的第一次登入〉
- 決定 ncb 的 certbot 容器去留：`server_name` 是 `ncb.local`，ACME 不簽，手放的憑證沒有 renewal 設定檔，`renew` 迴圈等於沒做事。拿掉容器，或改成公網網域（公網 DNS 不在本 repo 範圍）。做完改 `docs/ARCHITECTURE.md`〈已知的結構性限制〉ncb 那條與 `README.md`〈ncb 的憑證〉
- 把 `AGENTS.md`〈真環境煙測要逐條手動跑〉的手動煙測清單寫成 podman 軌腳本（apply → 等 Running → 探測 → 登入 → 再 apply → delete → 確認 pod 與卷不在），一個服務失敗記錄後繼續跑下一個，結尾才決定退出碼；放進 Taskfile。做完改 `AGENTS.md`〈真環境煙測要逐條手動跑〉
- `task test` 改成每支測試檔都要印 `ALL <n> PASS`，沒印就算失敗。現在漏寫 `__main__` 的測試檔零輸出、回 0，`task test` 會把它當成通過
- `Taskfile.yaml` 的 `test` 改成自動收 `tests/test_*.py`，不逐行列檔。做完連同上一條，刪 `AGENTS.md`〈`task test` 回 0 不等於每支都跑了〉
- 安裝 `yamllint`，對全部 `kustomization.yaml` 跑一次語法檢查

**以下有觸發條件才做：**

- 把 `[[ … ]] && { …; }` 獨立語句改成 `if`，位置用 `git grep -n -E '^\s*\[\[.*\]\]\s*&&\s*\{' -- '*.sh'` 查。gmm 的 sync 會刪資料（要替這支腳本加 errexit 時）
- `bin/assert-images.sh` 補四件：任一軌 `kubectl kustomize` 失敗時要回非零（現在另一軌有產出就印 `Success`）；錯誤訊息分開講 kubectl 與 skopeo；完整參照的判準 `*/*:*` 會把 `localhost:5000/foo` 誤判成完整；抽取 `image:` 的正則可能誤抓 YAML 字串值裡形似 `image:` 的行（改這支檔時）
- 把 `bin/assert-images.sh` 的離線那一半（渲染產物的 `image:` 都是完整參照）拆成獨立檢查，掛進 `.githooks/pre-push`。整支掛不進去：它要逐一連 registry，而且 pre-push 展開的樹沒有 `secret.env`，有機密的服務在 kustomize 就失敗，要先補 `.example` 佔位檔。做完改 `AGENTS.md`〈`images:` 對不上時不會報錯〉與 `docs/ARCHITECTURE.md`〈設計取捨〉那條（push 時間能接受多十幾秒時）
- `bin/svc.sh` 先檢查動詞再補位機密檔（現在打錯動詞也會先建出 `secret.env`），補位出來的檔權限設成 0600（改這支檔時）
- `.gitignore` 刪掉沒有對應目錄的複數 `secrets/` 規則、`build/` 與「留給遷移期」的註解；單數 `secret/` 的放行規則改成也涵蓋子目錄（改這支檔時）
- `tests/test_manifest_diff.py` 合併重複的 helper（`run_diff` 與 `_run`），寫死的 `"python3"` 改成 `sys.executable`（改這支檔時）
- 同 pod 多容器的服務（quay、gmm、grafana）都沒有啟動順序控制：quay-app 先起來時連不上 redis、guacamole 等 mariadb 建 schema、grafana 連不上 3306，都靠重啟自癒。補就緒等待或 readinessProbe（上游改變啟動時序，或不再自癒時）
- gmm 接 AD 時開 LDAP：映像有 LDAP 擴充，在 `services/gmm/base/deployment.yaml` 加 `LDAP_*` env，`LDAP_SEARCH_BIND_PASSWORD` 走 `secret/secret.env`（要接 AD 時）
- gmm 的 guacd 一直以 `GUACD_LOG_LEVEL=debug` 跑，正式環境改 `info`（log 量成問題時）
- ncb 的 `default.conf` 對所有請求固定送 `Connection "upgrade"`，沒有 `proxy_buffering off`。改成 `map $http_upgrade` 決定 Connection 值，並補 `proxy_buffering off`（上游回 400，或串流回應卡住時）
- 實測 ncb 的憑證順序：先放自簽讓 nginx 起來、再 `certonly --webroot`、再 `nginx -s reload`。現在 README 寫的是這個順序但沒跑過；結果寫回 `README.md`〈ncb 的憑證〉（下次改 ncb 啟動流程時）
- 驗證 `bin/svc.sh` 對 podman `delete` 的 exit 125 容錯（podman 軌開始產出 `PersistentVolumeClaim` doc 時）
- 驗「拆 pod 留卷」的手動做法：`kubectl kustomize services/<svc>/podman | sudo podman kube down -` 不帶 `--force` 是否留下具名卷。成立就寫進 `README.md`〈拆除與暫停〉（有人要暫停又不能用 `pod stop` 時）
