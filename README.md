# app-services

**容器服務的部署集**。內容是各服務的 kustomize manifest 正本、一支部署控制腳本，以及少量營運工具。

每個服務都能走**兩條部署路徑（track）**，由同一份控制腳本、同一組動詞驅動：

| 部署路徑 | 用途 | 底層 | 拓樸 |
|---|---|---|---|
| `podman` | 單機／驗證 | `podman kube play`（需 `sudo`） | 與 k8s 軌同 kind（`Deployment`／`DaemonSet`；`ncb` 為 `Pod`、`minio` 不共用）＋ 具名卷（named volume）＋ hostPort |
| `k8s` | 正式／分散式 | `kubectl apply` | `Deployment`／`StatefulSet`／`DaemonSet` ＋ PVC ＋ Service ＋ Ingress |

## 服務一覽

| 服務 | 用途 | podman | k8s | 服務埠 | 持久卷 |
|---|---|:-:|:-:|---|---|
| `filebrowser` | 網頁檔案總管 | ✓ | ✓ | 4000 | data 10Gi |
| `gmm` | Guacamole 遠端桌面閘道（guacamole ＋ guacd ＋ mariadb） | ✓ | ✓ | 8080 | recordings 1Gi、db 2Gi |
| `grafana` | 儀表板（含 mariadb 後端） | ✓ | ✓ | 3000 | data 1Gi、db 2Gi |
| `iperf3` | 網路頻寬測試 server | ✓ | ✓ | 5201 | 無（無狀態） |
| `juicefs` | JuiceFS 分散式檔案系統（S3-gateway 形態） | ✓ | ✓ | 9005 | obj 10Gi、meta 2Gi |
| `kadm` | k8s 管理 SSH 堡壘（pod 內含 podman） | — | ✓ | 22100→22 | wk 10Gi |
| `litellm` | LLM 統一出口（litellm proxy ＋ postgresql） | ✓ | ✓ | 4000 | db 2Gi |
| `minio` | S3 相容物件儲存 | ✓ | ✓ | 9000、9001 | data（k8s 為 `volumeClaimTemplates`） |
| `ncb` | nginx ＋ certbot TLS 反向代理前門 | ✓ | — | 80、443 | ncb-conf、certbot |
| `openwebui` | Open WebUI 對話前端（webui ＋ postgresql 含 pgvector，主資料庫兼向量庫） | ✓ | ✓ | 8080 | data 10Gi、db 5Gi |
| `quay` | Project Quay 容器映像倉庫（quay-app ＋ postgresql ＋ redis） | ✓ | ✓ | 8080 | pg-data 2Gi、quay-data 5Gi |
| `static-nginx` | 純靜態網頁 server | ✓ | ✓ | 80 | data 10Gi |
| `vllm` | 地端推論（vLLM，掛 GPU；chat ＋ embed ＋ rerank 同卡共存） | ✓ | — | 8000、8001、8002（不開 hostPort） | models（具名卷） |
| `xadm-lxqt` | k8s 圖形管理工作站（LXQt via xrdp） | — | ✓ | 33890→3389、22100→22 | wk 10Gi |
| `xadm-xfce` | k8s 圖形管理工作站（XFCE4 via xrdp） | — | ✓ | 33890→3389、22100→22 | wk 10Gi |

只建一個部署路徑目錄的服務，就代表另一路徑不支援：`ncb` 的反向代理與 TLS 在 k8s 由叢集 Ingress ＋ cert-manager 負責，`kadm`／`xadm-*` 本質上是叢集內的管理工作站。`vllm` 的 k8s 路徑還沒做，工單已移出版控，用 `git show d91bcf3:docs/tickets/2026-09-17-企業-AI-實務架構/09-實驗室-K8s：拆成一-pod-一容器並打通聊天.md` 取回。

`litellm`、`openwebui` 的 podman 路徑綁主機的所有介面，實驗室網段的其他主機連得到。網頁開 `http://<主機位址>:8080`，agent 連 `http://<主機位址>:4000`，實驗室主機是 172.20.1.192。連線是 HTTP 明文，主機沒有另設防火牆。OpenWebUI 開放註冊，新帳號要 admin 核准才能聊天。`vllm` 不開 hostPort，`litellm` 經 pod 網路用 `vllm-pod` 連進來。podman 的 kube 網路沒有隔離，同網路的 pod 與主機本身都連得到 `vllm`，主機對外位址連不到。

每個服務的工作負載正本在 `services/<svc>/base/`，兩條路徑目錄只放各自的差異（podman 的 hostPort、k8s 的 Service／Ingress／PVC）。`minio` 是唯一例外：`podman kube play` 不支援 `StatefulSet`，它的 podman 路徑保留自己的 `pod.yaml`，不引用 `base/`。

k8s 路徑的 Ingress 主機名一律 `<svc>.local`（`juicefs`、`iperf3`、`kadm`、`xadm-*` 不出 Ingress）。

> 🔴 **`kadm`／`xadm-lxqt`／`xadm-xfce` 是最高權服務**：三者皆為 `cluster-admin` ClusterRoleBinding ＋ `privileged` 容器 ＋ 映像內建帳密，且經 LoadBalancer 對外開 SSH／RDP。帳密不可輪換，外洩等於整座叢集淪陷——**只能綁在信任網段**。目前這三個服務沒有常駐實例（驗證完即拆）。

## 環境需求

| 工具 | 用途 | 必要性 |
|---|---|---|
| `task` | 唯一入口 | 必要 |
| `kubectl` | 渲染（內建 kustomize）與 k8s 部署 | 必要（podman 路徑也要，渲染用） |
| `podman` ＋ `sudo` | podman 路徑部署 | 走 podman 路徑時 |
| `python3` ＋ PyYAML | 單元測試、`manifest-diff` | 必要 |
| `skopeo` | `task image:check` 查映像是否仍在來源 registry | 用到時 |
| NVIDIA 驅動 ＋ container toolkit（CDI）＋ podman 5.7.0 | `vllm` 的 podman 路徑掛 GPU；`nvidia-ctk cdi list` 要看得到 `nvidia.com/gpu=all`。podman 5.7.0 實測可用，4.9.3 掛不到 GPU | 部署 `vllm` 時 |
| `jq`／`curl`／`nmap` | `task seed` 營運工具 | 用到時 |

k8s 路徑另有兩項叢集前置：PVC 與 Ingress **刻意不指定 class**（可攜設計），依賴叢集已標記**預設 StorageClass** 與**預設 IngressClass**。

## 用法

```bash
# 1. 有機密的服務先填真值（gitignored；gmm／grafana／juicefs／litellm／minio／openwebui／quay 七個服務具備）
cp services/<svc>/secret/secret.env.example services/<svc>/secret/secret.env
cp services/quay/secret/config.yaml.example services/quay/secret/config.yaml       # quay 另有檔案型機密

# 2. 部署
task apply SVC=<svc> TRACK=podman                    # podman 路徑
# vllm、litellm、openwebui 的 podman 路徑依主機分軌，實驗室用 TRACK=podman-4060ti
KUBECONFIG=<叢集設定檔> task apply SVC=<svc> TRACK=k8s   # k8s 路徑

# 3. 拆除（破壞性，見下方警告）
task delete SVC=<svc> TRACK=podman
KUBECONFIG=<叢集設定檔> task delete SVC=<svc> TRACK=k8s

# 其他
task image:check SVC=<svc>                           # 渲染產物的每個 image 都解析成完整參照，且仍存在於來源 registry
task --list-all                                      # 列出全部 task
```

> ⚠️ **步驟 1 不做也跑得動，但跑起來的是假的。** 機密真值檔缺席時 kustomize 直接失敗，所以 `bin/svc.sh` 會自 `.example` 複製一份佔位值頂上，並在 `apply` 時多印兩行 `Info:` 說明「用的不是真密碼」。**那兩行不是雜訊**——這樣部署出去的服務連不上相依元件，minio 更會拿假的 root 帳密把資料卷建起來，之後換真值得砍卷重來。

`litellm` 的模型清單是 `services/litellm/base/config.yaml`，整份入庫，不必複製。k8s 路徑改完這個檔要另跑 `kubectl rollout restart deployment/litellm`：ConfigMap 名稱不帶雜湊，`apply` 不會重啟 pod。雲端來源的金鑰填進 `services/litellm/secret/secret.env`，值不加引號：kustomize 會把引號當成值的一部分。DLP 用 LiteLLM 內建的 guardrail，寫在 `config.yaml` 檔尾的 `guardrails`。公司自訂關鍵字改 `tw-dlp` 程式裡的 `KEYWORDS`，改完要重新部署 `litellm` 才生效。直接指定雲端別名時，`tw-dlp` 阻擋含個資或圖片的請求。網頁聊天的 `chat-auto` 帶個資或圖片時，改由地端回答。

### 營運同步（`task seed`）

部署前後的銜接動作，有 `sync.sh` 的服務才具備，各自實作不同：

```bash
# gmm：佈建 guacamole 連線
cp services/gmm/sync.conf.example services/gmm/sync.conf                 # 連線目標與非機密參數
cp services/gmm/sync.secret.env.example services/gmm/sync.secret.env     # guacadmin 密碼
cp services/gmm/mapping.list.example services/gmm/mapping.list           # 連線清單（pipe 分隔，恰好 10 欄）
task seed SVC=gmm                                                        # 唯讀檢查用 task seed:check，只驗 token、不寫入

# openwebui：種下第一位 admin（安全時序：部署完成→立即 sync→才對外）。
# 同一支還會寫入每個模型的系統提示詞（system-prompt.txt）與常駐橫幅（banner.txt）。
# 語音開關照 sync.conf 的 VOICE_ENABLED 設。litellm 新增模型後要再跑一次
cp services/openwebui/sync.conf.example services/openwebui/sync.conf
cp services/openwebui/sync.secret.env.example services/openwebui/sync.secret.env
task seed SVC=openwebui                                                  # 唯讀檢查用 task seed:check，只驗 admin、系統提示詞、橫幅與語音開關，不寫入

# vllm：部署前先下載模型權重進具名卷（容器以離線模式啟動，卷裡沒有權重就起不來）。模型清單依軌而定，TRACK 必填
task seed SVC=vllm TRACK=podman-4060ti                                   # 唯讀檢查用 task seed:check，只查權重在不在、不下載
```

> ⚠️ **`task seed SVC=gmm` 具破壞性**：它會刪除 `mapping.list` 以外多餘的 user／group／connection（只保留 admin 帳號與 ROOT 群組）。

> ⚠️ **`task delete` 不可逆**：k8s 路徑刪掉 workload 產出的全部資源，**含 manifest 內宣告的 PVC**（叢集 StorageClass 的 `reclaimPolicy` 為 `Delete`，資料連帶消失）與帶外產生的 Secret；podman 路徑 `podman kube down --force`，pod 與具名卷一併移除。兩者執行前都會要求輸入服務名確認，**無 tty（cron／CI）時 `read` 失敗、腳本中止、什麼都不刪**。
>
> `StatefulSet` 的 `volumeClaimTemplates` PVC 由 k8s 動態建立、不在 manifest 內，**不隨 `delete` 移除**；腳本會列出這些 PVC 並印出手動清除的指令。

> **要暫停但保留資料**：本介面只有 `apply` 與 `delete` 兩個宣告式動詞，**不提供 `stop`**——命令式的暫停會讓叢集實況與 manifest 正本分岔，下一次 `apply` 會靜默把服務拉回。需要暫停時退回原生工具：k8s 路徑 `kubectl scale -n <ns> --replicas=0 deployment/<name>`（`DaemonSet` 如 `iperf3` 無 `replicas` 欄位，要停只能 `delete`）；podman 路徑 `sudo podman pod stop <svc>-pod`（`kube play` 吃 `Deployment`／`DaemonSet` 時 pod 名帶 `-pod` 後綴；`ncb` 與 `minio` 仍是 `<svc>`）。恢復一律用 `task apply`。

### coding agent 接 LiteLLM（發個人 key）

開發者的 coding agent 用個人 key 經 LiteLLM 連模型，請求紀錄會歸到那把 key。agent 自己選模型，不經自動分流。範例都在 `services/litellm/agents/`。範例裡的位址是主機本機的 `127.0.0.1`。agent 在實驗室網段的其他主機跑時，把範例裡的 `127.0.0.1` 換成主機位址。

管理員發 key 與停用 key：

```bash
# 發 key：key_alias 用 user-<帳號>，user_id 填 email。回應的 key 連同告知文字 key-notice.txt 一起交給使用者
curl -s http://127.0.0.1:4000/key/generate -H "Authorization: Bearer <LITELLM_MASTER_KEY>" -H 'Content-Type: application/json' \
  -d '{"key_alias":"user-<帳號>","user_id":"<email>"}' | jq -r .key

# 停用：先用 key_alias 查出雜湊值。停用後這把 key 的請求回 401。要恢復時，同樣的指令改打 /key/unblock
KEY_HASH="$(curl -s 'http://127.0.0.1:4000/key/list?key_alias=user-<帳號>&return_full_object=true' -H "Authorization: Bearer <LITELLM_MASTER_KEY>" | jq -r '.keys[0].token')"
curl -s -X POST http://127.0.0.1:4000/key/block -H "Authorization: Bearer <LITELLM_MASTER_KEY>" -H 'Content-Type: application/json' -d "{\"key\":\"${KEY_HASH}\"}"
```

使用者設定 agent。Claude Code 先看這則警語：

> ⚠️ **Claude Code 的網址與個人 key 要寫在同一個檔、同時設定。** 只設 `ANTHROPIC_BASE_URL` 時，Claude Code 會拿登入的個人訂閱憑證連 LiteLLM，憑證就經過了 LiteLLM。兩個值都寫進 `settings.local.json`。這個檔要列在 `.gitignore` 裡。網址不要單獨寫進團隊共用的 `.claude/settings.json`。開 Claude Code 後跑 `/status`。`Auth token` 那一行要寫 `ANTHROPIC_AUTH_TOKEN`。

| agent | 設定檔放哪裡 | 範例 |
|---|---|---|
| OpenCode | 專案根目錄的 `opencode.json`。個人 key 放環境變數 `LITELLM_API_KEY` | `opencode.json.example` |
| Claude Code | 專案的 `.claude/settings.local.json` | `claude-settings.local.json.example` |

OpenCode 範例只開 LiteLLM 一個來源，分享與自動更新都關閉。產生標題這類小任務固定用 `chat-local`。`limit.context` 要跟模型實際的長度一致：`chat-local` 對 vLLM 的 `--max-model-len`，`chat-cloud` 取自 Cloudflare 的模型資訊。

Claude Code 範例是給付費的雲端 Claude 用的。Claude 的別名要等正式環境改接 Google Cloud 才設，實驗室沒有。LiteLLM 目前不擋 Claude Code 選非 Claude 的別名，官方也不支援這種用法。

> OpenCode 會讀專案的 `.claude/skills/`，也會讀家目錄的 `~/.claude/skills/`。家目錄裡個人 skill 的名稱，會隨每次請求送給選用的模型，選雲端時也一樣。

## 驗證

```bash
task test        # 單元測試（離線，不碰真叢集）
```

實測輸出：每支測試檔各自逐項 `[OK]`，收尾一行 `ALL <n> PASS`。`python3 -m pytest tests/ -v` 跑的是同一批測試函式。

litellm 另有實機回歸，重新部署 litellm 後跑。它打主機本機的正式服務，不是離線測試：

```bash
task litellm:check   # DLP 18 則＋分流 8 則，每則印 [OK]／[FAIL]，全過時印 ALL 26 PASS
```

腳本是 `tests/live_gateway.py`，key 從 litellm 與 openwebui 的 `secret/secret.env` 讀，不印出。位址預設 `http://127.0.0.1:4000`，環境變數 `LITELLM_URL` 可以改。連不上時印 `[FAIL]`，退出碼 1。

離線是靠測試把 `KUBECONFIG` 指向不存在的路徑達成的——`kubectl kustomize` 是純本地操作不受影響，`kubectl apply`／`get`／`delete` 則會連線失敗，不會動到任何真實資源。

`kubectl kustomize` 過得了就代表 manifest 語法沒問題，所以單一服務的正本可以直接驗——這同時是**看渲染結果的唯一方式**，`build/` 中介產物已廢除（k8s 軌直接 `kubectl apply -k`，podman 軌把產物經 stdin 餵給 `podman kube play`）：

```bash
kubectl kustomize services/<svc>/k8s      # 不需要叢集
bash bin/assert-images.sh <svc>           # 兩軌的 image 都解析成完整參照，且仍在來源 registry
```

> ⚠️ **「純本地、不需任何前置」對七支有機密的服務（gmm／grafana／juicefs／litellm／minio／openwebui／quay）不成立。** 它們的 `secret/` 目錄以 `secretGenerator` 讀 gitignored 的真值檔，檔案缺席時 `kubectl kustomize` **直接 rc=1**（`evalsymlink failure`）。要嘛照上方用法段複製一份 `.example`，要嘛跑一次 `bash bin/svc.sh <svc> delete k8s`（在確認提示處直接 Enter 取消）讓腳本自 `.example` 補上佔位值。

**push 時會自動跑工作區共用的防護規則（guardrail）。** 護欄的行為、例外（行尾 `gitleaks:allow`）與接線，見根 repo 的 `AGENTS.md` 與 `.githooks/`。接線由根 repo `dot/.gitconfig` 的 `includeIf` 提供，不必在本 repo 設 `core.hooksPath`；新機器跑根 repo 的 `bin/bootstrap.sh`。`bin/assert-images.sh` **不在護欄裡**——共用護欄加不進本 repo 自有的檢查，要記得自己跑。

> ⚠️ **本 repo 無 CI**。所有驗證都在本機：`task test` ＋ pre-push ＋ 人工真環境煙測。宣稱「完成」前請確實跑過。

## 更多文件

| 文件 | 內容 |
|---|---|
| `AGENTS.md` | 給 AI agent 的專案脈絡、開發規範、地雷區 |
| `docs/ARCHITECTURE.md` | 架構、渲染管線、模組職責、設計決策 |
| `docs/PROGRESS.md` | 當前進度、待辦、技術債清冊、版本沿革 |
| `docs/superpowers/`、`docs/tickets/` | spec、plan、工單，只存本機、不進版控。舊版用 `git show def6982:docs/superpowers/` 與 `git show f37d0e6:docs/tickets/` 列出 |
