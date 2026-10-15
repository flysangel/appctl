# app-services 架構

本檔講 repo 怎麼組織、部署怎麼跑、機密怎麼進容器，以及為什麼這樣設計。用法見 [README.md](../README.md)。

## 1. 定位與邊界

每個服務一個目錄，目錄裡是各軌的 kustomize 正本。映像參照與機密宣告都寫在 `kustomization.yaml` 裡。一支控制腳本只做守衛、機密補位與確認，部署交給 `kubectl apply -k` 與 `podman kube play`。

沒有中央引擎、沒有自訂 schema、沒有模板語言，也沒有字串替換。

- 上游：images repo 建好的映像推上 registry 後，版號直接寫進各服務 `base/kustomization.yaml` 的 `images:`。本 repo 不讀 images 的版本清單，也沒有 `versions.conf`。
- 上游：k8s 軌部署到 infra-k8s 建好的叢集。
- 叢集級 addon（LB、Ingress、cert-manager 等）在 infra-k8s，不在本 repo。本 repo 只放使用者面的服務。
- push 護欄不算上游依賴。它不釘版，由工作區根 repo 管理。
- 不管公網 DNS 與 DDNS。服務只給信任網段用，Ingress 主機名一律 `<svc>.local`，工作區沒有 repo 維護公網名稱。

## 2. 目錄結構

```
Taskfile.yaml              唯一入口，task --list-all 列清單

bin/
  svc.sh                   部署控制腳本
  assert-images.sh         讀 kubectl kustomize 產物：每個 image 都要是完整參照，而且還在來源 registry
  manifest-diff.py         語意零漂移比對器（大改時用，不在日常流程）

.gitattributes             腳本強制 LF
tests/                     離線單元測試，加上 live_gateway.py（litellm 實機回歸，task litellm:check 呼叫）

services/<svc>/
  base/                    工作負載正本，兩軌共用；檔名是 kind 小寫
    kustomization.yaml     resources: 工作負載，加 images:（映像完整參照）
    config.yaml            （僅 litellm）模型清單與 DLP 設定，整份進版控，產生 ConfigMap litellm-config
  {podman,k8s}/            overlay，可直接 kubectl kustomize；podman 軌可依主機分軌，例如 podman-4060ti
    kustomization.yaml     namespace:、resources: [../base]；podman 軌另有 hostPort 的 JSON6902 patch；
                           有機密者另加 ../secret
  secret/                  （有機密的服務才有）機密宣告，兩軌共用
    kustomization.yaml     secretGenerator 加 disableNameSuffixHash: true（進版控）
    secret.env.example     機密範本（進版控，佔位值）
    secret.env             機密真值（gitignored）
    config.yaml{,.example} （僅 quay）檔案型機密，真值 gitignored、範本進版控
  sync.sh                  （選配）營運工具，哪些服務有：ls services/*/sync.sh
  sync.conf.example        連線目標與非機密參數的範本，哪些服務有：ls services/*/sync.conf.example；真檔 gitignored
  sync.secret.env.example  sync 用密碼的範本，哪些服務有：ls services/*/sync.secret.env.example；真檔 gitignored
  mapping.list.example     （僅 gmm）連線清單範本；真檔 mapping.list 與 seed 產出的 random.csv 都 gitignored
  agents/                  （僅 litellm）coding agent 的設定範例與發 key 的告知文字，不進 kustomize
```

`build/` 中介產物已經不存在。要看渲染結果，現跑 `kubectl kustomize services/<svc>/<軌>`；base64 機密不落地成檔案。單一服務的完整工作負載要從 `base/` 讀起，overlay 只帶該軌的差異。

`services/finetune/`、`services/vllm/finetuned-4060ti/` 與對應的測試刻意不進版控，只存本機。

## 3. 依賴與資料流

### 部署管線

`task apply SVC=<svc> TRACK=<軌>` 呼叫 `bin/svc.sh`，依序做三件事：

```
① 守衛       services/<svc>/<軌>/ 目錄存在？一道檢查同時涵蓋服務名打錯與該軌不支援
      ↓
② 機密補位   secret/{secret.env,config.yaml} 不在時，從同名 .example 複製佔位值
      ↓
③ 執行       k8s：kubectl apply -k ｜ podman：kubectl kustomize | podman kube play
```

渲染不在腳本裡。`apply -k` 由 kubectl 內建的 kustomize 直接吃目錄；podman 軌因為 `podman kube play` 不懂 kustomize，才多一次 `kubectl kustomize`，把產物經 stdin 餵過去。

podman 軌的 `kube play --replace` 會砍掉同名 pod 整個重建：進行中的連線全斷，容器層的狀態消失，具名卷保留。所以改了 ConfigMap 或 `secret.env` 之後重跑 `apply`，podman 軌直接生效；k8s 軌的 Secret 與 ConfigMap 名稱不帶雜湊，`apply` 不會重啟 pod，理由見〈設計取捨〉。具名卷到 `delete` 才刪。

`delete` 多兩件事：先要求輸入服務名確認（沒有 tty 時 `read` 讀到 EOF 就中止），再跑 `kubectl delete -k --ignore-not-found` 或 `podman kube down --force -`。

- k8s：`StatefulSet` 的 `volumeClaimTemplates` PVC 由叢集動態建立，不在 manifest 內，不隨 `delete` 移除。腳本刪完會列出這些 PVC，並印出手動清除的指令。
- podman：podman 4.9.3 對含 `PersistentVolumeClaim` doc 的 yaml 跑 `kube down --force`，卷已刪掉卻以 125 結束。腳本對這個假錯誤做了容錯，理由寫在 `bin/svc.sh` 的註解。

### 映像參照：短名 → 完整參照

manifest 的 `image:` 寫短名（例如 `filebrowser`、`grafana-mariadb`），完整參照只寫在 `kustomization.yaml` 的 `images:`。kustomize 渲染時把短名換成完整參照。`bin/assert-images.sh` 讀渲染產物，任一 `image:` 沒有換成完整參照就回 1；一個 image 都沒抓到也算失敗。

### 機密：四種形式

正本目錄不放機密真值，但有機密宣告。`services/<svc>/secret/` 自帶一份 `kustomization.yaml`，裡面是 `secretGenerator`，兩軌用 `resources: [../secret]` 引用同一份。

| 形式 | 真值放哪 | 怎麼進容器 | 誰有 |
|---|---|---|---|
| env 型機密 | `services/<svc>/secret/secret.env`（gitignored） | `secretGenerator` 的 `envs:` 產生 Secret；正本用 `secretKeyRef`、`envFrom` 引用 | 有 `secret/` 的服務 |
| 檔案型機密 | `services/<svc>/secret/config.yaml`（gitignored），範本 `.example` 進版控 | `secretGenerator` 的 `files:` 直接吃真值檔；workload 用 `volumes[].secret` 掛成檔案 | 只有 quay（Secret `quay-conf`） |
| 非機密設定檔 | 真檔放在產生它的 kustomize 目錄（quay、litellm 在 `base/`；ncb 只在 `podman/`，因為只有 podman 軌） | `configMapGenerator`；workload 用 `configMap.name` 掛載 | quay（`initdb.sql`）、ncb（`default.conf`）、litellm（`config.yaml`） |
| sync 工具的本機檔 | `services/<svc>/sync.conf`、`sync.secret.env`，gmm 另有 `mapping.list`（含目標主機的帳密）與 seed 產出的 `random.csv`（自動產生的使用者密碼），全部 gitignored | 不進容器、不經 kustomize；`sync.sh` 在操作者主機讀 | 有 `sync.sh` 的服務；`mapping.list`、`random.csv` 只有 gmm |

kustomize 的 load restrictor 擋裸檔案路徑，不擋目錄型 resource。`envs: [../secret.env]` 會失敗，`resources: [../secret]` 可以：`resources:` 指向的目錄自成一個 kustomize root，`namespace:` 也會正確套到產生的 Secret。所以不需要 `--load-restrictor LoadRestrictionsNone`，`kubectl apply -k` 可以直接用，`delete -k` 也涵蓋 Secret。

litellm 的 `config.yaml` 不含機密：金鑰都寫成 `os.environ/` 參照，`KEYWORDS` 是示範值。它整份進版控，由 `configMapGenerator` 產生 ConfigMap `litellm-config`，名稱照 quay 的 `quay-pg-init` 關掉雜湊，manifest 才能寫死引用。

### 兩軌的差異

兩軌共用 `base/` 的工作負載與 `images:`，差別集中在下面四處：

| | podman | k8s |
|---|---|---|
| 儲存 | 具名卷：`persistentVolumeClaim.claimName` 經 `podman kube play` 對應到本機具名卷 | PVC（叢集 StorageClass 動態供應）；`StatefulSet` 另有 `volumeClaimTemplates` |
| 對外 | hostPort，綁主機所有介面 | Service 加 Ingress |
| 跨 pod 名稱 | Deployment 名加 `-pod`，例如 `litellm-pod`；base 寫 Service 名的 env 由 overlay 改寫（`litellm` 的 `config.yaml` 例外，直接寫 podman 名） | Service 名，例如 `litellm` |
| GPU 資源鍵 | CDI 名稱 `nvidia.com/gpu=all`，由 overlay 從 base 的 K8s 鍵換過來 | `nvidia.com/gpu` |

同一個 pod 內的容器互連一律寫 `127.0.0.1`（gmm 的 guacd 與 mariadb、grafana 的 mariadb、quay 的 postgres 與 redis），兩軌共用，不需 overlay 改寫；把 DB 拆成獨立 Deployment 時才要換成 Service 名。ncb 的上游是 `host.containers.internal:9000`，即主機上 minio podman 軌的 hostPort：經主機 hostPort 連，不走 pod 網路名，上游不在 podman 裡也能代理。

拓樸不同的服務只有 `minio`：podman 是單節點 `Pod`；k8s 是 `StatefulSet` 加多個 `volumeClaimTemplates` 與 headless Service。

### 卷

卷直接寫在正本 workload 的 `volumes:`、`volumeMounts:`，標準 k8s 語法。

- 共享卷：同一個 pod 的多個容器掛同一個 volume。gmm 的 guacamole 與 guacd 共用 `recordings`：`RECORDING_ENABLED=true` 時 guacd 寫錄影檔、guacamole 讀來回放。ncb 的 nginx 與 certbot 共用 `certbot`：憑證與 ACME webroot（`/etc/letsencrypt/www`）都在裡面，是兩容器唯一的耦合點。沿用 `ReadWriteOnce`。
- 暫時卷：`emptyDir` 加 `medium: Memory` 與 `sizeLimit`，用在 `/dev/shm`（xadm-lxqt、xadm-xfce）。pod 重啟就清空。
- PVC 容量寫死在 `k8s/pvc.yaml`；minio 寫在 `base/statefulset.yaml` 的 `volumeClaimTemplates`。

## 4. 設計取捨

| 取捨 | 選了什麼 | 代價 |
|---|---|---|
| 動詞只有 `apply`、`delete`，不提供 `stop` | 命令式的暫停會讓叢集實況與 manifest 正本分岔，下次 `apply` 會靜默把服務拉回 | 要暫停得退回原生工具（`kubectl scale --replicas=0`、`podman pod stop <svc>-pod`）；`delete` 連卷刪，沒有「拆 pod 留卷」的動詞，要留卷只能繞過 `task`（待驗的做法列在 `docs/PROGRESS.md`） |
| 工作負載收在共用 `base/`，兩軌只寫差異 | 兩軌對齊 kind 後，kustomize 原生的 base 與 overlay 就抽得乾淨，不需要自訂抽象；版號每個服務只寫一處 | `minio` 對不齊（見〈已知的結構性限制〉）；podman 軌的 `hostPort` 要靠 overlay patch 帶入 |
| 映像參照直接寫在 `images:`，不做 `${REGISTRY_PREFIX}` 間接層，也不經 `envsubst` | 零字串替換，正本可以直接 `kubectl apply -k`；佔位用途只有 image 與 namespace，kustomize 原生欄位全部涵蓋 | 換 registry 要改每個服務的 `images:`；ncb 的 `server_name`、憑證路徑、上游三處寫死在 `default.conf`，只服務一個網域，多站要複製 server 區塊 |
| manifest 只寫短名，短名一律帶服務名 | 帶服務名的短名在 Docker Hub 官方庫都不存在，`images:` 漏列時一定失敗；規則沒有例外，不需要 lint 執法 | `grafana-grafana`、`quay-quay-app` 這類重複字樣讀起來笨拙 |
| 機密用 `secretGenerator` 宣告在 `secret/`，兩軌共用 | `apply -k`、`delete -k` 直接涵蓋 Secret，腳本不必自己建、自己刪 | 真值檔不在時整個部署流程（含純本地 `kubectl kustomize`）都失敗，要靠 `.example` 佔位值補位 |
| Secret 與 ConfigMap 名稱不加內容雜湊 | podman 的 secret store 沒有 owner reference 與 GC，雜湊輪替會留下永久孤兒；`delete -k` 也只刪得掉當下那個雜湊 | k8s 軌改機密或設定不會觸發滾動更新，要手動 `rollout restart`；podman 軌每次 `apply` 都 `--replace` 重建 pod，沒有這個問題。雜湊政策寫在共用的 `secret/`，不能分軌設定 |
| quay 的 `config.yaml` 不進版控 | `secretGenerator` 的 `files:` 不做變數替換，含密碼的設定檔只能整份當真值檔維護 | 換機器要照 `.example` 手抄並填回機密欄位；`.example` 補位只保證跑得起來，不保證跑得對 |
| PVC 容量寫死在 `k8s/pvc.yaml`（minio 在 `volumeClaimTemplates`） | 單一環境下這個值不會變；抽成參數會讓它看起來可以隨時調整 | 要改容量得改這些檔，而且只在第一次部署生效 |
| 不留部署鉤子（`post-apply.sh`） | 這個機制存在過三個版本，始終沒有任何服務用到 | 真的需要「部署後才能做的初始化」時要重新設計，目前由 `task seed` 扮演這個角色 |
| 映像優先用 tag 釘，不用 digest | 和其他服務的寫法一致，比較好讀 | 同一個 tag 可能被重推，不像 digest 保證不可變；全部容器 `imagePullPolicy: IfNotPresent`，同 tag 重推後已有快取的節點或 podman 主機不會重拉，`apply` 成功但跑的是舊層，要清本機映像或改版號。minio 與 juicefs 還是 digest，因為 mirror 沒有版本 tag |
| push 護欄是黑名單、盡力而為的第二道防線 | 主防線是真值與浮動參照不寫進版控；黑名單只能擋已知的樣式 | 沒列進黑名單的寫法擋不到 |
| `assert-images.sh` 放在 `bin/`，不進 push 護欄 | 共用護欄只收全部 repo 都適用的檢查，它要 `kubectl` 與 `skopeo`，只對本 repo 有意義 | 這道守衛靠人記得跑，不是靠機制 |
| 自動分流用 LiteLLM 內建的 `complexity_router`，由 `chat-local` 用 LLM 判斷難度 | 實測 LLM 判斷比 heuristic 加中文關鍵字準：heuristic 沒命中關鍵字的困難題都留在地端 | 分流不看圖片；知識庫聊天整段送去判斷；MEDIUM 送地端，部分困難題會留在地端；判斷由 LiteLLM 內部發出，升版要重驗 |
| DLP 用 LiteLLM 內建的 guardrail：`custom_code` 的 `tw-dlp`，只綁在雲端別名上 | 實測多個候選，只有模型層 guardrail 沒有外洩，也不會連送地端的請求一起擋；fallback 到雲端時也會檢查。內建的 `content_filter` 不收 `structured_messages`，漏掉 `/v1/responses` 的 `instructions`；只看 `model` 決定要不要檢查的寫法，擋不住 fallback 到雲端的那一次 | 阻擋時回 200 加合成回答，不是錯誤；`chat-auto` 改由地端回答時沒有提示；新增雲端模型忘了綁就不檢查 |
| podman 軌的 hostPort 都不設 hostIP，綁主機全部介面，對實驗室網段開放 | 實驗室其他主機要直接用網頁與 agent（litellm、openwebui）；manifest 不寫死主機 IP，換主機照用 | 每個 hostPort 都是 HTTP 明文：`/ui` 的 master key、gmm 的登入密碼與遠端桌面帳密都明文傳送；主機沒有防火牆，連得到主機的網段都能連每個 hostPort |
| LiteLLM 不上 Redis，只跑 1 個 worker 與 1 個副本 | 限流、預算與路由狀態要加 worker 或副本才需要共用；LiteLLM 只轉送請求，推論在 vLLM，加了也不會變快 | 重新部署 LiteLLM 時服務中斷，沒有零停機 |
| 動詞固定（清單用 `task --list-all` 查），服務與軌用 `SVC=`、`TRACK=` 帶入，不每服務開一份 taskfile | 服務多了 taskfile 不會跟著長；`includes` 與 `dir:` 的路徑坑一次都不用踩 | `task --list-all` 看不出有哪些服務，要 `ls services/` |
| `bin/svc.sh` 不做 preflight，只驗 `services/<svc>/<軌>/` 目錄存在 | sudo、工具、埠占用、同名 pod 的檢查 kubectl 與 podman 自己都會報 | 錯誤訊息來自 kubectl 或 podman，不是腳本；hostPort 被占時建一半的 pod 會留著，見 `AGENTS.md`〈地雷〉 |
| 全部服務部署到 `default` namespace，腳本不建 namespace | 單一環境不需要用 namespace 隔離；`delete` 列 PVC 時可以寫死 `-n default` | overlay 的 `namespace:` 改成叢集沒有的名字，`apply -k` 直接失敗 |
| 營運工具（`task seed`）在操作者主機跑 `services/<svc>/sync.sh`，不放進叢集排程 | 機密從本機 gitignored 檔讀，manifest 不放真值，也不在執行期 clone repo | 要排程得靠主機的 cron；各 `sync.sh` 要的工具得裝在主機上（見 `README.md`〈前置需求〉） |
| quay 的 quay-app、postgres、redis 放同一個 pod | config.yaml 的 DB 與 redis host 都寫 `127.0.0.1`，兩軌共用同一份，不必像 litellm 由 overlay 改寫 | 三容器沒有啟動順序控制（待辦在 `docs/PROGRESS.md`）；quay-app 不能獨立擴 |
| podman 軌用具名卷，不用 hostPath | 免掉建目錄、`setfacl` 給容器 uid 寫入權、清理白名單、`.gitignore` 排除 runtime 目錄四件事；所有權與生命週期由 podman 管 | 資料不在 repo 目錄下，取檔要 `sudo podman volume inspect` 找 mountpoint，備份走 `podman volume export` |

### 測試只靠 bash 與 python

離線測試把 `KUBECONFIG` 指向不存在的路徑。`kubectl kustomize` 是純本地操作，不受影響；需要連叢集的操作會失敗，所以測試動不到真實資源。

`tests/live_gateway.py` 刻意不離線：它對執行中的 litellm 跑 DLP 與分流情境，比對回應標頭 `x-litellm-model-name` 判斷請求去了地端還是雲端。它每則都印結果，全部跑完才決定退出碼。

## 5. 已知的結構性限制

- `minio` 兩軌不能共用 `base/`：`podman kube play` 不支援 `kind: StatefulSet`（實測 exit 125）。它的 podman 軌自帶 `pod.yaml` 與 `images:`，改版號要動兩處。podman 4.9.3 的 `man podman-kube-play` 列的支援 kind 也沒有 Job 與 CronJob，所以本 repo 沒有週期性工作負載，週期動作寫進容器 `command`（ncb 的 certbot 每 12 小時 `renew`、nginx 每 168 小時 reload）。
- quay 用 `LocalStorage` 寫 `/datastorage/registry`，k8s 的 PVC 是 `ReadWriteOnce`，quay-app 只能 1 個副本。
- ncb 的 `server_name` 是 `ncb.local`：`.local` 是 mDNS 保留域（RFC 6762），ACME 不簽。手放進卷的憑證沒有 renewal 設定檔，certbot 容器的 `renew` 迴圈沒有東西可續，回 0 不報錯。憑證只能自簽或用內部 CA；要用 Let's Encrypt 得改成公網網域，而公網 DNS 不在本 repo 範圍。certbot 容器的去留列在 `docs/PROGRESS.md`。
- gmm 的 `REMOTE_IP_VALVE_ENABLED=true` 讓 guacamole 從 `X-Forwarded-For` 取來源 IP，`BAN_ENABLED` 的暴力登入封鎖才封得到人而不是封掉 Ingress。podman 軌前面沒有代理，客戶端自帶的 `X-Forwarded-For` 會被信任，可偽造繞過封鎖。
- `kadm`、`xadm-lxqt`、`xadm-xfce` 經 LoadBalancer 對外開 SSH 或 RDP，與叢集最高權（`cluster-admin`）之間只隔兩樣東西：映像內建的帳密，與一把建置時留在映像層裡的 bigred 無密碼私鑰。私鑰的來龍去脈見 images 的 `docs/ARCHITECTURE.md` 裡講映像層私鑰那條，消除列在 images 的 `docs/PROGRESS.md`。本 repo 沒有在執行期改密碼，也沒有關密碼登入。只能放在信任網段。
- `iperf3` 的 5201 埠、`juicefs` 的 S3 9005 埠沒有額外認證，只能放在信任網段。juicefs 的 metadata 是 SQLite，只能單寫；它的 minio sidecar 是單節點。
- 自動分流不是資安邊界。使用者可以在訊息裡指定判斷結果，資料能不能送出去只靠 DLP。
- `bin/svc.sh` 對 podman `delete` 的 exit 125 容錯還沒實際觸發過：現在 podman 軌的產物都沒有 `PersistentVolumeClaim` doc。
