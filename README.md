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
| `minio` | S3 相容物件儲存 | ✓ | ✓ | 9000、9001 | podman：data；k8s：data0、data1 |
| `ncb` | nginx 加 certbot 的 TLS 反向代理，上游是主機上的 minio 9000 | ✓ | — | 80、443 | certbot（`ncb-conf` 是 ConfigMap，不是卷） |
| `quay` | Project Quay 容器映像倉庫（quay-app、postgresql、redis） | ✓ | ✓ | 8080 | pg-data、quay-data |
| `static-nginx` | 純靜態網頁 server | ✓ | ✓ | 80 | data |
| `xadm-lxqt` | k8s 圖形管理工作站（LXQt via xrdp） | — | ✓ | 33890→3389、22100→22 | wk |
| `xadm-xfce` | k8s 圖形管理工作站（XFCE4 via xrdp） | — | ✓ | 33890→3389、22100→22 | wk |

- 埠是 podman 軌的 hostPort 與 k8s 的 Service 埠，卷的容量以 `services/<svc>/k8s/pvc.yaml` 為準（minio 在 `base/statefulset.yaml`）。同一台主機的 podman 軌，同埠的服務不能同時跑。
- 服務只建一個軌的目錄，就代表另一軌不支援。`ncb` 在 k8s 沒有對應物：k8s 軌的 Ingress 都只有 HTTP，沒有 `tls:`，叢集也沒有 Issuer，TLS 要不要做歸 infra-k8s 決定；`kadm`、`xadm-*` 本來就是叢集內的管理工作站。
- 每個服務的工作負載正本在 `services/<svc>/base/`，兩軌的目錄只放各自的差異（podman 的 hostPort、k8s 的 Service、Ingress、PVC）。`minio` 是例外，原因見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)〈已知的結構性限制〉。
- k8s 軌的 Ingress 主機名一律是 `<svc>.local`。有沒有 Ingress，看 `services/<svc>/k8s/ingress.yaml` 在不在。

**`kadm`、`xadm-lxqt`、`xadm-xfce` 是最高權服務。** 三者都綁 `cluster-admin` ClusterRoleBinding、跑 `privileged` 容器、用映像內建的帳密，而且經 LoadBalancer 對外開 SSH 或 RDP。帳密不能輪換。映像層裡還有 images 建置時留下的 bigred 無密碼私鑰，關掉密碼登入也擋不住，見 images 的 `docs/ARCHITECTURE.md` 裡講映像層私鑰那條。外洩等於整座叢集淪陷，所以只能放在信任網段。

## 1. 安裝

需要下面的命令。裝好後跑 `task doctor`，確認必要命令都在、種類也對：

| 工具 | 用途 | 必要性 |
|---|---|---|
| `task` | 唯一入口 | 必要 |
| `kubectl` | 渲染（內建 kustomize）與 k8s 部署 | 必要，podman 軌也要用它渲染 |
| `podman` 與 `sudo` | podman 軌部署。podman 軌是 rootful，查狀態與 log 也要 `sudo`；沒有 tty 時先 `sudo -v` | 走 podman 軌時 |
| `python3` 與 PyYAML | 單元測試、`manifest-diff`、`task lint` 的四大文件與 Taskfile 檢查 | 必要 |
| `skopeo` | `task check:image` 查映像是否還在來源 registry | 用到時 |
| `jq`、`curl`、`nmap` | `task seed` 營運工具 | 用到時 |
| `shellcheck` | `task lint` 的靜態檢查 | 必要 |
| `gitleaks` | push 護欄的機密掃描 | 必要 |

k8s 軌另有叢集前置：PVC 與 Ingress 刻意不指定 class，依賴叢集已經標好預設 StorageClass 與預設 IngressClass；`kadm`、`xadm-*` 的 `LoadBalancer` Service 要叢集有 LB 提供者，否則卡在 pending。

push 護欄是本 repo 自帶的 `.githooks/pre-push`，由帳號層派工 hook 呼叫。沒有帳號層 hook 時，跑 `git config core.hooksPath .githooks`。

### 填機密

```bash
# 有機密的服務先填真值（gitignored）。哪些服務有機密：ls services/*/secret/secret.env.example
cp services/<svc>/secret/secret.env.example services/<svc>/secret/secret.env
cp services/quay/secret/config.yaml.example services/quay/secret/config.yaml       # quay 另有檔案型機密，裡面的 DB 與 redis 密碼要和 secret.env 一致
```

**不填也跑得動，但跑起來的是假的。** 機密真值檔不在時，kustomize 直接失敗，所以 `bin/svc.sh` 會從 `.example` 複製一份佔位值補位，`apply` 時多印兩行 `Info:` 說明「用的不是真密碼」。這樣部署出去的服務連不上相依元件。有資料卷的服務（minio、各家的資料庫）會用佔位密碼建卷，之後換成真值時，要刪掉卷再重建。

## 2. 用法

### 部署與拆除

```bash
# 部署
task apply SVC=<svc> TRACK=podman                    # podman 軌
KUBECONFIG=<叢集設定檔> task apply SVC=<svc> TRACK=k8s   # k8s 軌

# 拆除（不可逆，見〈拆除與暫停〉）
task delete SVC=<svc> TRACK=podman
KUBECONFIG=<叢集設定檔> task delete SVC=<svc> TRACK=k8s
```

### ncb 的憑證

`ncb` 的憑證要自己放進卷，順序見 [docs/PROGRESS.md](docs/PROGRESS.md) 的待驗條目。已知的事實：

- 憑證放在具名卷 `ncb-certbot` 的 `live/ncb.local/fullchain.pem` 與 `privkey.pem`。mountpoint 用 `sudo podman volume inspect ncb-certbot` 查。改 `server_name` 時，`live/<名稱>/` 的目錄名要一起改。
- nginx 的 443 區塊引用這兩個檔，檔不在時 nginx 起不來，80 埠也不聽。所以先放一組自簽憑證讓 nginx 起來。
- `ncb.local` 是 mDNS 保留域（RFC 6762），Let's Encrypt 不簽。要用 ACME 得先把 `server_name` 改成公網網域，再 `sudo podman exec ncb-certbot certbot certonly --webroot -w /etc/letsencrypt/www -d <網域> --email <信箱> --agree-tos`。第一次先加 `--staging` 試：Let's Encrypt 的 rate limits 文件寫同一組網域每 7 天只簽 5 張重複憑證。
- 放完或簽完要 `sudo podman exec ncb-nginx nginx -s reload`，或重跑 `apply`。nginx 自己的 reload 迴圈每 168 小時才跑一次。

### quay 的第一位 superuser

`quay` 的存取政策寫在 `config.yaml`：只有 superuser 能建帳號、public repo 免登入可拉、登入用資料庫帳密。第一位 superuser 要自己建，另有兩件事要在第一次 `apply` 前知道：

- 等 `GET /health/instance` 回 200 後，`POST /api/v1/user/initialize`，JSON 帶 `username`（要等於 `config.yaml` 的 `SUPER_USERS`，範本是 `quay`）、`password`、`email`。Quay 文件寫這支 API 只在使用者表為空時開放。
- `SERVER_HOSTNAME` 是 `quay.local` 不帶埠，podman 軌的 hostPort 是 8080。用 `<主機>:8080` push 時，token 的 realm 會指向 `quay.local` 的 80 埠。
- 以 `.example` 補位上線時，`config.yaml` 的 `SECRET_KEY`、`DATABASE_SECRET_KEY` 是字面值 `${SECRET_KEY}`。建庫後再換會讓既有的 token 失效，所以要先填好。

### grafana 的第一次登入

`grafana` 沒設 `GF_SECURITY_ADMIN_*`，第一次登入用上游預設的 `admin`／`admin`。podman 軌一 `apply` 3000 埠就對網段開放，apply 完立刻登入改掉。

### 網段開放

podman 軌的 hostPort 綁主機的所有介面，同網段的其他主機都連得到。連線是 HTTP 明文，主機沒有另設防火牆，埠見服務一覽表。

`task dev` 列出開發用的 task，`task ops` 列出維運用的 task，`task --list-all` 列出全部 task。改完要跑哪些檢查，見 [AGENTS.md](AGENTS.md)〈完成定義〉。

### 營運同步（`task seed`）

部署前後的銜接動作。只有帶 `sync.sh` 的服務才有，各自實作不同（哪些服務有：`ls services/*/sync.sh`）：

```bash
# gmm：佈建 guacamole 連線
# services/gmm/sync.conf 已進版控，放連線目標與非機密參數。跑 k8s 軌時，改它的 GUACAMOLE_SERVER
cp services/gmm/sync.secret.env.example services/gmm/sync.secret.env     # guacadmin 密碼
cp services/gmm/mapping.env.example services/gmm/mapping.env             # 連線清單（pipe 分隔，欄位照 mapping.env.example）
task seed SVC=gmm                                                        # 唯讀檢查用 task check:seed，只驗 token、不驗 mapping.env、不寫入
```

**`task seed SVC=gmm` 有破壞性**：它會刪除 `mapping.env` 以外多餘的 user、group、connection，只保留 admin 帳號與 ROOT 群組。動資料前，腳本要求輸入服務名 `gmm`。沒有 tty 時，腳本中止，什麼都不刪。它的行為：

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
task check:image SVC=<svc>   # 渲染產物的每個 image 都解析成完整參照，而且還在來源 registry
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

## 3. 文件導覽

| 文件 | 內容 |
|---|---|
| [AGENTS.md](AGENTS.md) | 開工必讀：地雷、完成定義、不要做的事 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 邊界、目錄結構、部署管線、設計取捨 |
| [docs/PROGRESS.md](docs/PROGRESS.md) | 還沒做的待辦 |

spec、plan、工單放在 `docs/superpowers/` 與 `docs/tickets/`，只存本機，不進版控。

### 上下游

| 方向 | 對象 | 關係 |
|---|---|---|
| 上游 | images | 映像推上 registry 後，版號直接寫進各服務 manifest 的 `image:` |
| 上游 | infra-k8s | 提供 k8s 軌部署用的叢集 |
| 護欄 | 帳號層派工 hook | 呼叫本 repo 自帶的 `.githooks/pre-push` |

## 4. 貢獻

改動在 worktree 的分支上做。merge 前，[AGENTS.md](AGENTS.md)〈完成定義〉列的驗證要全部通過。

## 5. 授權

Apache License 2.0，全文見 [LICENSE](LICENSE)。
