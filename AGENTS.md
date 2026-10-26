# AGENTS.md

寫給 AI agent 的 app-services 作業指引。只收 app-services 專屬、而且跨檔的事。只跟單一檔案有關的坑寫在該檔註解，打開那個檔就讀得到。

## 1. 30 秒定位

- 這是容器服務的部署集，加上 Go 寫的部署控制工具 `appctl`（`cmd/appctl/`、`internal/`）。服務的正本是 `services/` 底下的 kustomize manifest。
- `appctl` 內嵌 kustomize 渲染。k8s 軌把產物交給 `kubectl`；podman 軌把產物翻成 Quadlet 單元（`internal/quadlet`），交給 systemd。
- 改結構前先讀 `docs/ARCHITECTURE.md`〈依賴與資料流〉。
- CI 是 GitHub Actions（`.github/workflows/`），每次 push 跑 `go vet`、`go test` 與 `appctl check`。push 前本機的驗證照〈完成定義〉跑，不要只靠 CI。
- push 護欄是本 repo 自帶的 `.githooks/pre-push`，由帳號層派工 hook 呼叫。兩層怎麼串，見 `~/.claude/rules/githooks.md`〈兩層 hook 怎麼串〉。

## 2. 開工

1. 讀 `docs/PROGRESS.md`〈目前狀態〉。
2. 跑 `task doctor`。
3. 跑 `task lint` 與 `task test`。

基準是紅的時，先修好，再開始新工作。

### 機密檔是進版控的 demo 值

`services/*/secret/secret.env`、`services/quay/secret/config.yaml`、`services/gmm/mapping.env`、`services/gmm/sync.secret.env` 都進版控，內容是 demo 值。repo 是公開的。

- 實際環境由操作者直接改部署主機 clone 裡的檔。在部署主機上不要 `git commit -a`，也不要把這些檔的改動 push 出去。
- `services/gmm/random.csv` 是 seed 自動產生的使用者密碼，gitignored，唯一留存處。不要 `cat`，不要 `git add -f`。
- 測試假值與 demo 值一律含 `fake`、`example`、`changeme`、`dummy` 其中一個字，或短於 8 個字元。理由見〈測試假值要過帳號層 gitleaks〉。

### 會刪資料的動作，先問使用者

- `appctl delete`：k8s 軌連 manifest 內宣告的 PVC 一起刪，叢集 StorageClass 的 `reclaimPolicy` 是 `Delete`，資料跟著消失；podman 軌連具名卷一起移除。
- `appctl gmm seed`：刪除 `mapping.env` 以外多餘的 user、group、connection。動資料前先印計畫，再要求輸入服務名，沒有 tty 時中止。
- `appctl smoke`：最後一步是 `delete`，連卷刪掉。
- `appctl delete ncb --track podman`：連 `ncb-certbot` 卷刪掉，憑證一起消失。Let's Encrypt 的 rate limits 文件寫同一組網域每 7 天只簽 5 張重複憑證，刪了再簽會撞上限。
- `kadm`、`xadm-lxqt`、`xadm-xfce` 是叢集最高權服務，帳密不能輪換。要部署前，先確認網段隔離。

### 加一個服務

1. `services/<svc>/base/`：工作負載正本，檔名是 kind 小寫（`deployment.yaml`、`statefulset.yaml`、`pod.yaml`…）。`image:` 直接寫 mirror 的完整參照（tag 或 `@sha256:` digest）。
2. `services/<svc>/base/kustomization.yaml`：`resources:` 指向工作負載。不寫 `images:`，`appctl check` 會擋。
3. `services/<svc>/{podman,k8s}/kustomization.yaml`：`namespace: default`、`resources: [../base]`，加該軌專屬的資源（k8s 的 `service.yaml`、`ingress.yaml`、`pvc.yaml`）。podman 軌要改值時，用 kustomize patch 寫在 overlay。
4. podman 軌建 `services/<svc>/podman/setting.conf`，寫主機埠。文法見既有服務的檔頭。
5. 只支援一軌，就只建一個目錄。目錄存在就代表支援。
6. 有機密時，建 `services/<svc>/secret/`，放 `kustomization.yaml`（`secretGenerator` 加 `generatorOptions.disableNameSuffixHash: true`）與 `secret.env`（demo 值）。兩軌的 `resources:` 各加一行 `../secret`。`.gitignore` 的 `!services/*/secret/secret.env` 已經放行它；其他檔名要另加放行規則，否則 `*.env` 會擋掉。
7. 跑〈完成定義〉。`go test ./internal/quadlet/` 的 `TestTranslateAllServices` 會翻譯每個服務的 podman 軌。

### 改映像版本

改 manifest 的 `image:`。juicefs 與 minio 共用同一顆 digest，用 `command grep -rln 'sha256:' services/` 找出全部位置，加 `command` 的理由見 `~/.claude/CLAUDE.md`〈Claude 下的 `grep` 會跳過 gitignore 的檔〉。

改完跑 `appctl check-images <svc>`。同一個 tag 被重推時，有快取的節點或 podman 主機不會重拉（`IfNotPresent`，見 `docs/ARCHITECTURE.md`〈設計取捨〉），改 tag 或先清本機映像。跨大版本時，查上游有沒有改啟動時序或設定介面。

### 寫程式的規矩

- `appctl` 用 Go 寫，gofmt 與 `go vet` 要乾淨。外部指令一律經 `internal/run` 的 `Runner`，測試換成 `run.Fake`。
- bash 只用在 `install.sh`、`bin/doctor.sh` 與 F9 維護者工具。Python 只用在 F9 維護者工具。不要新增 bash 或 Python 的營運工具，改加 `appctl` 子命令。
- F9 維護者工具（`.githooks/`、`bin/check-docs.py`、`bin/check-taskfile.py`、`bin/manifest-diff.py`、`bin/render-diff.sh`、`bin/run-tests.py` 與它們的測試）是範本 repo 的副本，不在這裡改。
- 終端訊息用 `Info:`、`Error:`、`Success:` 前綴，`Error:` 走 stderr，內文用台灣繁體中文。

## 3. 地雷

### 不要手寫 Quadlet 單元，systemd 會改寫 `%` 與 `$`

systemd 把單元檔裡的 `%T` 這類字樣換成路徑，`$VAR` 換成環境變數，不報錯。filebrowser 與 juicefs 的指令有 `$`。翻譯器（`internal/quadlet` 的 `Escape`）把 `%` 寫成 `%%`、`$` 寫成 `$$`。要改單元內容，改翻譯器或 manifest，不要直接改 `/etc/containers/systemd/` 底下的檔：下次 `apply` 會蓋掉。

### manifest 加新欄位前，先擴充翻譯器

翻譯器只認 `internal/quadlet/whitelist.go` 列的欄位，其他欄位一律報錯，不產生單元。理由：漏翻的欄位在 podman 端會被靜默丟掉，例如沒翻的 `resources.limits` 等於沒有限制。在 base 加欄位時，先在 `internal/quadlet` 實作翻譯與測試，再改 manifest。只有 k8s 軌要的欄位，寫在 k8s overlay。

### podman 軌的主機埠只寫在 `setting.conf`

翻譯器不收 `hostPort`。manifest 寫了 `hostPort` 會報錯；podman overlay 不要用 patch 加 `hostPort`。主機埠只寫在 `services/<svc>/podman/setting.conf`，同主機撞埠也由 `appctl check` 從這裡算。

### merge 或 checkout 會不提示就蓋掉主工作樹的真值檔

舊版把機密真值放在 gitignored 的檔，現在同名的 demo 檔進版控。還留著舊真值檔的工作樹（主工作樹、舊版 clone 的部署主機），在 merge、checkout 或 `git pull` 帶進同名的追蹤檔時，git 不提示就覆寫被忽略的檔，真值消失。動這類 git 操作前，先問使用者，再用 `cp -p` 把真值檔備份到 repo 外。

### 測試假值要過帳號層 gitleaks

帳號層 gitleaks 規則擋 `password: <8 字以上的值>` 這類寫法（鍵名含 password、passwd、secret、token、api_key、apikey）。commit 歷史裡出現過就推不出去，只能改寫歷史。測試與 demo 的值一律含 `fake`、`example`、`changeme`、`dummy` 其中一個字，或短於 8 個字元。

### push 護欄跑的是主工作樹的腳本

`pre-push` 的規則檔與檢查腳本取自主工作樹，不取自你在推的分支，見 `~/.claude/rules/githooks.md`〈在 worktree 裡 push 驗不到你剛改的 hook〉。分支刪掉或改了某支檢查時，merge 前推分支會被主工作樹的舊檢查擋下。merge 後，`.githooks/pre-push` 的 `REPO_CHECKS` 指向的腳本已不存在，那道檢查印 `Info:` 略過，靠 CI 的 `appctl check` 補上。

### 這台主機的 go 不在非登入 shell 的 PATH

`task lint`、`task test`、`task bootstrap` 都要呼叫 `go`。非登入 shell（例如 agent 的 Bash 工具）找不到 `go` 時，先把 go 的安裝目錄加進 `PATH` 再跑 `task`。不要把主機的 go 路徑寫進 repo 的任何檔。`task doctor` 會查 `go` 在不在 `PATH`。

### 映像還在不在 registry，push 時不會查

`appctl check` 擋短名、缺 tag 與 `latest`。映像是不是還在來源 registry，只有 `appctl check-images <svc>` 查，push 時與 CI 都不跑：它要逐一連 registry。改了 manifest 的 `image:`，要自己跑。

### `services/` 底下不要寫 `docker.io/` 字面值

push 護欄的 forbidden 掃描會掃 `services/` 的新增行，註解也算。它擋哪些樣式見 `.githooks/forbidden-patterns.txt`，不要把樣式抄進本檔。映像要用 mirror 的完整參照，直接寫在 manifest 的 `image:`。要舉例時，改講法或指路徑。哪些路徑不掃，見 `~/.claude/rules/githooks.md`〈各道掃描的例外不一樣〉。

### PVC 容量只在第一次部署時生效

叢集預設 StorageClass 沒有開 `allowVolumeExpansion`。對既有 PVC 改容量再 `apply`，會直接失敗。這時最自然的下一步是 `kubectl delete pvc` 重建，但叢集 StorageClass 的 `reclaimPolicy` 都是 `Delete`，**資料會被刪掉**。要改容量前，先停下來問使用者。

### podman 軌是 rootful

- 部署動詞、`smoke`、`ncb cert` 要用 `sudo appctl`。`appctl` 內部不加 `sudo`。
- 不加 `sudo` 的 `podman pod ps`、`podman volume ls`、`podman logs` 看的是你自己的 rootless 實例，什麼都查不到，看起來像服務沒起來。沒有 tty 時 sudo 要密碼會直接失敗，先 `sudo -v`。
- podman 軌沒有 Service。pod 之間不用名稱互連，要連別的服務就走主機埠，例如 ncb 用 `host.containers.internal:9000` 連 minio。

## 4. 完成定義

下面各層全部通過，才算完成：

- L1：`task lint` 回 0。它跑四大文件與 Taskfile 檢查、`go run ./cmd/appctl check` 與 shellcheck。
- L2：`task test` 回 0。它跑 `go test ./...`，再跑 F9 的 Python 測試；每支 Python 測試檔都要印 `ALL <n> PASS`。
- L3：真環境。只在改到跨元件的路徑時才跑，例如部署流程、服務之間的連線。要跑哪些，見〈改了哪類檔要多驗什麼〉。
- push 之後，GitHub Actions 的 `ci` 要綠。

### 改了哪類檔要多驗什麼

- 改了 manifest 或 `kustomization.yaml`：跑 `appctl render <svc> --track <軌>`，兩軌都跑；podman 軌另外會被 `TestTranslateAllServices` 翻譯一次。
- 改了 manifest 的 `image:`：跑 `appctl check-images <svc>`。
- 改動會影響很多服務的渲染時（例如改 base 的共用寫法），對每個受影響的服務與軌跑 `bash bin/render-diff.sh <svc> <軌> main`，它比對 `main` 與工作樹的渲染產物。
- 改了 `internal/quadlet`：更新 `internal/quadlet/testdata/` 的 golden 檔前，逐行看懂差異，再提交。
- 改了 `.githooks/` 或 `bin/check-*.py`：照 `~/.claude/rules/githooks.md`〈改了 `.githooks/` 底下任何東西〉再跑一次真的 hook。手動跑時照 `~/.claude/rules/githooks.md`〈直接跑 `pre-push` 不餵 stdin 會得到假綠燈〉。

### 不要用 pytest 代替 `task test`

`python3 -m pytest tests/` 會一起收到本機不進版控的 finetune 測試，結果和 `task test` 不一定相同。`task test` 只收沒被 `.gitignore` 擋掉的檔，收法見 `bin/run-tests.py` 的檔頭。

### 真環境煙測會刪資料，先問使用者

podman 軌的煙測是 `sudo appctl smoke <svc>...`，步驟與各服務的探測寫在 `internal/smoke/smoke.go`。宣稱完成前，要真的跑過並貼出輸出。

- 最後一步是 `delete`，會刪掉該服務的卷，正式資料一起消失。跑之前先問使用者要跑哪些服務。
- ncb、kadm、xadm-* 預設拒跑，理由見〈會刪資料的動作，先問使用者〉；要跑得加 `--allow-guarded`。
- 每個服務的 `delete` 會要求輸入服務名，所以要有 tty，沒有 tty 時那個服務記為失敗。
- k8s 軌沒有煙測，照 `internal/smoke/smoke.go` 的步驟手動跑。

## 5. 不要做的事

| 不要 | 為什麼 |
|---|---|
| 沒問使用者就跑 `appctl delete`、`appctl smoke`、`appctl gmm seed`，或刪 PVC | 會刪資料，不能復原 |
| 被歷史 commit 擋 push 時，自己 squash 或改寫歷史 | 選哪條路是使用者的決定，先停下來問。HEAD 的內容也要修乾淨，squash 帶走的是 HEAD |
| 順手改 `docs/PROGRESS.md` 列的既有問題 | 不在這次的範圍內，會讓 diff 混進無關改動 |
| 手寫或手改 `/etc/containers/systemd/` 底下的單元 | 下次 `apply` 會蓋掉；`%` 與 `$` 沒跳脫時 systemd 靜默改寫指令 |
| 用 `podman kube play` 或 `podman kube down` 部署、拆除服務 | 這條路已經拿掉。舊 pod 會占住主機埠，`appctl apply` 會拒絕部署 |
| 在 `docs/` 放四大文件以外的檔 | 沒人連得到，會和程式漂移。`docs/superpowers/` 與 `docs/tickets/` 是本機暫存，已 gitignore |
| 把機密放進命令列、URL、scratchpad 或 commit 訊息 | 會出現在 `ps`、log 或版控 |
| 在部署主機的 clone 上 commit 或 push | 機密檔已經改成真值，gitleaks 不保證擋得住 |
| 在本 repo 設 local 或 worktree 作用域的 `core.hooksPath` | git 改用那個目錄，不經過帳號層派工 hook，機密掃描與本 repo 的 `pre-push` 都靜默失效。`task doctor` 會查。細節見 `~/.claude/rules/githooks.md`〈repo 自設 local `core.hooksPath` 會繞過帳號層機密掃描〉 |
