# AGENTS.md

寫給 AI agent 的 app-services 作業指引。只收 app-services 專屬、而且跨檔的事。只跟單一檔案有關的坑寫在該檔註解，打開那個檔就讀得到。

## 1. 30 秒定位

- 這是容器服務的部署集，沒有應用程式碼。內容是 kustomize manifest 正本、一支部署控制腳本 `bin/svc.sh`、離線測試、litellm 的實機回歸，以及各服務的營運工具 `sync.sh`。
- 渲染由 kustomize 原生欄位（`namespace:`、`images:`）完成，腳本不做任何字串替換。`bin/svc.sh` 只做守衛、機密補位與確認，部署交給 `kubectl apply -k` 與 `podman kube play`。
- LiteLLM 的 DLP 與自動分流都用內建功能，設定在 `services/litellm/base/config.yaml`。
- 改結構前先讀 `docs/ARCHITECTURE.md`〈依賴與資料流〉。
- 沒有 CI，所有驗證都在本機。不要假設遠端有東西幫你擋。
- push 護欄由工作區根 repo 管理，本 repo 沒有自己的 hook。細節見工作區根 repo 的 `AGENTS.md`。

## 2. 動手前

### 機密真值檔就在本機磁碟上

有機密的服務（`ls services/*/secret/secret.env.example`），同目錄的 `secret.env` 存在且含真值，靠 `.gitignore` 擋住。quay 另有 `secret/config.yaml`，`gmm` 與 `openwebui` 另有 `sync.secret.env`。

- 不要 `cat` 它們，不要把內容貼進任何輸出，不要 `git add -f`。
- 要知道有哪些鍵，讀同目錄的 `.example`。

### 會刪資料的動作，先問使用者

- `task delete`：k8s 軌連 manifest 內宣告的 PVC 一起刪，叢集 StorageClass 的 `reclaimPolicy` 是 `Delete`，資料跟著消失；podman 軌連具名卷一起移除。
- `task seed SVC=gmm`：刪除 `mapping.list` 以外多餘的 user、group、connection。
- `kadm`、`xadm-lxqt`、`xadm-xfce` 是叢集最高權服務，帳密不能輪換。要部署前，先確認網段隔離。

### 從 worktree 部署前，先複製真值檔

新開的 worktree 沒有 gitignored 的機密檔，`apply` 會補上 `.example` 佔位值。已經在跑的服務，資料庫卷是用真密碼建的，換成佔位值就連不上。

- 從主工作樹複製真值檔進 worktree，用 `cp -p` 保留原權限。
- `git worktree remove` 會連同 worktree 裡的機密檔一起刪掉，主工作樹要留一份。
- `task litellm:check` 也從兩份 `secret.env` 讀 key，在 worktree 裡跑之前同樣要先複製。

### 加一個服務

1. `services/<svc>/base/`：工作負載正本，檔名是 kind 小寫（`deployment.yaml`、`daemonset.yaml`、`pod.yaml`…）。manifest 不放機密真值，`image:` 只寫短名。
2. `services/<svc>/base/kustomization.yaml`：`resources:` 指向工作負載，加 `images:`。`name:` 寫短名，`newName:` 寫完整 repo，`newTag:` 加雙引號；digest 型改用 `digest:`（值帶 `sha256:` 前綴），而且不給 `newTag:`。
3. 短名一律帶服務名：單容器用 `<svc>`，多容器用 `<svc>-<容器名>`。不要用裸容器名，理由見〈`images:` 對不上時不會報錯〉。
4. `services/<svc>/{podman,k8s}/kustomization.yaml`：`namespace: default`、`resources: [../base]`，加該軌專屬的資源（podman 的 hostPort patch；k8s 的 `service.yaml`、`ingress.yaml`、`pvc.yaml`）。podman 軌的 `hostPort` 用 JSON6902 加 `op: test`，照既有服務的 overlay 抄，不用 strategic merge。
5. 只支援一軌，就只建一個目錄。目錄存在就代表支援。
6. 有機密時，建 `services/<svc>/secret/`，放 `kustomization.yaml`（`secretGenerator` 加 `generatorOptions.disableNameSuffixHash: true`）與 `secret.env.example`（佔位值，進版控）。兩軌的 `resources:` 各加一行 `../secret`。沒有機密的服務不要建這個目錄：`resources` 指向不存在的目錄，kustomize 會直接失敗。
7. 跑〈改完要驗〉。

### 改映像版本

改 `services/<svc>/base/kustomization.yaml` 的 `images:`。`minio` 的 podman 軌不共用 base，自帶一份 `images:`，要一起改；juicefs 與 minio 共用同一顆 digest，用 `grep -rln 'digest: sha256:' services/` 找出全部位置。`vllm` 的容器共用同一顆映像，但短名各一筆，base 的每一筆都要改。

改完跑 `task image:check SVC=<svc>`。跨大版本時，查上游有沒有改啟動時序或設定介面。

### 寫程式的規矩

- bash：不要把 `[[ cond ]] && { ...; }` 當獨立語句。條件為假時整段回非零，日後加上 `errexit` 會靜默中止；當腳本最後一條語句時，整支腳本會以 1 結束。用 `if` 寫。既有的違規列在 `docs/PROGRESS.md`，不要順手改。
- 不在 bash 裡解析 YAML，要解析就用 python。
- `yq` 是 python-yq，只用 jq 相容語法，不用 `yq e`、`yq ea`、`yq eval`。
- 語言只用 bash 與 python。bash 管流程、IO、git 膠水；python 管結構化資料驗證與 litellm 的實機回歸。`config.yaml` 裡的 `custom_code` 是 LiteLLM 沙箱跑的類 Python，照 LiteLLM 的規格寫。

## 3. 地雷

### `images:` 對不上時不會報錯

manifest 的 `image:` 寫短名，完整參照在 `kustomization.yaml` 的 `images:`。`name:` 對不上時，kustomize 回 0、不警告，產物原樣留著短名。短名如果剛好是 Docker Hub 官方庫的名字（例如 `nginx`、`mariadb`、`redis`），就會拉到能正常啟動的錯映像。所以短名一律帶服務名。

能擋這件事的是 `bin/assert-images.sh`，它只在 `task image:check` 跑，**push 時不會跑**：共用護欄只收全部 repo 都適用的檢查。改了 `images:` 或 manifest 的 `image:`，要自己跑。

### `services/` 底下不要寫 `docker.io/` 字面值

push 護欄的 forbidden 掃描會掃 `services/` 的新增行，註解也算。它只擋 `docker.io/` 與指向浮動分支的 `raw/<分支>/`，不擋 quay、ghcr 等上游，所以 `images:` 的 `newName:` 可以寫 `quay.io/...`。映像要用 mirror，不要直接寫 `docker.io/`。要舉例時，改講法或指路徑（「見 `services/<svc>/<軌>/kustomization.yaml` 的 `images:`」）。哪些路徑不掃，見工作區根 repo 的 `.claude/rules/githooks.md`〈各道掃描的例外不一樣〉；樣式清單見工作區根 repo 的 `.githooks/forbidden-patterns.txt`。

### 手動跑 `kubectl kustomize` 前，要先備好機密檔

有機密的服務，`secret/` 的 `secretGenerator` 直接讀 `secret.env`。檔案不在時，`kubectl kustomize` 回 1（`evalsymlink failure`），連純本地的語法驗證都跑不動。`bin/svc.sh` 會自動補位，但手動跑 `kubectl kustomize services/<svc>/k8s` 時不會。先把 `.example` 複製成 `secret.env`，或先跑一次 `bash bin/svc.sh <svc> delete k8s`，在確認提示處直接按 Enter 取消，讓腳本補位。

`apply` 時多印的兩行 `Info:`（「目前用的是 .example 佔位值」）不是雜訊：它表示這次部署出去的服務連不上相依元件。

### PVC 容量只在第一次部署時生效

叢集預設 StorageClass 沒有開 `allowVolumeExpansion`。對既有 PVC 改容量再 `apply`，會直接失敗。這時最自然的下一步是 `kubectl delete pvc` 重建，但叢集 StorageClass 的 `reclaimPolicy` 都是 `Delete`，**資料會被刪掉**。要改容量前，先停下來問使用者。

### podman 的 pod 名與跨 pod 名稱跟著 kind 變

- `kind: Pod` 起出來的 pod 叫 `<svc>`；`Deployment`、`DaemonSet` 叫 `<svc>-pod`，容器叫 `<svc>-pod-<容器名>`。`podman pod stop`、`logs` 這類指令要跟著改。
- podman 軌沒有 Service，別的 pod 要用「Deployment 名加 `-pod`」連，例如 `http://litellm-pod:4000`。base 裡寫 K8s Service 名的 env，podman 軌的 overlay 要改寫。例外是 `services/litellm/base/config.yaml`：它是 ConfigMap 檔，`api_base` 直接寫 podman 名，k8s overlay 不改寫。
- podman 掛 GPU 要寫 CDI 名稱（`nvidia.com/gpu=all` 或編號 `nvidia.com/gpu=0`），由 overlay 從 base 的 K8s 鍵換過來。同一個 pod 裡混用不同編號還沒驗過：第一次這樣部署時，進每個容器跑 `nvidia-smi`，確認它只看得到自己那張卡。

### 同一張卡上的 GPU 記憶體要跨服務一起算

vLLM 的 `--gpu-memory-utilization` 以整張卡為分母，同一張卡上所有容器加總不超過 0.92，理由寫在 `services/vllm/base/deployment.yaml` 的註解。別的 pod 也掛同一張卡時（例如本機的 finetune），改 `vllm` 的比例要回頭看它。

### litellm 改了，openwebui 要跟著跑 seed

OpenWebUI 的系統提示詞、橫幅與語音開關由 `task seed SVC=openwebui` 寫進資料庫，不靠環境變數。litellm 新增模型後要再跑一次，新模型才會有系統提示詞。`task seed:check` 會列出沒套上系統提示詞的模型。

### 自動分流與 DLP

設定都在 `services/litellm/base/config.yaml`，單一設定項的坑寫在該檔註解。下面是跨檔或要動手才會碰到的：

- **分流不是資安邊界。** 使用者可以在訊息裡指定判斷結果，或加特定字串讓判斷升級。資料能不能送出去，靠 DLP 把關。
- **看請求去了哪裡，要看回應標頭 `x-litellm-model-name`。** `x-litellm-model-group` 與回應本文的 `model` 都寫 `chat-auto`。
- **DLP 阻擋時回 200，不是錯誤。** 回應是一則以「DLP：請求含有…」開頭的合成回答，agent 看到的是一般回答。
- **DLP 檢查整段對話。** 一段對話出現過個資，之後每一輪送雲端都會命中。email 規則也會命中程式碼裡的 git 位址。
- **改了 `KEYWORDS` 或 `config.yaml`，要重新部署 litellm。** k8s 軌另跑 `kubectl rollout restart deployment/litellm`：ConfigMap 名稱不帶雜湊，`apply` 不會重啟 pod。
- **LiteLLM 升版後要重跑 `task litellm:check`。** 沙箱的限制或 `inputs` 的欄位變了，不會報錯。

### LiteLLM 加 worker 或副本前，要先上 Redis

LiteLLM 現在只跑 1 個 worker 與 1 個副本，沒有 Redis。限流計數、並行數、預算與路由的冷卻狀態都存在各行程的記憶體裡。加了 worker 或副本之後，各行程各算各的，會各自放行同一份額度，而且不報錯。podman 軌會忽略 `replicas`，只能加 worker。

要加 worker（`--num_workers` 大於 1）或副本時，三件事一起做：

1. 上 Redis。兩處都要接：限流與預算用的協調快取（`general_settings.coordination_redis` 或 `REDIS_*` 環境變數），以及路由器的 `router_settings.redis_host`。
2. 拿掉 `services/litellm/base/deployment.yaml` 的 `LITELLM_DISABLE_NO_REDIS_WARNING`。留著它，Redis 沒接上時也看不到提示。
3. 加副本時，把 PostgreSQL 搬出 litellm 的 pod。postgres 的資料在 `ReadWriteOnce` 的 PVC，每個副本都會起一個 postgres 搶同一個 PVC。只加 worker 時不用搬。

做完後，帶 key 打 `/health/readiness/details`，確認 `show_no_redis_warning` 是 `false`；再讀設定檔，確認 `router_settings.redis_host` 有值。前一步驗不到路由器那一處。

## 4. 改完要驗

```bash
task test                              # 離線單元測試，必跑。每支測試檔印 ALL <n> PASS
kubectl kustomize services/<svc>/k8s   # 該服務正本的語法；podman 軌同樣跑一次
bash bin/assert-images.sh <svc>        # 渲染產物的 image 都是完整參照，而且還在來源 registry（task image:check 也跑這支）
task litellm:check                     # 實機回歸：對執行中的 litellm 跑 DLP 與分流情境。重新部署 litellm 後必跑
```

- `task test` 用 `python3 tests/<檔>.py` 逐支執行。新增測試檔時，要把它加進 `Taskfile.yaml` 的 `test` 任務。該任務逐行列出測試檔，不會自動收新檔。新檔的檔尾也一定要補 `__main__` 執行段，照 `tests/test_manifest_diff.py` 檔尾的寫法。少了這段，`task test` 對已列入的檔零輸出、回 0，一支都沒跑卻報成功。判斷有沒有真的在跑，看每支有沒有印 `ALL <n> PASS`。
- `python3 -m pytest tests/` 也能跑同一批測試，但會一起收到本機不進版控的 finetune 測試，結果和 `task test` 不一定相同。
- `task litellm:check` 呼叫 `tests/live_gateway.py`，打正式的 litellm，不是離線測試。檔名刻意不以 `test_` 開頭，pytest 不會收進去。
- 改動會影響很多服務的渲染時（例如改 base 的共用寫法），用 `bin/manifest-diff.py` 比對改前改後的 `kubectl kustomize` 產物。有機密的服務加 `--include-secrets`。
- 其他真環境煙測沒有腳本，要逐條手動跑。宣稱完成前，要真的跑過並貼出輸出。
- 想手動驗一次 push 護欄，照工作區根 repo 的 `.claude/rules/githooks.md`〈直接跑 `pre-push` 不餵 stdin 會得到假綠燈〉。護欄掃這次 push 的每個 commit 的新增行，所以不要用 `git diff main..HEAD` 判斷會不會被擋：「加了又刪」的內容在淨差異裡看不見。

## 5. 不要做的事

| 不要 | 為什麼 |
|---|---|
| 沒問使用者就跑 `task delete`、`task seed SVC=gmm`，或刪 PVC | 會刪資料，不能復原 |
| 被歷史 commit 擋 push 時，自己 squash 或改寫歷史 | 選哪條路是使用者的決定，先停下來問。HEAD 的內容也要修乾淨，squash 帶走的是 HEAD |
| 順手改 `docs/PROGRESS.md` 列的既有違規 | 不在這次的範圍內，會讓 diff 混進無關改動 |
| 把 `bin/svc.sh` 的 podman `delete` 容錯當成已驗證 | 那道容錯還沒在實際情況下觸發過 |
| 「補齊」`minio` 的 podman 軌讓它共用 base | `podman kube play` 不支援 `StatefulSet`，做不到 |
| 在 `docs/` 放四大文件以外的檔 | 沒人連得到，會和程式漂移。`docs/superpowers/` 與 `docs/tickets/` 是本機暫存，已 gitignore |
| 把機密放進命令列、URL、scratchpad 或 commit 訊息 | 會出現在 `ps`、log 或版控 |
| 在本 repo 設 local 或 worktree 作用域的 `core.hooksPath` | 會蓋過工作區的設定，push 護欄靜默失效。見工作區根 repo 的 `AGENTS.md`〈`core.hooksPath` 被 local 設定蓋過，護欄會靜默失效〉 |
