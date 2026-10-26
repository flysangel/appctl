# app-services 進度

## 1. 目前狀態

- 標準驗證路徑：`task doctor` → `task lint` → `task test`，push 後看 GitHub Actions 的 `ci`。真環境驗證照 `AGENTS.md`〈完成定義〉的 L3。
- 綠的層：`task doctor`、`task lint`、`task test`。
- 控制面是 `appctl`（Go），podman 軌走 Quadlet。`bin/svc.sh`、`podman kube play` 路徑與 F9 以外的 Python 工具都已刪除。
- 已跑過 harness-adopt，`bin/check-docs.py` 與 `.githooks/` 是範本 repo 的新契約副本。
- 已套用 `~/.claude/rules/directories.md`，例外列在 `docs/ARCHITECTURE.md`〈目錄結構〉。
- 已套用 `~/.claude/rules/taskfile.md`〈分類原則〉，`bin/check-taskfile.py` 驗過。
- 未驗證：`appctl` 的部署動詞與服務子命令都還沒在真環境跑過；GitHub Actions 的 `ci` 與 `release` 還沒在 GitHub 上跑過。清單見〈待辦〉。
- 正式服務仍是舊版 `podman kube play` 部署的，還沒切換到 `appctl`。
- 正式 gmm 現在有範本建出的測試資料：user alice、bob，group LAB，兩條帶 `.X` 的連線。密碼在主工作樹的 `services/gmm/random.csv`。第一次用真清單 seed 時，這些測試資料會被當成「以外」刪掉。
- blocker：`appctl-quadlet` 分支推不上去。`pre-push` 跑的是主工作樹的舊 `bin/check-services.py`，它要 `.example`，分支已經沒有。CI 與預發布版延到 merge 後 push main 再做。

## 2. 進行中

- 分支 `appctl-quadlet`：控制面改成 Go 的 `appctl`，podman 軌從 `podman kube play` 改成 Quadlet。
- 做到哪：實作、離線測試與四大文件都完成。
- 下一步：先備份主工作樹 gitignored 的真值檔到 repo 外，再 merge 回 main。

## 3. 待辦

還沒做的工程與維運待辦，一行一條。做完或不再成立就刪掉，紀錄寫在 commit 訊息，查 `git log`。有觸發條件的，句尾括號寫什麼時候做。

**merge 後先做：**

- push main，確認 GitHub Actions 的 `ci` 第一次跑是綠的
- push `v0.1.0-rc.1` tag 發布預發布版，再用 `install.sh` 帶版本在部署主機裝一次，確認 sha256 驗證與 `appctl version`（正式 `v0.1.0` 等 rc 驗完再問使用者）
- `.githooks/pre-push` 的 `REPO_CHECKS` 改成呼叫 `appctl check`：merge 後它列的 `check-manifests.py`、`check-services.py` 已刪，那道檢查印 `Info:` 略過，push 前本機不擋，只靠 CI（改 F9 範本時一起改）

**真環境驗證（每項先問使用者）：**

- podman 軌在這台主機跑 `apply`、`stop`、`delete`：先 filebrowser，再多容器服務與 minio；確認 `stop` 後重開機會回來、`delete` 後卷與 secret 都不在
- `appctl smoke` 跑一輪 podman 軌的服務，貼出輸出
- podman 換 secret 時，容器還在用的 secret 能不能先刪再建（`apply` 改了機密檔時）
- `Tmpfs=`（`emptyDir` 加 `medium: Memory`）在 pod 內的行為，例如 `/dev/shm` 大小（有 podman 軌服務用到時）
- `appctl quay init` 對真 quay 確認「已初始化」的回應形狀與 quay 版本（現在依文件寫成 HTTP 400 加 `non-empty database`）
- `appctl ncb cert` 自簽：放進 `ncb-certbot` 卷後 nginx 起得來；ACME 要公網網域，等有網域時再驗
- k8s 軌在 infra-k8s 的叢集跑 `apply`、`delete`，以及 gmm、quay 子命令的自動 port-forward（每次 `delete` 前確認該服務在叢集上沒有正式資料）
- `appctl gmm seed` 對正式 gmm 跑兩次，確認第二次只覆寫、不新建（冪等）

**正式服務切換：**

- 正式服務從 `podman kube play` 切到 `appctl`：逐一 `sudo podman pod rm -f <pod>`（留卷），改好部署主機 clone 裡的真值檔，再 `sudo appctl apply`，確認 `VolumeName=` 接回舊資料；先拿一個有資料卷的服務試

**工程：**

- minio 與 juicefs 改用 tag 釘映像：先等 images 替 `minio/minio` 同步版本 tag（列在 images 的 `docs/PROGRESS.md`，mirror 現在只有 `latest`），再改用 `command grep -rln 'sha256:' services/` 列出的每份 manifest
- 決定 quay `secret.env` 的 `SECRET_KEY`、`DATABASE_SECRET_KEY` 去留：沒有容器引用它們，quay 讀的是 `config.yaml` 的同名鍵，而 demo 的 `config.yaml` 這兩鍵還是字面值 `${…}`。拿掉 `secret.env` 的兩鍵並在 `config.yaml` 填 demo 值，或讓 `appctl check` 的機密一致性也比對這兩鍵
- `services/quay/k8s/ingress.yaml` 補 `nginx.ingress.kubernetes.io/proxy-body-size`：ingress-nginx 文件的預設 body 上限是 1m，推映像層會 413；infra-k8s 的 ingress addon 沒有全域設定
- quay 補 readinessProbe（`GET /health/instance`）：第一次啟動要跑 migration，`appctl apply` 回來時 quay 還沒就緒
- grafana 的 admin 密碼納入 `secret/secret.env`（`GF_SECURITY_ADMIN_PASSWORD`），不再用上游預設 admin/admin。做完改 `README.md`〈grafana 的第一次登入〉
- 決定 ncb 的 certbot 容器去留：`server_name` 是 `ncb.local`，ACME 不簽，自簽憑證沒有 renewal 設定檔，`renew` 迴圈等於沒做事。拿掉容器，或改成公網網域（公網 DNS 不在本 repo 範圍）。做完改 `docs/ARCHITECTURE.md`〈已知的結構性限制〉ncb 那條與 `README.md`〈ncb 的憑證〉
- `appctl delete` 不靠這次的渲染與翻譯：manifest 壞掉時現在刪不掉已部署的服務，改成只靠 `deployed.json` 也能刪
- `appctl apply` 刪舊 podman secret 失敗時，錯誤被吸收而且從紀錄移除，真值留在主機上；改成留在紀錄或用 `podman secret exists` 確認
- 翻譯器替 Secret 的每個鍵都建 podman secret，包含沒人引用的 `QUAY_SUPERUSER_*`；改成只建單元引用到的鍵
- `appctl gmm` 的 `mapping.env` 補 CRLF 與行尾空白的測試：拿掉那行處理時，seed 會刪光重建全部連線
- `k8s.PortForward` 讀不到埠時先 Wait 後 Stop，kubectl 輸出超長行時可能卡住；補逾時與 ctx 取消的測試
- `.github/workflows/` 的 action 改釘 commit SHA
- k8s 軌 `delete` 遇到 StatefulSet 時仍印「含資料卷」，但動態 PVC 沒刪；而且先渲染再確認，manifest 壞掉時連確認都到不了。訊息改成依實際刪了什麼，確認移到渲染前
- podman 軌 `apply` 保留被拿掉的卷時不印 `Info:`；`deployed.json` 壞掉時的錯誤沒寫下一步。兩處都補訊息
- 翻譯器遇到兩個卷用同一個 `claimName` 時產生重複的 `.volume`；改成合併或報錯
- `appctl ncb cert` 的 `--email` 與網域以 `-` 開頭時沒擋，會被 certbot 當成旗標；ACME 的 stdout 被丟掉，失敗時看不到 certbot 的說明
- 安裝 `yamllint`，對全部 `kustomization.yaml` 跑一次語法檢查

**以下有觸發條件才做：**

- podman 軌是否也把 `systemctl`、`podman` 的輸出吞掉，跟 k8s 軌修過的問題一樣（下次改 `internal/podman` 時）
- k8s 軌 `kubectl` 成功時的 stderr warning 被丟掉，`run.Runner` 不回 stderr（下次改 `internal/run` 時）
- `appctl ncb cert --force` 換 key 後 cert 寫入失敗，會留下新 key 配舊 cert；改成兩個都寫好再 rename（下次改 `internal/ncb` 時）
- `setting.conf` 同一服務內重複的主機埠沒有檢查，現在只查跨服務撞埠（下次改 `internal/setting` 時）
- podman secret 名長度上限 253 沒有檢查（服務名、Secret 名或鍵名變長時）
- `gmm.ReadConf` 是通用的 env 解析器卻放在 `internal/gmm`，quay 也用；沒加引號的值遇到空白會截斷。抽成 `internal/envfile`（第三個子命令要讀 env 檔時）
- `appctl` 沒處理 SIGTERM，port-forward 的 kubectl 可能留著（有人用 cron 或 systemd 跑子命令時）
- `appctl check` 補一道：`secret/kustomization.yaml` 要有 `disableNameSuffixHash: true`，現在沒有程式擋（加新服務的機密時）
- `.gitignore` 刪掉沒有對應目錄的 `**/secrets/` 與 `build/` 規則（改這支檔時）
- `tests/test_manifest_diff.py` 合併重複的 helper（`run_diff` 與 `_run`），寫死的 `"python3"` 改成 `sys.executable`（改這支檔時）
- 同 pod 多容器的服務（quay、gmm、grafana）都沒有啟動順序控制：quay-app 先起來時連不上 redis、guacamole 等 mariadb 建 schema、grafana 連不上 3306，都靠重啟自癒。補就緒等待或 readinessProbe（上游改變啟動時序，或不再自癒時）
- gmm 接 AD 時開 LDAP：映像有 LDAP 擴充，在 `services/gmm/base/deployment.yaml` 加 `LDAP_*` env，`LDAP_SEARCH_BIND_PASSWORD` 走 `secret/secret.env`；翻譯器白名單照收（要接 AD 時）
- gmm 的 guacd 一直以 `GUACD_LOG_LEVEL=debug` 跑，正式環境改 `info`（log 量成問題時）
- ncb 的 `default.conf` 對所有請求固定送 `Connection "upgrade"`，沒有 `proxy_buffering off`。改成 `map $http_upgrade` 決定 Connection 值，並補 `proxy_buffering off`（上游回 400，或串流回應卡住時）
