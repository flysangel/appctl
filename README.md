# app-services

容器服務的部署集。內容是各服務的 kustomize manifest 正本、一支部署控制腳本（`bin/svc.sh`），以及少量營運工具。本 repo 沒有應用程式碼。

每個服務都能走兩條部署軌（track，下文簡稱「軌」），由同一支控制腳本、同一組動詞驅動：

| 軌 | 用途 | 底層 | 拓樸 |
|---|---|---|---|
| `podman` | 單機、驗證 | `podman kube play`（需 `sudo`） | 與 k8s 軌同 kind（`Deployment`、`DaemonSet`；`ncb` 是 `Pod`，`minio` 不共用）加具名卷（named volume）與 hostPort |
| `k8s` | 正式、分散式 | `kubectl apply` | `Deployment`、`StatefulSet`、`DaemonSet` 加 PVC、Service、Ingress |

服務一覽（目錄清單用 `ls services/` 查）：

| 服務 | 用途 | podman | k8s | 服務埠 | 持久卷 |
|---|---|:-:|:-:|---|---|
| `filebrowser` | 網頁檔案總管 | ✓ | ✓ | 4000 | data |
| `gmm` | Guacamole 遠端桌面閘道（guacamole、guacd、mariadb） | ✓ | ✓ | 8080 | recordings、db |
| `grafana` | 儀表板（含 mariadb 後端） | ✓ | ✓ | 3000 | data、db |
| `iperf3` | 網路頻寬測試 server | ✓ | ✓ | 5201 | 無（無狀態） |
| `juicefs` | JuiceFS 分散式檔案系統（S3-gateway 形態） | ✓ | ✓ | 9005 | obj、meta |
| `kadm` | k8s 管理 SSH 堡壘（pod 內含 podman） | — | ✓ | 22100→22 | wk |
| `litellm` | LLM 統一出口（litellm proxy 加 postgresql） | ✓ | ✓（連不到地端模型，見 [docs/PROGRESS.md](docs/PROGRESS.md)） | 4000 | db |
| `minio` | S3 相容物件儲存 | ✓ | ✓ | 9000、9001 | podman：data；k8s：data0、data1 |
| `ncb` | nginx 加 certbot 的 TLS 反向代理，上游是主機上的 minio 9000 | ✓ | — | 80、443 | certbot（`ncb-conf` 是 ConfigMap，不是卷） |
| `openwebui` | Open WebUI 對話前端（webui 加 postgresql 含 pgvector，主資料庫兼向量庫） | ✓ | ✓ | 8080 | data、db |
| `quay` | Project Quay 容器映像倉庫（quay-app、postgresql、redis） | ✓ | ✓ | 8080 | pg-data、quay-data |
| `static-nginx` | 純靜態網頁 server | ✓ | ✓ | 80 | data |
| `vllm` | 地端推論（vLLM，掛 GPU；chat、embed、rerank 同卡共存） | ✓ | — | 8000、8001、8002（不開 hostPort） | models |
| `xadm-lxqt` | k8s 圖形管理工作站（LXQt via xrdp） | — | ✓ | 33890→3389、22100→22 | wk |
| `xadm-xfce` | k8s 圖形管理工作站（XFCE4 via xrdp） | — | ✓ | 33890→3389、22100→22 | wk |

- 埠是 podman 軌的 hostPort 與 k8s 的 Service 埠，卷的容量以 `services/<svc>/k8s/pvc.yaml` 為準（minio 在 `base/statefulset.yaml`）。同一台主機的 podman 軌，同埠的服務不能同時跑。
- `vllm`、`litellm`、`openwebui` 的 podman 軌只有依主機分軌的目錄，`TRACK` 要填 `podman-4060ti` 這類名稱；填 `podman` 會被守衛擋下。
- 服務只建一個軌的目錄，就代表另一軌不支援。`ncb` 在 k8s 沒有對應物：k8s 軌的 Ingress 都只有 HTTP，沒有 `tls:`，叢集也沒有 Issuer，TLS 要不要做歸 infra-k8s 決定；`kadm`、`xadm-*` 本來就是叢集內的管理工作站；`vllm` 的 k8s 軌還沒做。
- 每個服務的工作負載正本在 `services/<svc>/base/`，兩軌的目錄只放各自的差異（podman 的 hostPort、k8s 的 Service、Ingress、PVC）。`minio` 是例外，原因見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)〈已知的結構性限制〉。
- k8s 軌的 Ingress 主機名一律是 `<svc>.local`。有沒有 Ingress，看 `services/<svc>/k8s/ingress.yaml` 在不在。

**`kadm`、`xadm-lxqt`、`xadm-xfce` 是最高權服務。** 三者都綁 `cluster-admin` ClusterRoleBinding、跑 `privileged` 容器、用映像內建的帳密，而且經 LoadBalancer 對外開 SSH 或 RDP。帳密不能輪換。映像層裡還有 images 建置時留下的 bigred 無密碼私鑰，關掉密碼登入也擋不住，見 images 的 `docs/ARCHITECTURE.md` 裡講映像層私鑰那條。外洩等於整座叢集淪陷，所以只能放在信任網段。

## 1. 前置需求

| 工具 | 用途 | 必要性 |
|---|---|---|
| `task` | 唯一入口 | 必要 |
| `kubectl` | 渲染（內建 kustomize）與 k8s 部署 | 必要，podman 軌也要用它渲染 |
| `podman` 與 `sudo` | podman 軌部署。podman 軌是 rootful，查狀態與 log 也要 `sudo`；沒有 tty 時先 `sudo -v` | 走 podman 軌時 |
| `python3` 與 PyYAML | 單元測試、`manifest-diff` | 必要 |
| `skopeo` | `task image:check` 查映像是否還在來源 registry | 用到時 |
| NVIDIA 驅動、container toolkit（CDI）、podman 5.7.0 | `vllm` 的 podman 軌掛 GPU；`nvidia-ctk cdi list` 要看得到 `nvidia.com/gpu=all`。podman 4.9.3 掛不到 GPU | 部署 `vllm` 時 |
| `jq`、`curl`、`nmap` | `task seed` 營運工具 | 用到時 |

k8s 軌另有叢集前置：PVC 與 Ingress 刻意不指定 class，依賴叢集已經標好預設 StorageClass 與預設 IngressClass；`kadm`、`xadm-*` 的 `LoadBalancer` Service 要叢集有 LB 提供者，否則卡在 pending。

push 護欄是本 repo 自帶的 `.githooks/pre-push`，由帳號層派工 hook 呼叫。

## 2. 上手

```bash
# 1. 有機密的服務先填真值（gitignored）。哪些服務有機密：ls services/*/secret/secret.env.example
cp services/<svc>/secret/secret.env.example services/<svc>/secret/secret.env
cp services/quay/secret/config.yaml.example services/quay/secret/config.yaml       # quay 另有檔案型機密，裡面的 DB 與 redis 密碼要和 secret.env 一致

# 2. 部署
task apply SVC=<svc> TRACK=podman                    # podman 軌
# vllm、litellm、openwebui 的 podman 軌依主機分軌，實驗室用 TRACK=podman-4060ti
KUBECONFIG=<叢集設定檔> task apply SVC=<svc> TRACK=k8s   # k8s 軌

# 3. 拆除（不可逆，見〈拆除與暫停〉）
task delete SVC=<svc> TRACK=podman
KUBECONFIG=<叢集設定檔> task delete SVC=<svc> TRACK=k8s
```

**步驟 1 不做也跑得動，但跑起來的是假的。** 機密真值檔不在時，kustomize 直接失敗，所以 `bin/svc.sh` 會從 `.example` 複製一份佔位值頂上，`apply` 時多印兩行 `Info:` 說明「用的不是真密碼」。這樣部署出去的服務連不上相依元件。有資料卷的服務（minio、各家的資料庫）會用佔位密碼建卷，之後換真值得砍卷重來。

`litellm` 的模型清單是 `services/litellm/base/config.yaml`，整份進版控，不必複製。

- k8s 軌改完這個檔，要另跑 `kubectl rollout restart deployment/litellm`：ConfigMap 名稱不帶雜湊，`apply` 不會重啟 pod。
- 雲端來源的金鑰填進 `services/litellm/secret/secret.env`，值不加引號。kustomize 會把引號當成值的一部分。
- DLP 用 LiteLLM 內建的 guardrail，寫在 `config.yaml` 檔尾的 `guardrails`。公司自訂關鍵字改 `tw-dlp` 程式裡的 `KEYWORDS`，改完重新部署 `litellm` 才生效。
- 直接指定雲端別名時，`tw-dlp` 阻擋含個資或圖片的請求。網頁聊天的 `chat-auto` 帶個資或圖片時，改由地端回答。

`ncb` 的憑證要自己放進卷，順序見 [docs/PROGRESS.md](docs/PROGRESS.md) 的待驗條目。已知的事實：

- 憑證放在具名卷 `ncb-certbot` 的 `live/ncb.local/fullchain.pem` 與 `privkey.pem`。mountpoint 用 `sudo podman volume inspect ncb-certbot` 查。改 `server_name` 時，`live/<名稱>/` 的目錄名要一起改。
- nginx 的 443 區塊引用這兩個檔，檔不在時 nginx 起不來，80 埠也不聽。所以先放一組自簽憑證讓 nginx 起來。
- `ncb.local` 是 mDNS 保留域（RFC 6762），Let's Encrypt 不簽。要用 ACME 得先把 `server_name` 改成公網網域，再 `sudo podman exec ncb-certbot certbot certonly --webroot -w /etc/letsencrypt/www -d <網域> --email <信箱> --agree-tos`。第一次先加 `--staging` 試：Let's Encrypt 的 rate limits 文件寫同一組網域每 7 天只簽 5 張重複憑證。
- 放完或簽完要 `sudo podman exec ncb-nginx nginx -s reload`，或重跑 `apply`。nginx 自己的 reload 迴圈每 168 小時才跑一次。

`quay` 的存取政策寫在 `config.yaml`：只有 superuser 能建帳號、public repo 免登入可拉、登入用資料庫帳密。第一位 superuser 要自己建，另有兩件事要在第一次 `apply` 前知道：

- 等 `GET /health/instance` 回 200 後，`POST /api/v1/user/initialize`，JSON 帶 `username`（要等於 `config.yaml` 的 `SUPER_USERS`，範本是 `quay`）、`password`、`email`。Quay 文件寫這支 API 只在使用者表為空時開放。
- `SERVER_HOSTNAME` 是 `quay.local` 不帶埠，podman 軌的 hostPort 是 8080。用 `<主機>:8080` push 時，token 的 realm 會指向 `quay.local` 的 80 埠。
- 以 `.example` 補位上線時，`config.yaml` 的 `SECRET_KEY`、`DATABASE_SECRET_KEY` 是字面值 `${SECRET_KEY}`。建庫後再換會讓既有的 token 失效，所以要先填好。

`grafana` 沒設 `GF_SECURITY_ADMIN_*`，第一次登入用上游預設的 `admin`／`admin`。podman 軌一 `apply` 3000 埠就對網段開放，apply 完立刻登入改掉。

### 實驗室的連線方式

`litellm`、`openwebui` 的 podman 軌綁主機的所有介面，實驗室網段的其他主機都連得到。網頁開 `http://<主機位址>:8080`，agent 連 `http://<主機位址>:4000`，實驗室主機是 172.20.1.192。

- 連線是 HTTP 明文，主機沒有另設防火牆。其他有 hostPort 的服務同樣綁全部介面、同樣明文，埠見服務一覽表。
- OpenWebUI 開放註冊，新帳號要 admin 核准才能聊天。
- `vllm` 不開 hostPort，`litellm` 經 pod 網路用 `vllm-pod` 連進來。podman 的 kube 網路沒有隔離，同網路的 pod 與主機本身都連得到 `vllm`；從主機的對外位址連不到。

## 3. 常用操作

`task --list-all` 列出全部 task。改完要跑哪些檢查，見 [AGENTS.md](AGENTS.md)〈完成定義〉。

### 營運同步（`task seed`）

部署前後的銜接動作。只有帶 `sync.sh` 的服務才有，各自實作不同（哪些服務有：`ls services/*/sync.sh`）：

```bash
# gmm：佈建 guacamole 連線
cp services/gmm/sync.conf.example services/gmm/sync.conf                 # 連線目標與非機密參數
cp services/gmm/sync.secret.env.example services/gmm/sync.secret.env     # guacadmin 密碼
cp services/gmm/mapping.list.example services/gmm/mapping.list           # 連線清單（pipe 分隔，欄位照 mapping.list.example）
task seed SVC=gmm                                                        # 唯讀檢查用 task seed:check，只驗 token、不驗 mapping.list、不寫入

# openwebui：種下第一位 admin。podman 軌一 apply 就對網段開放，apply 完立刻 seed，縮短沒有 admin 的空窗。
# 同一支還會寫入每個模型的系統提示詞（system-prompt.txt）與常駐橫幅（banner.txt）。
# 語音開關照 sync.conf 的 VOICE_ENABLED 設。litellm 新增模型後要再跑一次
cp services/openwebui/sync.conf.example services/openwebui/sync.conf
cp services/openwebui/sync.secret.env.example services/openwebui/sync.secret.env
task seed SVC=openwebui                                                  # 唯讀檢查用 task seed:check，只驗 admin、系統提示詞、橫幅與語音開關，不寫入

# vllm：部署前先下載模型權重進具名卷（容器以離線模式啟動，卷裡沒有權重就起不來）。模型清單依軌而定，TRACK 必填
task seed SVC=vllm TRACK=podman-4060ti                                   # 唯讀檢查用 task seed:check，只查權重在不在、不下載
```

**`task seed SVC=gmm` 有破壞性**：它會刪除 `mapping.list` 以外多餘的 user、group、connection，只保留 admin 帳號與 ROOT 群組。它的行為：

- `GUACAMOLE_SERVER` 要含 webapp 路徑 `/guacamole`。k8s 軌先 `kubectl port-forward svc/gmm 8080`，再把 URL 填進 `sync.conf`。
- guacadmin 的初始密碼由 mariadb 映像內建的 schema 決定。第一次登入後在 UI 改掉，`sync.secret.env` 要跟著改，否則取 token 失敗。
- userpass 欄留空且帳號不存在時，產生 12 位密碼，只寫進 `services/gmm/random.csv`（0600），終端不印。這是唯一留存處。
- 連線名稱由 domain、acct、ip、alias 組成，不是欄位。改任一欄等於換名字：下次 seed 建新連線、刪舊連線。
- 每行先用 nmap 探目標埠，沒回 open 就在名稱加 `.X`。目標關機再開機，兩次 seed 會各刪建一次。
- 已存在的 user 不更新密碼，sharing profile 不更新，permission 只加不減；connection 已存在就整份 PUT。要移除資源只有一條路：刪 mapping 行再跑 seed。
- 每個 user 對 group 與 connection 只拿 READ；每條 connection 固定建 `ro`、`rw` 兩個 sharing profile。
- 腳本不看 API 回應，永遠以 0 結束。`task seed SVC=gmm` 回 0 不代表同步成功，看輸出裡有沒有「取 id 失敗，跳過」。改成檢查狀態碼列在 [docs/PROGRESS.md](docs/PROGRESS.md)。

### 查映像

```bash
task image:check SVC=<svc>   # 渲染產物的每個 image 都解析成完整參照，而且還在來源 registry
```

### 拆除與暫停

**`task delete` 不可逆。**

- k8s 軌刪掉 workload 產出的全部資源，包含 manifest 內宣告的 PVC 與機密。叢集 StorageClass 的 `reclaimPolicy` 是 `Delete`，資料連帶消失。
- podman 軌跑 `podman kube down --force`，pod 與具名卷一起移除。
- 兩者執行前都要求輸入服務名確認。沒有 tty（cron、CI）時 `read` 失敗，腳本中止，什麼都不刪。
- `StatefulSet` 的 `volumeClaimTemplates` PVC 由 k8s 動態建立，不在 manifest 內，所以不隨 `delete` 移除。腳本會列出這些 PVC，並印出手動清除的指令。
- `delete` 不動 `services/<svc>/secret/` 的真值檔與 sync 工具的本機檔，拆掉再 `apply` 用同一份。
- 要從 podman 具名卷取檔（例如 gmm 的錄影、ncb 的憑證），用 `sudo podman volume inspect <卷名>` 找 mountpoint，備份用 `sudo podman volume export <卷名>`。卷名就是 manifest 的 `claimName`。
- 要拆 pod 但留卷，`task` 沒有這個動詞。手動做法還沒驗過，列在 [docs/PROGRESS.md](docs/PROGRESS.md)。

要暫停但保留資料時，本介面不提供 `stop`，理由見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)〈設計取捨〉。改用原生工具：

- k8s 軌：`kubectl scale -n <ns> --replicas=0 deployment/<name>`；`minio` 是 `statefulset/minio`。`DaemonSet`（例如 `iperf3`）沒有 `replicas` 欄位，要停只能 `delete`。
- podman 軌：`sudo podman pod stop <svc>-pod`。`kube play` 吃 `Deployment`、`DaemonSet` 時 pod 名帶 `-pod` 後綴；`ncb` 與 `minio` 仍是 `<svc>`。
- 恢復一律用 `task apply`。

### coding agent 接 LiteLLM（發個人 key）

開發者的 coding agent 用個人 key 經 LiteLLM 連模型，請求紀錄會歸到那把 key。agent 自己選模型，不經自動分流。範例都在 `services/litellm/agents/`。範例裡的位址是主機本機的 `127.0.0.1`；agent 在實驗室網段的其他主機跑時，換成主機位址。

管理員發 key 與停用 key：

```bash
# master key 在 services/litellm/secret/secret.env，先載進環境變數，不要貼進命令列
set -a; . services/litellm/secret/secret.env; set +a

# 發 key：key_alias 用 user-<帳號>，user_id 填 email。回應的 key 連同告知文字 key-notice.txt 一起交給使用者
curl -s http://127.0.0.1:4000/key/generate -H "Authorization: Bearer ${LITELLM_MASTER_KEY}" -H 'Content-Type: application/json' \
  -d '{"key_alias":"user-<帳號>","user_id":"<email>"}' | jq -r .key

# 停用：先用 key_alias 查出雜湊值。停用後這把 key 的請求回 401。要恢復時，同樣的指令改打 /key/unblock
KEY_HASH="$(curl -s 'http://127.0.0.1:4000/key/list?key_alias=user-<帳號>&return_full_object=true' -H "Authorization: Bearer ${LITELLM_MASTER_KEY}" | jq -r '.keys[0].token')"
curl -s -X POST http://127.0.0.1:4000/key/block -H "Authorization: Bearer ${LITELLM_MASTER_KEY}" -H 'Content-Type: application/json' -d "{\"key\":\"${KEY_HASH}\"}"
```

使用者設定 agent。Claude Code 先看這則警語：

> **Claude Code 的網址與個人 key 要寫在同一個檔、同時設定。** 只設 `ANTHROPIC_BASE_URL` 時，Claude Code 會拿登入的個人訂閱憑證連 LiteLLM，憑證就經過了 LiteLLM。兩個值都寫進 `settings.local.json`，這個檔要列在 `.gitignore` 裡。網址不要單獨寫進團隊共用的 `.claude/settings.json`。開 Claude Code 後跑 `/status`，`Auth token` 那一行要寫 `ANTHROPIC_AUTH_TOKEN`。

| agent | 設定檔放哪裡 | 範例 |
|---|---|---|
| OpenCode | 專案根目錄的 `opencode.json`。個人 key 放環境變數 `LITELLM_API_KEY` | `opencode.json.example` |
| Claude Code | 專案的 `.claude/settings.local.json` | `claude-settings.local.json.example` |

- OpenCode 範例只開 LiteLLM 一個來源，分享與自動更新都關閉。產生標題這類小任務固定用 `chat-local`。`limit.context` 要跟模型實際的長度一致：`chat-local` 對 vLLM 的 `--max-model-len`，`chat-cloud` 取自 Cloudflare 的模型資訊。
- Claude Code 範例是給付費的雲端 Claude 用的。Claude 的別名要等正式環境改接 Google Cloud 才設，實驗室沒有。LiteLLM 目前不擋 Claude Code 選非 Claude 的別名，官方也不支援這種用法。
- OpenCode 會讀專案的 `.claude/skills/`，也會讀家目錄的 `~/.claude/skills/`。家目錄裡個人 skill 的名稱，會隨每次請求送給選用的模型，選雲端時也一樣。

## 4. 文件導覽

| 文件 | 內容 |
|---|---|
| [AGENTS.md](AGENTS.md) | 開工必讀：地雷、完成定義、不要做的事 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 邊界、目錄結構、部署管線、設計取捨 |
| [docs/PROGRESS.md](docs/PROGRESS.md) | 還沒做的待辦 |

spec、plan、工單放在 `docs/superpowers/` 與 `docs/tickets/`，只存本機，不進版控。

### 上下游

| 方向 | 對象 | 關係 |
|---|---|---|
| 上游 | images | 映像推上 registry 後，版號直接寫進各服務 `base/kustomization.yaml` 的 `images:` |
| 上游 | infra-k8s | 提供 k8s 軌部署用的叢集 |
| 護欄 | 帳號層派工 hook | 呼叫本 repo 自帶的 `.githooks/pre-push` |
