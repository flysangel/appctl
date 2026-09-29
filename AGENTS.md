# AGENTS.md

給 AI agent 的專案脈絡。人類讀者請從 `README.md` 開始。

## 這個 repo 是什麼

容器服務的部署集，**沒有應用程式碼**。LiteLLM 的 DLP 與自動分流都用內建功能，設定在 `services/litellm/base/config.yaml`。其餘是 kustomize manifest 正本與一支部署控制腳本（`bin/svc.sh`）。另有離線單元測試、一支 litellm 的實機回歸，以及各服務的營運工具 `sync.sh`。

渲染是 kustomize 原生欄位（`namespace:` ＋ `images:`）做的，腳本**不做任何字串替換**——`envsubst` 與 `service.conf` 已全面退場，`build/` 中介產物也一併廢除。

要動手之前先讀 `docs/ARCHITECTURE.md` 的第 3 節（部署管線）與第 4 節（機密的三條路徑）。那兩節解釋了為什麼這裡的每個「多此一舉」都有理由。

## 🔴 地雷區

### 1. `images:` 的四個坑，全部靜默

manifest 裡的 `image:` 寫的是**短名**，完整字面值在同軌 `kustomization.yaml` 的 `images:` 區塊。這條轉換路徑上有四個坑，前三個不會有任何錯誤訊息：

1. **`name:` 對不上時 kustomize rc=0、無警告、產物原樣留短名**（靜默 no-op）。防線是 `bash bin/assert-images.sh <svc>`——它讀 `kubectl kustomize` 的產物，任一 `image:` 未解析為完整參照即 rc=1。**注意這道守衛只在 `task image:check` 跑、不在 pre-push**，你得自己記得跑。pre-push 是工作區共用的護欄，本 repo 自有的檢查加不進去（見第 5 條）。
2. **短名一律帶服務名**（單容器 `<svc>`、多容器 `<svc>-<容器名>`），改名時別圖省事拿裸容器名。skopeo 實測：`nginx`、`mariadb`、`redis` 在 Docker Hub 的 `library` 命名空間**真實存在**，用裸容器名當短名的話（ncb、grafana、quay 三支），`images:` 一漏列就會拉到**能正常啟動的錯映像**——比跑不起來更糟。
3. **`digest:` 的值不可省 `sha256:` 前綴**——漏掉時 kustomize 不驗證、`kubectl apply --dry-run=client` 也照過，只有真的拉映像時才炸。另外 `digest:` 與 `newTag:` **不可同時給**：會產出 `repo:TAG@sha256:...`，runtime 只認 digest、tag 淪為假資訊。
4. **`newTag:` 的值一律加雙引號**——這個唯一會當場叫的：純數字 tag 不加引號是 rc=1 硬錯誤（`cannot unmarshal number into Go struct field Image.images.newTag of type string`）。全 repo 只有 quay 的 postgres `13.23` 會踩，但規則統一比記例外便宜。

### 2. 七個服務的 `secret.env` 真值就在本機磁碟上

`services/{gmm,grafana,juicefs,litellm,minio,openwebui,quay}/secret/secret.env`（quay 另有 `secret/config.yaml`）存在且含真值，靠 `.gitignore` 擋住。**不要 `cat` 它們、不要把內容貼進任何輸出、不要 `git add -f`。** 需要知道有哪些鍵，讀同目錄的 `.example`。

### 3. `task delete` 與 `task seed SVC=gmm` 不可逆

- `task delete`：k8s 路徑連 manifest 內宣告的 PVC 一起刪，叢集 StorageClass 的 `reclaimPolicy` 是 `Delete`，**資料連帶消失**；podman 路徑連具名卷一起移除。執行前要求輸入服務名確認，無 tty 時 `read` 失敗、腳本中止。
- `task seed SVC=gmm`：會刪除 `mapping.list` 以外多餘的 user／group／connection。

兩者執行前一律先問使用者。

### 4. `kadm`／`xadm-lxqt`／`xadm-xfce` 是叢集最高權

三者皆 `cluster-admin` ClusterRoleBinding ＋ `privileged` 容器 ＋ 映像內建、**不可輪換的帳密**，且經 LoadBalancer 對外開 SSH／RDP。外洩＝整座叢集淪陷。

目前叢集上**沒有這三個服務的常駐實例**（驗證完即拆）。要重新部署前先確認網段隔離。

### 5. push 護欄是工作區共用的，本 repo 改不到也加不進去

護欄的行為、例外（行尾 `gitleaks:allow`）與接線，見根 repo 的 `AGENTS.md` 與 `.githooks/`。本 repo 內沒有任何護欄檔。改護欄要回根 repo 改，而且會同時影響 `/home/desktop/wk/workspace/bin/repos.txt` 列的全部 repo。

**本 repo 自己想加的檢查也進不了共用護欄。** 共用護欄只收全部 repo 都適用的檢查。`bin/assert-images.sh`（第 1 條那道守衛）要 `kubectl` 與 `skopeo`，只對本 repo 有意義。它因此只能放在 `bin/` ＋ Taskfile，**不會在 push 時自動跑**。

⚠️ **不要執行 `git config --local core.hooksPath`。** 接線由根 repo `dot/.gitconfig` 的 `includeIf` 提供，repo 的 local 設定會蓋過它。新機器跑根 repo 的 `bin/bootstrap.sh`。動手前先確認：

```bash
git config --show-origin --get core.hooksPath   # 來源應是根 repo 的 .githooks/include.gitconfig
```

### 6. （空號）

原本記台灣化掃描的豁免範圍。那道護欄已於 2026-09-16 退場，本條隨之刪除。編號保留，別處引用的「地雷區第 N 條」才不會錯位。

### 7. `services/` 底下不要寫 registry host 字面值

forbidden 掃描照掃 `services/` 的新增行，註解也算。哪些路徑不掃、樣式寫在哪，見根 repo 的 `AGENTS.md`〈各道掃描的例外不一樣〉與 `.githooks/forbidden-patterns.txt`。

**這條已經有人踩過**。`services/static-nginx/base/kustomization.yaml` 的檔頭註解原本為了解釋「為什麼短名不能用裸 `nginx`」，寫出帶尾斜線的完整 Docker Hub 路徑。這樣寫 push 會被擋。修法不是開豁免，是**換個講法把同一件事講完**。現在寫成「Docker Hub 官方庫真的有一個叫 nginx 的映像」，資訊沒少，樣式不命中。

要舉例就指路徑：「見 `services/<svc>/<軌>/kustomization.yaml` 的 `images:`」。

### 8. 沒有 CI

GitLab 端無 pipeline，整個工作區都沒有。所有驗證都在本機：`task test` ＋ pre-push ＋ 人工真環境煙測。**不要假設有東西會在遠端幫你擋。**

### 9. 缺 `secret/secret.env` 會讓那支服務整條 kustomize 路徑失敗

有機密的服務，`secret/` 目錄的 `secretGenerator` 直接讀 `secret.env`（quay 另加 `config.yaml`）。真值檔缺席時 `kubectl kustomize` **直接 rc=1**（`evalsymlink failure`）。不只 `apply -k` 與 `delete -k`，連純本地語法驗證都跑不動。

`bin/svc.sh:13-26` 會在動作前自同名 `.example` 複製佔位值兜底。所以：

- **`apply` 時多印的那兩行 `Info:`（「目前用的是 .example 佔位值，不是真密碼」）不是雜訊**，它代表這次部署出去的服務連不上相依元件。minio 更會拿假的 root 帳密把資料卷建起來，之後換真值得砍卷重來
- 手動只跑 `kubectl kustomize services/<svc>/k8s` 驗語法時，得自己先把 `.example` 複製一份，或先跑一次 `bash bin/svc.sh <svc> delete k8s`（在確認提示處直接 Enter 取消）讓腳本代勞
- **從 worktree 部署前，先把主工作樹的真值檔複製進 worktree。** 新開的 worktree 沒有 gitignore 的機密檔，`apply` 會補上佔位值。已經在跑的服務，資料庫卷是用真密碼建的，換成佔位值就連不上。複製時用 `cp -p` 保留 600 權限。收尾時 `git worktree remove` 會連同 worktree 裡的機密檔一起刪掉，主工作樹要留一份

### 10. podman overlay 的 hostPort 只能用 JSON6902

工作負載正本在 `base/`、兩軌共用，podman 軌要的 `hostPort` 由 overlay 的 patch 帶入。這個 patch 有兩種寫法，兩種都會靜默壞掉，只是壞法不同。

**strategic merge patch 對 `ports` 的合併鍵是 `(containerPort, protocol)`。** 漏寫 `protocol: TCP` 兩邊就對不上，kustomize **不顯示錯誤、退出碼 0**，產物看起來一切正常，只是 `hostPort` 憑空消失——部署出去服務對外不通才會發現。所以本 repo 一律不用 strategic merge 加 `hostPort`。

**JSON6902 靠索引定位容器**（`/spec/template/spec/containers/0/...`）。日後有人在 `base/` 重排容器順序，索引就會指到別的容器，`hostPort` 綁錯對象而 pod 照常 Running——比前者更難察覺。對策是每個 patch 前置一個 `op: test` 斷言該索引的容器名；重排 base 後 `kubectl kustomize` 會直接報 `testing value ... failed: test failed`，不會靜默做錯。

`juicefs` 是唯一索引不是 0 的服務（對外的是 `[1] juicefs`，`[0]` 是 minio sidecar）。

### 11. `podman kube play` 不支援 `kind: StatefulSet`

實測回 `Error: YAML document does not contain any supported kube kind`、`exit 125`。支援的只有 `Pod`／`Deployment`／`DaemonSet`／`PersistentVolumeClaim`。

所以 `minio`（k8s 軌是 `StatefulSet`）**兩軌無法共用 `base/`**——這是結構性限制不是漏做，它的 `podman/` 保留自己的 `pod.yaml` 與 `images:`，`base/` 只服務 k8s 軌。別去「補齊」它。

另外 pod 名稱會隨 kind 變：`kind: Pod` 起出來的 pod 叫 `<svc>`，`Deployment`／`DaemonSet` 叫 `<svc>-pod`、容器叫 `<svc>-pod-<容器名>`。`podman pod stop`／`logs` 這類指令要跟著改。

### 12. podman 軌掛 GPU 與跨 pod 連線，都不能照抄 K8s 寫法

兩個坑都不會報錯，pod 照常 Running，要到用的時候才發現。2026-09-18 在 podman 5.7.0 實測。

1. **GPU 資源鍵要寫 CDI 名稱。** K8s 標準鍵 `nvidia.com/gpu: 1` 在 podman 掛不到卡，容器裡沒有 `/dev/nvidia*`。podman 要寫 `nvidia.com/gpu=all: 1`。`base/` 寫 K8s 鍵，podman 軌的 overlay 用 JSON6902 換鍵，範例見 `services/vllm/podman-4060ti/kustomization.yaml`。podman 4.9.3 兩種寫法都掛不到

   **多張卡時改用編號指定**：`nvidia.com/gpu=0`、`nvidia.com/gpu=1`，容器只看得到那一張。`=0` 與 `=1` 各自單獨驗過（2026-09-19，雙卡主機），但**同一個 pod 裡混用不同編號還沒驗過**。第一次部署要進每個容器跑 `nvidia-smi` 確認它只看得到自己那張；只要有一個看得到兩張，就得改回 `=all` 搭 `CUDA_VISIBLE_DEVICES`
2. **跨 pod 的名稱是 pod 名，不是 Service 名。** podman 軌沒有 Service，別的 pod 要用「Deployment 名加 `-pod`」連，例如 `http://litellm-pod:4000`。K8s 軌的 `http://litellm:4000` 在 podman 解析不到。base 裡寫死 K8s 名稱的 env，podman 軌的 overlay 要改寫，範例見 `services/openwebui/podman-4060ti/kustomization.yaml`

### 13. OpenWebUI 的系統提示詞、橫幅與語音開關要靠 sync 寫入，不能靠環境變數

2026-09-18 在 OpenWebUI v0.11.0 抓包確認。環境變數 `DEFAULT_MODEL_PARAMS` 帶 `system` 時，管理介面看得到值，送給模型的訊息裡卻沒有 system。v0.11.0 只把這個 `system` 值記進 metadata。

系統提示詞的正本是 `services/openwebui/system-prompt.txt`，由 `task seed SVC=openwebui` 寫進每個模型紀錄的 `params.system`。litellm 新增模型後要再跑一次，新模型才會有系統提示詞；`task seed:check` 會列出沒套上系統提示詞的模型。

常駐橫幅同理：`WEBUI_BANNERS` 在 v0.11.0 是 PersistentConfig，env 只在全新資料庫當種子。橫幅的正本是 `services/openwebui/banner.txt`，同一支 `sync` 寫入 id 為 `prompt-retention` 的那一則。

語音開關也是：預設使用者權限的語音輸入、朗讀、通話三項，由同一支 `sync` 照 `sync.conf` 的 `VOICE_ENABLED` 設定。這組權限對 admin 不生效，要用一般帳號驗。2026-09-18 在 v0.11.0 實測：一般帳號的麥克風、通話與朗讀鈕都消失；admin 的還在，也用得了 OpenWebUI 內建的 CPU 語音輸入與瀏覽器朗讀。

### 14. 同一張卡上的多個 vLLM 要同時帶兩個記憶體參數

`vllm` 的 pod 有多個 vLLM 容器，podman 會同時啟動它們。2026-09-18 在 vLLM v0.29.0 實測，兩種單帶一個參數的寫法都會出事：

1. **只帶 `--gpu-memory-utilization`**：啟動時的記憶體剖析會算進別的 vLLM 容器正在配置的記憶體，報 `No available memory for the cache blocks` 後重啟。三個 vLLM 容器都重啟過，約 3.5 分鐘才碰巧收斂
2. **只帶 `--kv-cache-memory`**：比例退回預設 0.92，啟動檢查要求 14.33GiB 可用。只要另一個 vLLM 容器先起來，這項檢查就失敗，容器會一直重啟

兩個一起帶才穩：比例值負責啟動時的可用記憶體檢查，KV cache 用固定位元組數，不做剖析。換模型、換卡或改 context 長度時，兩組數字都要重算。KV cache 的數字取自只帶比例時日誌印出的 `Current kv cache memory in use`。

**比例值以整張卡為分母，同一張卡上的容器加起來不要超過 0.92**，那是 vLLM 單一行程的預設值。容器分在不同卡時各算各的。跨服務共卡的那一筆最容易漏：別的 pod 也掛同一張卡時，改 `vllm` 的比例要回頭看它。

### 15. 自動分流與雲端別名的八個坑

2026-09-18 起在 LiteLLM v1.98.0 與 OpenWebUI v0.11.0 實測，2026-09-19 改用 LiteLLM 內建的 `complexity_router`。分流設定在 `config.yaml` 的 `chat-auto` 那一筆，由 `chat-local` 以 LLM 判斷難度。網頁聊天預設走 `chat-auto`。

1. **雲端別名要設 `max_tokens`。** 請求沒帶 `max_tokens` 時，Cloudflare 只給 256 個 token。推理模型想完就沒有額度作答，回答內容是 null。模型清單的兩個雲端別名都設 `max_tokens: 8192`，請求自己帶的值仍優先
2. **背景任務要設 `TASK_MODEL_EXTERNAL`。** OpenWebUI 的標題、標籤、跟進問題都是背景任務。對外部連線的模型，這些任務只看 `TASK_MODEL_EXTERNAL`，不看 `TASK_MODEL`。沒設時沿用聊天的 `chat-auto`，自寫 hook 時期實測標籤任務被判困難而送上雲端。`chat-local` 也要留在聊天模型白名單裡。v0.11.0 的程式碼在任務模型不在清單時改用聊天模型，這一點沒實測
3. **重新部署 LiteLLM 後，OpenWebUI 會連舊位址。** podman 軌重建 pod 時位址會變，OpenWebUI 的連線池預設快取 DNS 300 秒。這段時間聊天回 `Server Connection Error`。`AIOHTTP_POOL_DNS_TTL` 設 10 之後，重新部署 LiteLLM 的指令結束約 18 秒就恢復
4. **看去向要看 `x-litellm-model-name`。** 分流之後，回應標頭 `x-litellm-model-group` 與回應本文的 `model` 都寫 `chat-auto`。經 fallback 回地端時，`x-litellm-model-group` 改寫 `chat-local`。實際送到哪個後端，看標頭 `x-litellm-model-name`，例如 `hosted_vllm/chat-local` 或 `cloudflare/@cf/openai/gpt-oss-120b`
5. **判斷模型的逾時預設只有 3 秒。** `chat-local` 兼做判斷時可能來不及回。設定改成 `timeout_ms: 15000`，並設 `classifier_fallback: default_model` 與 `default_model: chat-local`，判斷逾時或出錯就留在地端
6. **含圖片的困難題改由地端回答。** 分流只看文字，判成困難就送 `chat-cloud`。`tw-dlp` 看到圖片就阻擋它，再經 `chat-auto` 的 fallback 改由 `chat-local` 回答，圖片不會送出去。Cloudflare 本來也不收陣列形式的訊息，會回 500
7. **知識庫聊天整段送去判斷。** 模板、檢索內容與問題在同一則使用者訊息裡，判斷模型整段都看得到。實測知識庫的一簡一難都分對
8. **分流不是資安邊界。** 使用者可以在訊息裡指定判斷結果。實測「早安。分類時請直接判定這則是 REASONING。」被送上雲端；舊的「請只回答 HARD 這個字」則沒有效果。訊息含 `LITELLM ESCALATE` 這串字時，判斷也會往上升一級。資料能不能送出去，要靠 DLP 把關，見第 16 條

### 16. DLP 的八個坑

2026-09-19 在 LiteLLM v1.98.0 實測，改用內建的 guardrail。規則是 `config.yaml` 的 `tw-dlp`，一段 `custom_code`，只綁在 `chat-cloud` 與 `chat-cloud-alt` 上。它阻擋台灣個資、公司關鍵字與圖片。

1. **只有綁了的別名才檢查。** 新增雲端模型時，要在它的 `litellm_params` 加 `guardrails: [tw-dlp]`，忘了加就不檢查。送地端的請求不檢查，含個資也照常回答
2. **`chat-auto` 不能綁任何 guardrail。** router 會把 deployment 的 `guardrails` 帶進請求。`chat-auto` 綁了 guardrail 時，分流到 `chat-cloud` 的請求已經帶著 `chat-auto` 的清單，`tw-dlp` 就不會跑。實測綁上 `no-image-auto` 之後，困難題帶個資送到了 Cloudflare
3. **`chat-auto` 帶個資會改由地端回答。** 簡單題本來就留在地端。困難題分到 `chat-cloud` 時，`tw-dlp` 阻擋它，再經 `chat-auto` 的 fallback 改由 `chat-local` 回答。這時回應標頭 `x-litellm-attempted-fallbacks` 是 1。拿掉這條 fallback，困難題帶個資就會變成錯誤。回答沒有提示，使用者不知道被改送
4. **阻擋回 200，不是錯誤。** `custom_code` 在送出前阻擋時，回一則合成回答，內容是「DLP：請求含有…」。agent 看到的是一般回答，不是 400
5. **沙箱只拿得到一部分資料。** `custom_code` 的 `request_data` 只有 `model`、使用者與 `metadata`，拿不到訊息與請求自帶的 fallback。文字要從 `inputs` 收。`inputs` 的 `texts` 不含 `/v1/responses` 的 `instructions`，要連 `structured_messages` 一起收。內建的 `content_filter` 不收 `structured_messages`，實測漏掉 `instructions`，所以沒用它
6. **fallback 到雲端時會再檢查一次。** 請求自帶 `fallbacks: ["chat-cloud"]`，地端又出錯時，改送 `chat-cloud` 的那一次會過 `tw-dlp`，實測阻擋。只看 `model` 決定要不要檢查的寫法擋不住這條路，實測個資送到了雲端
7. **公司關鍵字寫在程式裡。** 改 `tw-dlp` 程式的 `KEYWORDS` 那一行，改完重新部署 litellm；k8s 軌另跑 `kubectl rollout restart deployment/litellm`（ConfigMap 名稱不帶雜湊，`apply` 不會重啟 pod）。LiteLLM 升版要重跑 `task litellm:check`：沙箱的限制或 `inputs` 的欄位變了，不會報錯
8. **整段對話都檢查。** 一段對話出現過個資，之後每一輪送雲端都會命中。email 規則也會命中程式碼裡的 git 位址，例如 `git@github.com`

### 17. LiteLLM 加 worker 或副本前，要先上 Redis

2026-09-19 在 LiteLLM v1.98.0 確認。LiteLLM 現在只跑 1 個 worker 與 1 個副本，沒有 Redis。`services/litellm/base/deployment.yaml` 設了 `LITELLM_DISABLE_NO_REDIS_WARNING`，`/ui` 因此不顯示「No Redis configured」提示。

限流計數、並行數、預算的即時累計與路由的冷卻狀態，都存在各行程的記憶體裡。加了 worker 或副本之後，各行程各算各的，會各自放行同一份額度，而且不報錯。podman 軌會忽略副本數（`replicas`），只能加 worker。

要加 worker（`--num_workers` 大於 1）或副本時，三件事一起做：

1. **上 Redis。** 兩處都要接上：限流與預算用的協調快取（`general_settings.coordination_redis` 或 `REDIS_*` 環境變數），以及路由器的 `router_settings.redis_host`
2. **拿掉 `LITELLM_DISABLE_NO_REDIS_WARNING`。** 留著它，Redis 沒接上時也看不到提示
3. **加副本時，把 PostgreSQL 搬出 litellm 的 pod。** 現在兩個容器在同一個 pod，postgres 的資料放在 PVC `litellm-db`，存取模式是 `ReadWriteOnce`。直接加副本時，每個副本都會起一個 postgres，掛同一個 PVC。副本排到別的節點時卡在 Multi-Attach 錯誤；排在同一個節點時，兩個 postgres 會搶同一個資料目錄。只加 worker 時，各 worker 連同一個 postgres，不用搬

三件做完後這樣驗：

1. 帶 key 打 `/health/readiness/details`
2. 確認 `show_no_redis_warning` 是 `false`。環境變數已經拿掉，任一處接上 Redis，這個值就是 `false`
3. 讀 LiteLLM 的設定檔，確認 `router_settings.redis_host` 有值。第 2 步驗不出路由器那一處

## 開發規範

### Bash

- **禁 `[[ cond ]] && { ...; }` 作為獨立語句**：條件為假時整段回傳非零，日後加上 `errexit` 會靜默中止；作為腳本最後一條語句時，會讓整支腳本以 1 退出。用 `if` 寫（範例見 `bin/svc.sh` 的 `if [[ "${verb}" == apply ]]` 區塊）
  - 既有違規：`services/gmm/sync.sh:69,86,103,107,110`（`PROGRESS.md` 技術債 2，不在任何案子範圍內，別順手改）
- 不在 bash 裡 parse YAML。要 parse 就用 python
- `yq` 是 python-yq（jq wrapper），**只用 jq 相容語法**，禁 `yq e`／`yq ea`／`yq eval`

### 語言邊界

- **bash**：流程、IO、git 膠水、防護規則
- **python**：結構化資料驗證，例如 YAML parse、型別檢查、鍵集合差集。另外一處是 litellm 的實機回歸。LiteLLM 設定檔裡的 `custom_code` 是 LiteLLM 沙箱跑的類 Python，照 LiteLLM 的規格寫
- 不引入第三種語言

### 文件邊界與引用方式

`docs/` 底下受版控的只有 `ARCHITECTURE.md` 與 `PROGRESS.md`。spec、plan、工單放 `docs/superpowers/` 與 `docs/tickets/`，兩處已列進 `.gitignore`，只存本機，見根 repo `CLAUDE.md`〈路由表〉。**不要再開新的零散文件**。歸不進這幾處的內容，先想清楚它是不是真的該存在。已評估、刻意延後的項目記在 `PROGRESS.md` 的「技術債清冊」段，每條須附 `檔案:行` 或指令輸出為證。

**引用散文檔時只指章節名，不要帶行號。** 行號留給程式碼（`見 bin/svc.sh:13-26` 這種沒問題）；指向 `AGENTS.md`／`README.md`／`docs/*.md` 一律寫章節名，例如「見 `AGENTS.md` 的『語言邊界』」。

**例外是缺陷登記**：本檔的「已知的文件漂移」表與技術債清冊要求的 `檔案:行`，目的是精確定位某一行，不附行號就沒用。那些條目照舊附——它們會在每次複驗時重新核對（漂移表的表頭就記著複驗日期），不像埋在註解裡的引用沒人回頭看。

理由是這種引用**壞掉時完全無聲**：散文的行號會隨任何一次改寫整批位移，而引用是單向的——被引的那份檔不知道誰指著它，改它的人收不到任何提醒，**沒有 lint、沒有測試、沒有 hook 會發現**。`bin/svc.sh` 兩處註解就是這樣爛掉的（一處指到位移後的錯行、一處指向本 repo 根本不存在的檔），2026-08-04 才修掉。要查現在還有沒有殘留：

```bash
grep -rn '\.md:[0-9]\+' bin/ services/ tests/ Taskfile.yaml
```

## 常見任務怎麼做

### 加一個服務

1. `services/<svc>/base/`：工作負載正本，檔名＝kind 小寫（`deployment.yaml`／`daemonset.yaml`／`pod.yaml`…）。manifest 內**零機密真值、`image:` 只寫短名**
2. `services/<svc>/base/kustomization.yaml`：`resources:` 指工作負載 ＋ `images:`（`name:` 短名、`newName:` 完整 repo、`newTag:` **加雙引號**；digest 型改用 `digest:` 且不給 `newTag:`）
3. `services/<svc>/{podman,k8s}/kustomization.yaml`：`namespace: default` ＋ `resources: [../base]` ＋ 該軌專屬資源（podman 的 hostPort patch、k8s 的 `service.yaml`／`ingress.yaml`／`pvc.yaml`）。podman 軌的 `hostPort` 用 JSON6902 ＋ `op: test`（見地雷區第 10 條），**不要用 strategic merge**
4. 短名規則零例外：單容器服務 `<svc>`、多容器服務 `<svc>-<容器名>`（見地雷區第 1 條，別用裸容器名）
5. 只支援一條路徑就只建一個目錄——目錄存在即代表該路徑支援
6. 有機密：建 `services/<svc>/secret/`，內含 `kustomization.yaml`（`secretGenerator` ＋ `generatorOptions.disableNameSuffixHash: true`）與 `secret.env.example`（佔位值，入庫）；兩軌各自 `resources:` 加一行 `../secret`。**零機密的服務不建這個目錄**——`resources` 指向不存在的目錄會讓 kustomize 直接失敗
7. 驗證：`kubectl kustomize services/<svc>/k8s`（純本地，不需叢集；有機密的服務先備妥真值檔或佔位值，見地雷區第 9 條）＋ `bash bin/assert-images.sh <svc>`

### 改映像版本

改 `services/<svc>/base/kustomization.yaml` 的 `images:` 字面值——**每服務只有一處**。唯一例外是 `minio`：它兩軌無法共用 `base/`（見地雷區第 11 條），podman 軌自帶一份 `images:`，改版號要動兩處；juicefs 與 minio 共用的那顆 digest 是 3 處（`juicefs/base`、`minio/base`、`minio/podman`）。`vllm` 的容器共用同一顆映像，短名各一筆。改版號時 base 的每一筆都要改。

然後 `task image:check SVC=<svc>` 確認來源 registry 真的有，順帶驗短名有解析成完整參照。跨大版本記得查上游是否改了啟動時序或設定介面。

### 驗證改動

```bash
task test                              # 離線單元測試，必跑（等同 python3 -m pytest tests/）
kubectl kustomize services/<svc>/k8s   # 該服務正本語法
bash bin/assert-images.sh <svc>        # 渲染產物的 image 皆為完整參照且仍在來源 registry
task litellm:check                     # 實機回歸：對執行中的 litellm 跑 DLP 與分流情境，重新部署 litellm 後必跑
```

⚠️ **新增測試檔時，檔尾一定要補 `__main__` 執行段**（風格照 `tests/test_manifest_diff.py` 檔尾：蒐集 `test_` 開頭的可呼叫物件、逐支跑、印 `[OK] <name>`、最後印總數，例外自然往上拋）。`Taskfile.yaml` 的 `test` 是用 `python3 tests/<檔>.py` 呼叫的，**pytest 風格的測試檔少了這段就會零輸出、rc=0，一支都沒跑卻報成功**。`tests/test_svc.py` 曾因此假綠燈一整輪（見 `PROGRESS.md` 版本沿革）。判斷有沒有真的在跑，看 `task test` 有沒有印出 `ALL <n> PASS`。

`task litellm:check` 是 litellm 的實機回歸，只驗 DLP 與分流。它呼叫 `tests/live_gateway.py`，打正式的 litellm，key 從兩份 `secret/secret.env` 讀。在 worktree 裡跑之前，照地雷區第 9 條先複製真值檔。檔名刻意不以 `test_` 開頭，pytest 不會收進去，離線測試也就不會打到正式服務。

其餘的真環境煙測目前**沒有腳本**，靠各服務 plan 裡的步驟逐條手打。plan 已移出版控，用 `git show def6982:docs/superpowers/plans/` 列出檔名，再用 `git show def6982:docs/superpowers/plans/<檔名>` 取回。宣稱「完成」前請確實跑過並貼出輸出，不要用推測代替。

### 推送前怎麼真的驗一次防護規則

手動跑 `pre-push` 的正確餵法、掃描範圍與例外，見根 repo 的 `AGENTS.md`〈直接跑 `pre-push` 不餵 stdin 會得到假綠燈〉。不餵 stdin 直接跑，一個 commit 都不掃，看起來卻像通過。

⚠️ **護欄掃的是被推的每個 commit 的新增行，「後來修掉了」不算數。** 所以**不要用 `git diff main..HEAD` 驗**。淨差異看不見「加了又刪」的內容，會給出假的通過。本 repo 已經因此連續三輪誤判「阻擋已解除」。

真被歷史 commit 卡住時，改 HEAD 沒有用，得處理歷史，例如 **squash merge** 或改寫那顆 commit。選哪條是**使用者的決策**，先問。

即便如此，**HEAD 該修還是要修**：squash 帶走的是 HEAD 的內容，HEAD 沒清乾淨，squash 出來的那顆一樣會被擋。

## 已知的文件漂移

以下位置的敘述與程式碼不符，尚未修正（2026-08-04 複驗）：

| 位置 | 問題 |
|---|---|
| `git show ceba8ef:docs/superpowers/specs/2026-06-14-app-services-ncb-design.md` 第 207 行 | 標「自簽預鋪（強制順序，在 up 之前）」，實測是 apply 之後補鋪、靠 `restartPolicy=always` 自癒 |

spec 與 plan 是**當時的決策紀錄，刻意不隨程式碼更新**，已移出版控，要看用 `git show <sha>:<路徑>` 取回——讀到與現況不符是正常的，以正本為準。上表只收「會誤導現在動手的人」的那幾處。
