# app-services 進度

還沒做的工程與維運待辦，一行一條。做完或不再成立就刪掉，紀錄寫在 commit 訊息，查 `git log`。有觸發條件的，句尾括號寫什麼時候做。

- minio 與 juicefs 改用 tag 釘映像：先在 images repo 替 `quay.io/flysangel/minio/minio` 同步版本 tag（mirror 現在只有 `latest`），再改用 `grep -rln 'digest: sha256:' services/` 列出的每份 `kustomization.yaml`
- 加一道檢查，比對 quay 兩處密碼：`secret.env` 的 `DB_PASSWORD`、`REDIS_PASSWORD`，與 `config.yaml` 的 `DB_URI`、`BUILDLOGS_REDIS` 內嵌的密碼。不一致時渲染與 `apply` 都成功，quay 起得來但連不上 DB；檢查不能印出值
- `task test` 改成每支測試檔都要印 `ALL <n> PASS`，沒印就算失敗。現在漏寫 `__main__` 的測試檔零輸出、回 0，`task test` 會把它當成通過
- 安裝 `yamllint`，對全部 `kustomization.yaml` 跑一次語法檢查
- 做 `vllm` 的 k8s 軌：拆成一個 pod 一個容器，並打通聊天。k8s overlay 要改寫 litellm `config.yaml` 的 `api_base`（現在寫 podman 名，k8s 軌的本地別名連不到）

**以下有觸發條件才做：**

- 把 `[[ … ]] && { …; }` 獨立語句改成 `if`，位置用 `git grep -n -E '^\s*\[\[.*\]\]\s*&&\s*\{' -- '*.sh'` 查。gmm 的 sync 會刪資料（要替這支腳本加 errexit 時）
- `bin/assert-images.sh` 補四件：任一軌 `kubectl kustomize` 失敗時要回非零（現在另一軌有產出就印 `Success`）；錯誤訊息分開講 kubectl 與 skopeo；完整參照的判準 `*/*:*` 會把 `localhost:5000/foo` 誤判成完整；抽取 `image:` 的正則可能誤抓 YAML 字串值裡形似 `image:` 的行（改這支檔時）
- `bin/assert-images.sh` 改進共用 push 護欄或其他自動流程（工作區共用護欄能掛各 repo 自己的檢查時）
- `bin/svc.sh` 先檢查動詞再補位機密檔（現在打錯動詞也會先建出 `secret.env`），補位出來的檔權限設成 0600（改這支檔時）
- `.gitignore` 刪掉沒有對應目錄的複數 `secrets/` 規則、`build/` 與「留給遷移期」的註解；單數 `secret/` 的放行規則改成也涵蓋子目錄（改這支檔時）
- `tests/test_manifest_diff.py` 合併重複的 helper（`run_diff` 與 `_run`），寫死的 `"python3"` 改成 `sys.executable`（改這支檔時）
- quay 三個容器沒有啟動順序控制：quay-app 先起來時連不上 redis，重啟一次後自癒。補 redis 就緒等待（上游改變啟動時序，或不再自癒時）
- 實測 ncb 第一次啟動要不要先放自簽憑證。現在是 `apply` 之後才補放，靠 `restartPolicy: always` 自癒；結果寫進 README（下次改 ncb 啟動流程時）
- 驗證 `bin/svc.sh` 對 podman `delete` 的 exit 125 容錯（podman 軌開始產出 `PersistentVolumeClaim` doc 時）
