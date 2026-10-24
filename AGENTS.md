# AGENTS.md

寫給 AI agent 的 app-services 作業指引。只收 app-services 專屬、而且跨檔的事。只跟單一檔案有關的坑寫在該檔註解，打開那個檔就讀得到。

## 1. 30 秒定位

- 這是容器服務的部署集，沒有應用程式碼。內容是 kustomize manifest 正本、一支部署控制腳本 `bin/svc.sh`、離線測試，以及各服務的營運工具 `sync.sh`。
- 渲染由 kustomize 原生欄位（`namespace:`）完成，映像直接寫完整參照在 manifest，腳本不做任何字串替換。`bin/svc.sh` 只做守衛、機密補位與確認，部署交給 `kubectl apply -k` 與 `podman kube play`。
- 改結構前先讀 `docs/ARCHITECTURE.md`〈依賴與資料流〉。
- 沒有 CI，所有驗證都在本機。不要假設遠端有東西幫你擋。
- push 護欄是本 repo 自帶的 `.githooks/pre-push`，由帳號層派工 hook 呼叫。兩層怎麼串，見 `~/.claude/rules/githooks.md`〈兩層 hook 怎麼串〉。

## 2. 開工

1. 讀 `docs/PROGRESS.md`〈目前狀態〉。
2. 跑 `task doctor`。
3. 跑 `task lint` 與 `task test`。

基準是紅的時，先修好，再開始新工作。

### 機密真值檔就在本機磁碟上

有機密的服務（`ls services/*/secret/secret.env.example`），同目錄的 `secret.env` 存在且含真值，靠 `.gitignore` 擋住。quay 另有 `secret/config.yaml`，`gmm` 另有 `sync.secret.env`，還有兩個：`mapping.env` 含目標主機的明文帳密，`random.csv` 是 seed 自動產生的使用者密碼，唯一留存處。

- 不要 `cat` 它們，不要把內容貼進任何輸出，不要 `git add -f`。
- 要知道有哪些鍵，讀同目錄的 `.example`。

### 會刪資料的動作，先問使用者

- `task delete`：k8s 軌連 manifest 內宣告的 PVC 一起刪，叢集 StorageClass 的 `reclaimPolicy` 是 `Delete`，資料跟著消失；podman 軌連具名卷一起移除。
- `task seed SVC=gmm`：刪除 `mapping.env` 以外多餘的 user、group、connection。腳本動資料前要求輸入服務名，沒有 tty 時中止。腳本先印計畫再確認；`mapping.env` 有任一行欄數不對或一筆都沒有時，整批中止。
- `task delete SVC=ncb TRACK=podman`：連 `ncb-certbot` 卷刪掉，憑證一起消失。Let's Encrypt 的 rate limits 文件寫同一組網域每 7 天只簽 5 張重複憑證，刪了再簽會撞上限。
- `kadm`、`xadm-lxqt`、`xadm-xfce` 是叢集最高權服務，帳密不能輪換。要部署前，先確認網段隔離。

### 從 worktree 部署前，先複製真值檔

新開的 worktree 沒有 gitignored 的機密檔，`apply` 會補上 `.example` 佔位值。已經在跑的服務，資料庫卷是用真密碼建的，換成佔位值就連不上。

- 從主工作樹複製真值檔進 worktree，用 `cp -p` 保留原權限。
- `git worktree remove` 會連同 worktree 裡的機密檔一起刪掉，主工作樹要留一份。

### 加一個服務

1. `services/<svc>/base/`：工作負載正本，檔名是 kind 小寫（`deployment.yaml`、`daemonset.yaml`、`pod.yaml`…）。manifest 不放機密真值，`image:` 直接寫 mirror 的完整參照（tag 或 `@sha256:` digest）。
2. `services/<svc>/base/kustomization.yaml`：`resources:` 指向工作負載。不寫 `images:`，`bin/check-manifests.py` 會擋。
3. `services/<svc>/{podman,k8s}/kustomization.yaml`：`namespace: default`、`resources: [../base]`，加該軌專屬的資源（podman 的 hostPort patch；k8s 的 `service.yaml`、`ingress.yaml`、`pvc.yaml`）。podman 軌的 `hostPort` 用 JSON6902 加 `op: test`，照既有服務的 overlay 抄，不用 strategic merge。
4. 只支援一軌，就只建一個目錄。目錄存在就代表支援。
5. 有機密時，建 `services/<svc>/secret/`，放 `kustomization.yaml`（`secretGenerator` 加 `generatorOptions.disableNameSuffixHash: true`）與 `secret.env.example`（佔位值，進版控）。兩軌的 `resources:` 各加一行 `../secret`。沒有機密的服務不要建這個目錄：`resources` 指向不存在的目錄，kustomize 會直接失敗。
6. 跑〈完成定義〉。

### 改映像版本

改 manifest 的 `image:`。`minio` 的 podman 軌不共用 base，自帶 `pod.yaml`，要一起改；juicefs 與 minio 共用同一顆 digest，用 `command grep -rln 'sha256:' services/` 找出全部位置，加 `command` 的理由見 `~/.claude/CLAUDE.md`〈Claude 下的 `grep` 會跳過 gitignore 的檔〉。

改完跑 `task check:image SVC=<svc>`。同一個 tag 被重推時，有快取的節點或 podman 主機不會重拉（`IfNotPresent`，見 `docs/ARCHITECTURE.md`〈設計取捨〉），改 tag 或先清本機映像。跨大版本時，查上游有沒有改啟動時序或設定介面。

### 寫程式的規矩

- bash：不要把 `[[ cond ]] && { ...; }` 當獨立語句。條件為假時整段回非零，日後加上 `errexit` 會靜默中止；當腳本最後一條語句時，整支腳本會以 1 結束。用 `if` 寫。既有的違規列在 `docs/PROGRESS.md`，不要順手改。
- 不在 bash 裡解析 YAML，要解析就用 python。
- `yq` 是 python-yq，只用 jq 相容語法，不用 `yq e`、`yq ea`、`yq eval`。
- 語言只用 bash 與 python。bash 管流程、IO、git 膠水；python 管結構化資料驗證，營運工具要打 HTTP API 時也用 python（例如 `services/gmm/sync.py`）。

## 3. 地雷

### 映像還在不在 registry，push 時不會查

`bin/check-manifests.py` 在 `task lint` 與 push 時擋短名、缺 tag 與 `latest`。映像是不是還在來源 registry，只有 `task check:image` 查，**push 時不會跑**：它要逐一連 registry，不是幾秒內跑得完的檢查。改了 manifest 的 `image:`，要自己跑。

### `services/` 底下不要寫 `docker.io/` 字面值

push 護欄的 forbidden 掃描會掃 `services/` 的新增行，註解也算。它擋哪些樣式見 `.githooks/forbidden-patterns.txt`，不要把樣式抄進本檔。映像要用 mirror 的完整參照，直接寫在 manifest 的 `image:`。要舉例時，改講法或指路徑。哪些路徑不掃，見 `~/.claude/rules/githooks.md`〈各道掃描的例外不一樣〉。

### 手動跑 `kubectl kustomize` 前，要先備好機密檔

有機密的服務，`secret/` 的 `secretGenerator` 直接讀 `secret.env`。檔案不在時，`kubectl kustomize` 回 1（`evalsymlink failure`），連純本地的語法驗證都跑不動。`bin/svc.sh` 會自動補位，但手動跑 `kubectl kustomize services/<svc>/k8s` 時不會。先把 `.example` 複製成 `secret.env`，或先跑一次 `bash bin/svc.sh <svc> delete k8s`，在確認提示處直接按 Enter 取消，讓腳本補位。

`apply` 時多印的兩行 `Info:`（「目前用的是 .example 佔位值」）不是雜訊：它表示這次部署出去的服務連不上相依元件。

### PVC 容量只在第一次部署時生效

叢集預設 StorageClass 沒有開 `allowVolumeExpansion`。對既有 PVC 改容量再 `apply`，會直接失敗。這時最自然的下一步是 `kubectl delete pvc` 重建，但叢集 StorageClass 的 `reclaimPolicy` 都是 `Delete`，**資料會被刪掉**。要改容量前，先停下來問使用者。

### podman 的 pod 名與跨 pod 名稱跟著 kind 變

- `kind: Pod` 起出來的 pod 叫 `<svc>`；`Deployment`、`DaemonSet` 叫 `<svc>-pod`，容器叫 `<svc>-pod-<容器名>`。`podman pod stop`、`logs` 這類指令要跟著改。
- podman 軌是 rootful：`bin/svc.sh` 用 `sudo podman`。不加 `sudo` 的 `podman pod ps`、`podman volume ls`、`podman logs` 看的是你自己的 rootless 實例，什麼都查不到，看起來像服務沒起來。沒有 tty 時 sudo 要密碼會直接失敗，先 `sudo -v`。
- `apply` 失敗時（例如 hostPort 被占）建一半的 pod 會留在系統裡繼續占埠，擋住下一個服務；`--replace` 只換同名 pod，不處理別的服務留下的。清法是 `sudo podman pod rm -f <pod>`。
- podman 軌沒有 Service，別的 pod 要用「Deployment 名加 `-pod`」連，例如 `http://grafana-pod:3000`。base 裡寫 K8s Service 名的 env，podman 軌的 overlay 要改寫。

## 4. 完成定義

下面各層全部通過，才算完成：

- L1：`task lint` 回 0。
- L2：`task test`。每支測試檔都要印 `ALL <n> PASS`，而且 `task test` 回 0。
- L3：真環境。只在改到跨元件的路徑時才跑，例如部署流程、服務之間的連線。要跑哪些，見〈改了哪類檔要多驗什麼〉。

### 改了哪類檔要多驗什麼

- 改了 manifest 或 `kustomization.yaml`：跑 `kubectl kustomize services/<svc>/k8s`，podman 軌同樣跑一次。先備好機密檔，見〈手動跑 `kubectl kustomize` 前，要先備好機密檔〉。
- 改了 manifest 的 `image:`：跑 `task check:image SVC=<svc>`。它查渲染產物的 image 都是完整參照，而且還在來源 registry。
- 改動會影響很多服務的渲染時（例如改 base 的共用寫法），對每個受影響的服務與軌跑 `bash bin/render-diff.sh <svc> <軌> main`，它比對 `main` 與工作樹的渲染產物，不帶機密真值。要連 Secret 的值一起比時，改用 `bin/manifest-diff.py --include-secrets` 手動比，產物檔含機密，比完就刪。
- 改了 `.githooks/` 或 `bin/check-*.py`：照 `~/.claude/rules/githooks.md`〈改了 `.githooks/` 底下任何東西〉再跑一次真的 hook。手動跑時照 `~/.claude/rules/githooks.md`〈直接跑 `pre-push` 不餵 stdin 會得到假綠燈〉。護欄掃這次 push 的每個 commit 的新增行，所以不要用 `git diff main..HEAD` 判斷會不會被擋：「加了又刪」的內容在淨差異裡看不見。

### 不要用 pytest 代替 `task test`

`python3 -m pytest tests/` 會一起收到本機不進版控的 finetune 測試，結果和 `task test` 不一定相同。`task test` 只收沒被 `.gitignore` 擋掉的檔，收法見 `bin/run-tests.py` 的檔頭。

### 真環境煙測會刪資料，先問使用者

podman 軌的煙測是 `task smoke -- <svc>...`，步驟與各服務的探測寫在 `bin/smoke.sh`。宣稱完成前，要真的跑過並貼出輸出。

- 最後一步是 `delete`，會刪掉該服務的卷，正式資料一起消失。跑之前先問使用者要跑哪些服務。
- ncb、kadm、xadm-* 預設拒跑，理由見〈會刪資料的動作，先問使用者〉；要跑得加 `--allow-guarded`。
- 每個服務的 `delete` 會要求輸入服務名，所以要有 tty，沒有 tty 時那個服務記為失敗。
- k8s 軌沒有煙測腳本，照 `bin/smoke.sh` 的步驟手動跑。

## 5. 不要做的事

| 不要 | 為什麼 |
|---|---|
| 沒問使用者就跑 `task delete`、`task seed SVC=gmm`，或刪 PVC | 會刪資料，不能復原 |
| 被歷史 commit 擋 push 時，自己 squash 或改寫歷史 | 選哪條路是使用者的決定，先停下來問。HEAD 的內容也要修乾淨，squash 帶走的是 HEAD |
| 順手改 `docs/PROGRESS.md` 列的既有違規 | 不在這次的範圍內，會讓 diff 混進無關改動 |
| 繞過 `bin/svc.sh`，直接用 `podman kube down --force` 拆服務 | podman 4.9.3 只刪 `kind: Pod` 與 PVC 文件的卷，Deployment 與 DaemonSet 的卷會留著，下次 `apply` 接回舊資料。`svc.sh` 會補 PVC 殼並確認卷已刪 |
| 「補齊」`minio` 的 podman 軌讓它共用 base | `podman kube play` 不支援 `StatefulSet`，做不到 |
| 在 `docs/` 放四大文件以外的檔 | 沒人連得到，會和程式漂移。`docs/superpowers/` 與 `docs/tickets/` 是本機暫存，已 gitignore |
| 把機密放進命令列、URL、scratchpad 或 commit 訊息 | 會出現在 `ps`、log 或版控 |
| 在本 repo 設 local 或 worktree 作用域的 `core.hooksPath` | git 改用那個目錄，不經過帳號層派工 hook，機密掃描與本 repo 的 `pre-push` 都靜默失效。`task doctor` 會查。細節見 `~/.claude/rules/githooks.md`〈repo 自設 local `core.hooksPath` 會繞過帳號層機密掃描〉 |
