# app-services

容器服務的部署集，加上部署控制工具 `appctl`。內容是各服務的 kustomize manifest 正本、`appctl` 的 Go 原始碼與離線測試。本 repo 沒有服務本身的應用程式碼。

repo 在 GitHub：`https://github.com/flysangel/appctl`。舊的 GitLab 副本已封存，不再更新。

每個服務都能走兩條部署軌（track，下文簡稱「軌」），由同一個 `appctl`、同一組動詞驅動：

| 軌 | 用途 | 底層 | 拓樸 |
|---|---|---|---|
| `podman` | 單機、驗證 | `appctl` 把渲染產物翻成 Quadlet 單元，交給 systemd（要 `sudo appctl`） | 每個服務一個 pod，加具名卷（named volume）；主機埠寫在 `services/<svc>/podman/setting.conf` |
| `k8s` | 正式、分散式 | `appctl` 渲染後交給 `kubectl apply -f -` | `Deployment`、`StatefulSet`、`DaemonSet` 加 PVC、Service、Ingress |

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

- 埠是 podman 軌的主機埠與 k8s 的 Service 埠。podman 軌的主機埠以 `services/<svc>/podman/setting.conf` 為準，manifest 不寫 `hostPort`。卷的容量以 `services/<svc>/k8s/pvc.yaml` 為準（minio 在 `base/statefulset.yaml`）。
- 同一台主機的 podman 軌，同埠的服務不能同時跑。哪些服務刻意共用同一埠、只能擇一部署，見 `config/port-groups.conf`。
- 服務只建一個軌的目錄，就代表另一軌不支援。`ncb` 在 k8s 沒有對應物：k8s 軌的 Ingress 都只有 HTTP，沒有 `tls:`，叢集也沒有 Issuer，TLS 要不要做歸 infra-k8s 決定。`kadm`、`xadm-*` 本來就是叢集內的管理工作站。
- 每個服務的工作負載正本在 `services/<svc>/base/`，兩軌的目錄只放各自的差異（podman 的 patch 與 `setting.conf`；k8s 的 Service、Ingress、PVC）。
- k8s 軌的 Ingress 主機名一律是 `<svc>.local`。有沒有 Ingress，看 `services/<svc>/k8s/ingress.yaml` 在不在。

**`kadm`、`xadm-lxqt`、`xadm-xfce` 是最高權服務。** 三者都綁 `cluster-admin` ClusterRoleBinding、跑 `privileged` 容器、用映像內建的帳密，而且經 LoadBalancer 對外開 SSH 或 RDP。帳密不能輪換。映像層裡還有 images 建置時留下的 bigred 無密碼私鑰，關掉密碼登入也擋不住，見 images 的 `docs/ARCHITECTURE.md` 裡講映像層私鑰那條。外洩等於整座叢集淪陷，所以只能放在信任網段。

## 1. 安裝

### 部署主機

```bash
# 裝最新的正式版到 /usr/local/bin/appctl（只有 linux amd64），下載後驗 sha256
curl -sSLf https://raw.githubusercontent.com/flysangel/appctl/main/install.sh | sudo bash

# 裝指定版本；預發布版（例如 v0.1.0-rc.1）一定要帶版本，最新版不含預發布版
curl -sSLf https://raw.githubusercontent.com/flysangel/appctl/main/install.sh | sudo bash -s -- <版本>

# appctl 從目前目錄往上找含 services/ 與 Taskfile.yaml 的目錄，所以要在 clone 裡跑
git clone https://github.com/flysangel/appctl.git
```

還沒有 Release 時，照〈從原始碼編譯〉自己編。

部署主機依用到的功能另外要裝：

| 工具 | 用途 |
|---|---|
| `podman` 與 systemd | podman 軌。主機是 Ubuntu 24.04 的官方 podman 4.9.3，設計取捨照這個版本定 |
| `kubectl` 與自己的 kubeconfig | k8s 軌，以及 `gmm`、`quay` 子命令在 k8s 軌的 port-forward |
| `skopeo` | `appctl check-images` 查映像是否還在來源 registry |
| `git` | podman 軌 `apply` 比對機密檔是不是還是 demo 值 |

k8s 軌另有叢集前置：PVC 與 Ingress 刻意不指定 class，依賴叢集已經標好預設 StorageClass 與預設 IngressClass。`kadm`、`xadm-*` 的 `LoadBalancer` Service 要叢集有 LB 提供者，否則卡在 pending。

### 從原始碼編譯

開發機要下面的命令。裝好後跑 `task doctor`，確認必要命令都在、種類也對：

| 工具 | 用途 | 必要性 |
|---|---|---|
| `task` | 開發入口：`doctor`、`lint`、`test`、`bootstrap` | 必要 |
| `go` | 編譯與測試 `appctl`，版本照 `go.mod` | 必要 |
| `kubectl` | k8s 軌與 port-forward | 必要 |
| `python3` 與 PyYAML | F9 維護者工具（四大文件、Taskfile 檢查、`manifest-diff`）與它們的測試 | 必要 |
| `shellcheck` | `task lint` 的靜態檢查 | 必要 |
| `gitleaks` | push 護欄的機密掃描 | 必要 |

```bash
task bootstrap   # 編譯成 bin/appctl（gitignored）；podman 軌用 sudo bin/appctl 跑
```

push 護欄是本 repo 自帶的 `.githooks/pre-push`，由帳號層派工 hook 呼叫。沒有帳號層 hook 時，跑 `git config core.hooksPath .githooks`。

### 機密：demo 值進版控

repo 是公開的。下面這些檔進版控，內容是 demo 值，任何人都看得到：

- `services/*/secret/secret.env`
- `services/quay/secret/config.yaml`
- `services/gmm/mapping.env`、`services/gmm/sync.secret.env`

實際環境由操作者直接改部署主機 clone 裡的這些檔。有兩個已知代價：

- 上游改了同一個檔時，`git pull` 會衝突，要手動合併。
- 在部署主機上 `git commit -a` 會把真密碼帶進 commit。push 護欄的 gitleaks 不保證擋得住，所以部署主機上不要 commit。

另外要知道：

- podman 軌 `apply` 時，機密檔內容跟 git HEAD 相同，就印 `Info: <檔> 還是版控裡的 demo 值`。這行表示部署出去的是公開的密碼。
- 有資料卷的服務（各家的資料庫、minio）在第一次 `apply` 時用當下的密碼建卷。之後只改檔，不會改卷裡的密碼。
- quay 的 `DB_PASSWORD`、`REDIS_PASSWORD` 同時寫在 `secret.env` 與 `config.yaml`。改完跑 `appctl check-secret quay`，它只印鍵名，不印值。
- `services/gmm/random.csv` 是 `appctl gmm seed` 的輸出，不進版控。

## 2. 用法

`appctl` 不帶參數時印用法。各子命令要用什麼身分跑：

| 子命令 | 身分 |
|---|---|
| `apply`、`stop`、`delete` 走 podman 軌 | `sudo appctl`。不是 root 就拒跑 |
| `smoke`、`ncb cert` | `sudo appctl` |
| `apply`、`delete` 走 k8s 軌 | 一般使用者，讀自己的 kubeconfig |
| `render`、`check`、`check-images`、`check-secret`、`gmm`、`quay`、`version` | 一般使用者 |

`appctl` 內部不自己加 `sudo`。podman 軌是 rootful：查狀態也要 `sudo`，例如 `sudo podman ps --pod`、`sudo systemctl status <svc>-<容器名>.service`。不加 `sudo` 的 `podman` 看的是你自己的 rootless 實例，什麼都查不到。

### 部署、暫停與拆除

```bash
# podman 軌
sudo appctl apply <svc> --track podman
sudo appctl stop <svc> --track podman
sudo appctl delete <svc> --track podman

# k8s 軌（沒有 stop）
KUBECONFIG=<叢集設定檔> appctl apply <svc> --track k8s
KUBECONFIG=<叢集設定檔> appctl delete <svc> --track k8s

# 只印渲染產物，不部署
appctl render <svc> --track <軌>
```

podman 軌的命名：pod 叫 `<svc>`，容器叫 `<svc>-<容器名>`，systemd 單元是 `<svc>-pod.service` 與 `<svc>-<容器名>.service`，卷名是 manifest 的 `claimName`（StatefulSet 是 template 名）。

主機上還有舊版 `podman kube play` 起的 pod 時，`apply` 拒絕部署，並印出 `sudo podman pod rm -f <pod>`。這個指令只拆 pod，留下卷。之後 `apply`，Quadlet 用同名的卷接回舊資料。

`stop` 只有 podman 軌有。它停掉該服務的全部單元，留下卷。效力到主機重開機或下次 `apply` 為止：容器單元開機會自動啟動。k8s 軌要暫停，改用 `kubectl scale -n default --replicas=0 deployment/<name>`；`minio` 是 `statefulset/minio`；`DaemonSet`（例如 `iperf3`）沒有 `replicas`，要停只能 `delete`。

**`delete` 不可逆。**

- 兩軌都要求輸入服務名確認。沒有 tty（cron、CI）時中止，什麼都不刪。
- podman 軌：停單元，刪單元檔、ConfigMap 檔、podman secret 與卷。刪完逐一確認 secret 與卷都不在，還有東西在就以 1 結束，並印出手動清除的指令。
- k8s 軌：`kubectl delete -f -` 刪掉渲染產物的全部資源，包含 manifest 內宣告的 PVC 與 Secret。叢集 StorageClass 的 `reclaimPolicy` 是 `Delete`，資料連帶消失。
- `StatefulSet` 的 `volumeClaimTemplates` PVC 由 k8s 動態建立，不在 manifest 內，不隨 `delete` 移除。`appctl` 列出這些 PVC，並印出手動清除的指令。
- `delete` 不動 repo 裡的機密檔，拆掉再 `apply` 用同一份。
- 要從 podman 具名卷取檔（例如 gmm 的錄影、ncb 的憑證），用 `sudo podman volume inspect <卷名>` 找 mountpoint，備份用 `sudo podman volume export <卷名>`。

### 檢查

```bash
appctl check                # 離線：image 完整參照、overlay 的 namespace、同主機撞埠、機密一致性
appctl check-images <svc>   # 連 registry：渲染產物的每個 image 都還在來源 registry（要 skopeo 與網路）
appctl check-secret <svc>   # 同一個機密值在各檔之間一致，只印鍵名；有哪些服務有規則見 internal/check/secret.go
```

### 煙測

```bash
sudo appctl smoke <svc>...                   # 每個服務：apply、等單元起來、HTTP 探測、再 apply、再探測、delete
sudo appctl smoke --allow-guarded ncb        # ncb、kadm、xadm-* 預設拒跑，要加這個旗標
```

最後一步是 `delete`，會刪掉該服務的卷，正式資料一起消失。每個服務的 `delete` 都要輸入服務名，沒有 tty 時那個服務記為失敗。各服務的探測寫在 `internal/smoke/smoke.go`。

### gmm 的營運同步

```bash
appctl gmm check --track podman   # 唯讀：驗 token 與 mapping.env 的欄數，不寫入
appctl gmm seed --track podman    # 照 mapping.env 佈建 guacamole 的 user、group、connection
```

設定檔：`services/gmm/sync.conf` 放連線目標與非機密參數，`sync.secret.env` 放 guacadmin 密碼，`mapping.env` 是連線清單（pipe 分隔，欄位寫在檔頭）。

**`appctl gmm seed` 有破壞性**：它刪除 `mapping.env` 以外多餘的 user、group、connection，只保留 admin 帳號與 ROOT 群組。動資料前，它先印出同步計畫（要建、要覆寫、要刪的名稱），再要求輸入服務名 `gmm`。沒有 tty 時中止，什麼都不刪。它的行為：

- podman 軌連 `sync.conf` 的 `GUACAMOLE_SERVER`，值要含 webapp 路徑 `/guacamole`。`--track k8s` 時，`appctl` 自己跑 `kubectl port-forward svc/gmm`，用完就停，不讀 `GUACAMOLE_SERVER`。
- guacadmin 的初始密碼由 mariadb 映像內建的 schema 決定。第一次登入後在 UI 改掉，`sync.secret.env` 要跟著改，否則取 token 失敗。
- userpass 欄留空且帳號不存在時，產生 `PASS_LENGTH` 位密碼，只寫進 `services/gmm/random.csv`（0600），終端不印。這是唯一留存處。
- 連線名稱由 domain、acct、ip、alias 組成，不是欄位。改任一欄等於換名字：下次 seed 建新連線、刪舊連線。
- 每行先對目標埠開 TCP 連線（3 秒逾時），連不上就在名稱加 `.X`。目標關機再開機，兩次 seed 會各刪建一次。
- 已存在的 user 不更新密碼，sharing profile 不更新，permission 只加不減；connection 已存在就整份 PUT。要移除資源只有一條路：刪 mapping 行再跑 seed。
- 每個 user 對 group 與 connection 只拿 READ；每條 connection 固定建 `ro`、`rw` 兩個 sharing profile。
- `mapping.env` 有任一行欄數不對，或一筆連線都沒有，就整批中止，什麼都不改。
- 每次 API 呼叫都檢查狀態碼。有失敗時印 `Error:`，其餘行照樣同步，最後以 1 結束。

### quay 的第一位 superuser

```bash
appctl quay init --track podman   # k8s 軌改帶 --track k8s，appctl 自己 port-forward svc/quay
```

- 帳號是 `config.yaml` 的 `SUPER_USERS` 第一個名字。密碼與信箱讀 `secret.env` 的 `QUAY_SUPERUSER_PASSWORD`、`QUAY_SUPERUSER_EMAIL`，這兩個鍵不進容器。
- `appctl` 等 `GET /health/instance` 回 200，再 `POST /api/v1/user/initialize`。Quay 文件寫這支 API 只在使用者表為空時開放。已經初始化時，`appctl` 印 `Info:` 並以 0 結束，可以重複跑。
- podman 軌連 `127.0.0.1:8080`，所以要在部署主機上跑。
- quay 的存取政策寫在 `config.yaml`：只有 superuser 能建帳號、public repo 免登入可拉、登入用資料庫帳密。
- `SERVER_HOSTNAME` 是 `quay.local` 不帶埠，podman 軌的主機埠是 8080。用 `<主機>:8080` push 時，token 的 realm 會指向 `quay.local` 的 80 埠。
- demo 的 `config.yaml` 裡，`SECRET_KEY`、`DATABASE_SECRET_KEY` 是字面值 `${SECRET_KEY}` 這類字串。建庫後再換會讓既有的 token 失效，所以第一次 `apply` 前先填好。

### ncb 的憑證

```bash
sudo appctl apply ncb --track podman                # nginx 沒有憑證起不來，會一直重啟
sudo appctl ncb cert                                # 放自簽憑證進 ncb-certbot 卷，再 reload nginx
sudo appctl ncb cert --acme --email <信箱>           # 公網網域才能用；預設走 Let's Encrypt staging
sudo appctl ncb cert --acme --email <信箱> --no-staging
```

- 網域讀 `services/ncb/podman/default.conf` 的第一個 `server_name`。憑證放在卷 `ncb-certbot` 的 `live/<網域>/fullchain.pem` 與 `privkey.pem`。
- 自簽不覆蓋卷裡已有的憑證檔，要換掉時加 `--force`。
- `ncb.local` 是 mDNS 保留域（RFC 6762），Let's Encrypt 不簽，`--acme` 會直接拒絕。要用 ACME 得先把 `server_name` 改成公網網域。
- ACME 走 webroot，nginx 要先在跑，所以第一次要先放自簽憑證。Let's Encrypt 的 rate limits 文件寫同一組網域每 7 天只簽 5 張重複憑證，所以預設走 staging，確認流程可行再加 `--no-staging`。
- certbot 遇到已存在的 `live/<網域>/` 會拒絕建憑證。所以 `--acme` 在 `renewal/<網域>.conf` 不存在時（目錄是自簽放的），先把 `live/<網域>/` 移到 `live/<網域>.selfsigned/`（取代舊的），再用 `--cert-name <網域>` 跑 certbot。certbot 失敗時，自簽憑證搬回原處，nginx 照常能用。已經是 certbot 簽的憑證不移動。
- nginx 沒在跑時，`ncb cert` 放好憑證但不 reload，改印出啟動指令：已部署過就 `sudo systemctl restart ncb-nginx.service`。

### grafana 的第一次登入

`grafana` 沒設 `GF_SECURITY_ADMIN_*`，第一次登入用上游預設的 `admin`／`admin`。podman 軌一 `apply` 3000 埠就對網段開放，apply 完立刻登入改掉。

### 網段開放

podman 軌的主機埠綁主機的所有介面，同網段的其他主機都連得到。連線是 HTTP 明文，主機沒有另設防火牆，埠見服務一覽表。

## 3. 文件導覽

| 文件 | 內容 |
|---|---|
| [AGENTS.md](AGENTS.md) | 開工必讀：地雷、完成定義、不要做的事 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 邊界、目錄結構、部署管線、設計取捨 |
| [docs/PROGRESS.md](docs/PROGRESS.md) | 目前狀態與還沒做的待辦 |

`task dev` 列出開發用的 task。改完要跑哪些檢查，見 [AGENTS.md](AGENTS.md)〈完成定義〉。spec、plan、工單放在 `docs/superpowers/` 與 `docs/tickets/`，只存本機，不進版控。

### 上下游

| 方向 | 對象 | 關係 |
|---|---|---|
| 上游 | images | 映像推上 registry 後，版號直接寫進各服務 manifest 的 `image:` |
| 上游 | infra-k8s | 提供 k8s 軌部署用的叢集 |
| 護欄 | 帳號層派工 hook | 呼叫本 repo 自帶的 `.githooks/pre-push` |
| 驗證與發布 | GitHub Actions | 每次 push 跑 `.github/workflows/ci.yml`；push 版本 tag（例如 `v0.1.0`、`v0.1.0-rc.1`，樣式見 `.github/workflows/release.yml` 的 `tags`）時，`release.yml` 驗證後發布 Release |

## 4. 貢獻

改動在 worktree 的分支上做。merge 前，[AGENTS.md](AGENTS.md)〈完成定義〉列的驗證要全部通過。

## 5. 授權

Apache License 2.0，全文見 [LICENSE](LICENSE)。
