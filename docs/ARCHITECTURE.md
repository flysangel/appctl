# app-services 架構

本檔講 repo 怎麼組織、部署怎麼跑、機密怎麼進容器，以及為什麼這樣設計。用法見 [README.md](../README.md)。

## 1. 定位與邊界

每個服務一個目錄，目錄裡是 kustomize 正本：`base/` 兩軌共用，`podman/`、`k8s/` 是各軌的 overlay。k8s YAML 是唯一正本。部署控制工具 `appctl` 是一個 Go binary，內嵌 kustomize 渲染：

- k8s 軌：渲染產物經 stdin 交給 `kubectl`。
- podman 軌：翻譯器把渲染產物翻成 Quadlet 單元，交給 systemd。

沒有自訂 schema、沒有模板語言，也沒有字串替換。podman 軌唯一不在 manifest 裡的事實是主機埠，寫在 `setting.conf`。

- 上游：images repo 建好的映像推上 registry 後，版號直接寫進各服務 manifest 的 `image:`。本 repo 不讀 images 的版本清單。
- 上游：k8s 軌部署到 infra-k8s 建好的叢集。
- 叢集級 addon（LB、Ingress、cert-manager 等）在 infra-k8s，不在本 repo。本 repo 只放使用者面的服務。
- push 護欄不算上游依賴。它是本 repo 自帶的 `.githooks/` 副本，由帳號層派工 hook 呼叫，不釘版。
- 驗證與發布在 GitHub Actions：`ci` 每次 push 跑，`release` 在 push 版本 tag（例如 `v0.1.0`、`v0.1.0-rc.1`，樣式見 `.github/workflows/release.yml` 的 `tags`）時驗證後發布 Release。部署主機用 `install.sh` 從 Release 裝 `appctl`。
- 不管公網 DNS 與 DDNS。服務只給信任網段用，Ingress 主機名一律 `<svc>.local`，工作區沒有 repo 維護公網名稱。

## 2. 目錄結構

```
Taskfile.yaml              開發入口：doctor、lint、test、bootstrap
LICENSE                    Apache License 2.0 全文
install.sh                 從 GitHub Release 下載 appctl，驗 sha256，裝到 /usr/local/bin
go.mod、go.sum             Go module github.com/flysangel/appctl

cmd/appctl/                CLI 入口：子命令分派與旗標解析
internal/
  repo/                    找 repo 根、列服務與軌
  render/                  內嵌 kustomize 渲染、切多文件 YAML
  setting/                 解析與檢查 setting.conf
  quadlet/                 翻譯器：渲染產物 → Quadlet 單元、ConfigMap 檔、podman secret；欄位白名單
  bundle/                  render + setting + quadlet 串成一個服務的翻譯產物
  podman/                  podman 軌的 apply、stop、delete 與部署紀錄
  k8s/                     k8s 軌的 apply、delete 與 port-forward
  check/                   appctl check、check-images、check-secret
  smoke/                   podman 軌煙測
  gmm/、quay/、ncb/        服務子命令
  confirm/                 刪資料前輸入服務名確認，沒有 tty 就中止
  run/                     外部指令的介面；測試換成 run.Fake
  installtest/             install.sh 的測試

bin/
  doctor.sh                task doctor：命令存在與種類、core.hooksPath 來源
  check-docs.py            F9：四大文件的必要節與引用
  check-taskfile.py        F9：Taskfile 的 task 名與分類
  run-tests.py             F9：收集 tests/test_*.py，每支都要印 ALL <n> PASS
  manifest-diff.py         F9：語意零漂移比對器（大改時用）
  render-diff.sh           F9：比對某個 revision 與工作樹的渲染產物
  appctl                   task bootstrap 的產物（gitignored）

config/
  port-groups.conf         同一台主機上刻意共用主機埠、只能擇一部署的服務組

.github/workflows/         ci.yml、release.yml
.gitattributes             腳本強制 LF
tests/                     F9 工具的 Python 測試

services/<svc>/
  base/                    工作負載正本，兩軌共用；檔名是 kind 小寫
    kustomization.yaml     resources: 工作負載（映像完整參照直接寫在 manifest）
  {podman,k8s}/            overlay
    kustomization.yaml     namespace:、resources: [../base]；有機密者另加 ../secret
  podman/setting.conf      podman 軌的主機埠（INI，文法在檔頭）
  secret/                  （有機密的服務才有）機密宣告，兩軌共用
    kustomization.yaml     secretGenerator 加 disableNameSuffixHash: true
    secret.env             機密 demo 值（進版控）
    config.yaml            （僅 quay）檔案型機密的 demo 值（進版控）
  sync.conf                （僅 gmm）連線目標與非機密參數
  sync.secret.env          （僅 gmm）guacadmin 密碼的 demo 值（進版控）
  mapping.env              （僅 gmm）連線清單的 demo 值（進版控）；seed 產出的 random.csv gitignored
```

目錄與副檔名照 `~/.claude/rules/directories.md`。只給一個服務用的設定留在 `services/<svc>/`；跨服務的設定放 `config/`，清單用 `ls config/` 查。

下面這些檔不照 `~/.claude/rules/directories.md`〈設定檔〉的副檔名規則：

| 檔 | 理由 |
|---|---|
| `.githooks/forbidden-patterns.txt`、`.githooks/gitleaks.toml` | harness 範本的副本，改名要從範本一起改；gitleaks 只讀 TOML |
| `services/quay/secret/config.yaml` | quay 只讀 YAML |
| `services/ncb/podman/default.conf` | 由 `configMapGenerator` 掛進容器，文法由 nginx 定 |
| `services/*/secret/secret.env` 等 demo 機密檔 | `.env` 照規則不進版控，但這些是公開的 demo 值，刻意進版控，理由見〈設計取捨〉 |

根層除了 `~/.claude/rules/directories.md`〈根層〉列的固定檔，另有三個檔：`go.mod`、`go.sum` 要放在 Go module 的根；`install.sh` 的下載網址寫在 README 與檔頭，搬家會讓舊網址失效。

要看渲染結果，現跑 `appctl render <svc> --track <軌>`。單一服務的完整工作負載要從 `base/` 讀起，overlay 只帶該軌的差異。

名字以 `podman` 開頭的目錄（例如 `podman-<主機>`）也算 podman 軌：`repo.Tracks`、部署動詞、`render`、`check`（依主機分組算撞埠）、gmm 與 quay 子命令都認得。`ncb cert` 與 `TestTranslateAllServices` 只看 `podman`，`smoke` 優先用 `podman`。目前沒有這種目錄。

`services/finetune/` 與對應的測試刻意不進版控，只存本機。

## 3. 依賴與資料流

### 部署管線

```
appctl apply <svc> --track <軌>
      ↓
① 守衛       services/<svc>/<軌>/ 目錄存在？一道檢查同時涵蓋服務名打錯與該軌不支援
      ↓
② 渲染       內嵌 kustomize 渲染 overlay（不呼叫外部 kubectl）
      ↓
③ k8s 軌     kubectl apply -f -（delete 是 kubectl delete -f - --ignore-not-found）
   podman 軌 翻譯器 → 寫單元、ConfigMap 檔、podman secret → daemon-reload → start 或 restart
```

podman 軌的 `apply` 依序做：

1. 不是 root 就拒跑。
2. 主機上有舊版 `podman kube play` 留下的 pod（`<svc>-pod`，或沒有 `<svc>-pod.service` 在跑的 `<svc>`）時拒跑，印出 `sudo podman pod rm -f <pod>`。
3. 機密檔內容跟 git HEAD 相同時，印 `Info:` 提醒還是 demo 值。
4. 寫單元與 ConfigMap 檔，更新 podman secret。上次部署有、這次沒有的單元，先停掉再刪檔。
5. `systemctl daemon-reload`，再啟動。自己的檔或它用到的卷、ConfigMap、secret 有變的單元 restart，其餘 start。

### 翻譯器的產物

每個服務一個 pod。podman 4.9.3 的 Quadlet 沒有 `.pod` 單元，所以 pod 由一般 systemd 單元建：

| 產物 | 位置 | 內容 |
|---|---|---|
| `<svc>-pod.service` | `/etc/systemd/system/` | oneshot：`podman pod create --replace --name <svc> -p <setting.conf 的埠>`，`ExecStop` 刪 pod |
| `<svc>-<容器名>.container` | `/etc/containers/systemd/` | `PodmanArgs=--pod=<svc>`，`Requires=`、`After=` 指向 pod 服務，`WantedBy=multi-user.target` |
| `<卷名>.volume` | `/etc/containers/systemd/` | `VolumeName=` 取 `claimName`；StatefulSet 取 template 名 |
| ConfigMap 檔 | `/var/lib/appctl/<svc>/configmap/<ConfigMap>/<鍵>` | 用唯讀 bind mount 掛進容器 |
| podman secret | podman 的 secret store | 名字是 `<svc>_<Secret 名>_<鍵>`，先刪再建；單元用 `Secret=` 掛成 env 或檔案 |
| 部署紀錄 | `/var/lib/appctl/<svc>/deployed.json`（0600） | 寫過的單元、ConfigMap、secret 與已生效內容的 sha256，以及卷名清單 |

單容器服務也走同一個形狀，翻譯器只有一條路徑。

- 欄位白名單在 `internal/quadlet/whitelist.go`。不在白名單的欄位就報錯，不產生單元。`serviceAccountName` 在 podman 端沒有意義，翻譯器略過，理由寫在白名單的註解。
- `command` 翻成 `PodmanArgs=--entrypoint=<JSON 陣列>`，`args` 翻成 `Exec=`。Quadlet 4.9.3 沒有 `Entrypoint=` 鍵。
- 單元裡的 `%` 寫成 `%%`、`$` 寫成 `$$`。k8s 的 `$(VAR)` 展開語法 podman 不支援，出現在 `command`、`args` 時翻譯器報錯。
- `emptyDir` 加 `medium: Memory` 翻成 `Tmpfs=`；沒有 medium 的 `emptyDir` 翻成匿名卷，容器刪除時一起刪。
- `restartPolicy` 的 Always 或省略翻成 `Restart=always`，OnFailure 翻成 `on-failure`，Never 翻成 `no`。

`deployed.json` 讓 `delete` 拿得到上次 `apply` 寫出的全部東西：manifest 改過或 `apply` 中途失敗時，只看這次的翻譯產物會漏刪。`apply` 寫檔前先存「舊紀錄加這次產物」的聯集，全部單元啟動成功後才把 hash 換成這次的值，所以中途失敗後再 `apply` 仍看得到「有變」而 restart。

`stop` 停掉紀錄裡的全部單元，容器在前、pod 服務最後，不刪卷。`delete` 先要求輸入服務名，再停單元、刪單元檔與 secret、`daemon-reload`、刪卷，並確認 secret 與卷都已不在。全部乾淨後才刪 `/var/lib/appctl/<svc>/`；中途失敗時紀錄留著，再跑一次 `delete` 就能接著清。

k8s 軌的 `delete` 多一步：`StatefulSet` 的 `volumeClaimTemplates` PVC 由叢集動態建立，不在 manifest 內，不隨 `delete` 移除。`appctl` 刪完列出這些 PVC，並印出手動清除的指令。

### `setting.conf`

`services/<svc>/podman/setting.conf` 放 k8s YAML 表達不了的 podman 專屬事實，第一版只有 `[ports]`。section 與 key 走白名單，不認得就報錯。檢查：容器名要存在；容器埠要在該容器的 `ports[].containerPort`；podman 軌一定要有 `[ports]`。`setting.conf` 不能覆寫 manifest 的值，要改值一律用 podman overlay 的 kustomize patch。

### 映像參照：直接寫完整參照

manifest 的 `image:` 直接寫 mirror 的完整參照，原始檔的值就是部署出去的值。`appctl check` 讀原始檔，擋短名、缺 tag、`latest` 與 kustomization 裡的 `images:`，`task lint` 與 CI 都跑。`appctl check-images <svc>` 讀渲染產物，用 skopeo 查每個參照還在來源 registry；一個 image 都沒抓到也算失敗。

### 機密：四種形式

`services/<svc>/secret/` 自帶一份 `kustomization.yaml`，裡面是 `secretGenerator`，兩軌用 `resources: [../secret]` 引用同一份。

| 形式 | 值放哪 | 怎麼進容器 | 誰有 |
|---|---|---|---|
| env 型機密 | `services/<svc>/secret/secret.env`（demo 值，進版控） | `secretGenerator` 的 `envs:` 產生 Secret；正本用 `secretKeyRef` 引用。podman 軌翻成 podman secret | 有 `secret/` 的服務 |
| 檔案型機密 | `services/quay/secret/config.yaml`（demo 值，進版控） | `secretGenerator` 的 `files:`；workload 用 `volumes[].secret` 掛成檔案 | 只有 quay（Secret `quay-conf`） |
| 非機密設定檔 | 真檔放在產生它的 kustomize 目錄（quay 在 `base/`；ncb 只在 `podman/`） | `configMapGenerator`；workload 用 `configMap.name` 掛載。podman 軌寫成主機上的檔 | quay（`initdb.sql`）、ncb（`default.conf`） |
| 服務子命令讀的檔 | gmm 的 `sync.secret.env`、`mapping.env`（demo 值，進版控）；quay 的 `secret.env` 裡 `QUAY_SUPERUSER_*` 兩鍵 | 不進容器；`appctl` 在操作者主機讀 | gmm、quay |

kustomize 的 load restrictor 擋裸檔案路徑，不擋目錄型 resource。`envs: [../secret.env]` 會失敗，`resources: [../secret]` 可以：`resources:` 指向的目錄自成一個 kustomize root，`namespace:` 也會正確套到產生的 Secret。

podman secret 整台主機共用，所以名字帶服務名與 Secret 名（`<svc>_<Secret 名>_<鍵>`），不同服務不會撞名。

### 兩軌的差異

兩軌共用 `base/` 的工作負載與映像參照，差別集中在下面幾處：

| | podman | k8s |
|---|---|---|
| 儲存 | 具名卷，名字是 `claimName`（StatefulSet 是 template 名） | PVC（叢集 StorageClass 動態供應）；`StatefulSet` 另有 `volumeClaimTemplates` |
| 對外 | `setting.conf` 的主機埠，綁主機所有介面 | Service 加 Ingress |
| 拓樸 | 每個服務一個 pod；StatefulSet 當單一 pod 處理，`replicas` 由 overlay 改成 1 | 照 manifest |
| 跨服務連線 | 經主機埠，例如 ncb 用 `host.containers.internal:9000` 連 minio | Service 名 |

同一個 pod 內的容器互連一律寫 `127.0.0.1`（gmm 的 guacd 與 mariadb、grafana 的 mariadb、quay 的 postgres 與 redis），兩軌共用，不需 overlay 改寫。把 DB 拆成獨立工作負載時才要換成 Service 名。

minio 的 podman overlay 把 `replicas` 改成 1、`args` 改成單一目錄，兩個 `volumeClaimTemplates` 併成一個 `minio-data`。

### 卷

卷直接寫在正本 workload 的 `volumes:`、`volumeMounts:`，標準 k8s 語法。

- 共享卷：同一個 pod 的多個容器掛同一個 volume。gmm 的 guacamole 與 guacd 共用 `recordings`：`RECORDING_ENABLED=true` 時 guacd 寫錄影檔、guacamole 讀來回放。ncb 的 nginx 與 certbot 共用 `certbot`：憑證與 ACME webroot（`/etc/letsencrypt/www`）都在裡面，是兩容器唯一的耦合點。沿用 `ReadWriteOnce`。
- 暫時卷：`emptyDir` 加 `medium: Memory` 與 `sizeLimit`，用在 `/dev/shm`（xadm-lxqt、xadm-xfce）。pod 重啟就清空。翻譯器不准 `emptyDir` 跨容器共用：podman 端每個容器各有一份。
- PVC 容量寫死在 `k8s/pvc.yaml`；minio 寫在 `base/statefulset.yaml` 的 `volumeClaimTemplates`。

### 服務子命令

- `appctl gmm seed|check`：讀 `sync.conf`、`sync.secret.env`、`mapping.env`，打 guacamole 的 REST API。
- `appctl quay init`：讀 `config.yaml` 的 `SUPER_USERS` 與 `secret.env` 的 `QUAY_SUPERUSER_*`，等 `/health/instance` 回 200 再 `POST /api/v1/user/initialize`。
- `appctl ncb cert`：網域讀 `default.conf` 的第一個 `server_name`，憑證寫進 `ncb-certbot` 卷，再 reload nginx。
- k8s 軌的 gmm 與 quay 由 `appctl` 自己跑 `kubectl port-forward`，從輸出讀本機埠，做完就關。

### 驗證與發布

- `task lint`：`bin/check-docs.py`、`bin/check-taskfile.py`、`go run ./cmd/appctl check`、shellcheck。用 `go run` 不用 `bin/appctl`，避免跑到舊的編譯產物。
- `task test`：`go test ./...`，再 `python3 bin/run-tests.py`。Go 測試的外部指令都經 `run.Fake`，不碰 podman、systemd 與叢集。`TestTranslateAllServices` 翻譯每個服務的 podman 軌。
- `ci.yml`：`go vet`、`go test`、`appctl check`。`release.yml` 先呼叫 `ci.yml`，通過後編 linux amd64 的 binary，附 `.sha256` 發布 Release；tag 含 `-` 時標成預發布版。

## 4. 設計取捨

| 取捨 | 選了什麼 | 代價 | 擋下它的檢查 |
|---|---|---|---|
| 控制面是一個 Go binary（`appctl`），不再用 bash 加 Python 的膠水 | bash 的 `errexit` 陷阱、`sudo` 吃 stdin、Taskfile 轉派 bash 再轉派 Python 這類坑整類消失；外部指令經 `run.Runner`，測試不碰真環境；部署主機只要裝一個檔 | 開發要 Go 工具鏈；部署主機要從 Release 裝 binary，改程式後要重新發布 | `.github/workflows/ci.yml` 的 `go vet`、`go test` |
| podman 軌用 Quadlet，不用 `podman kube play` | `kube play` 模擬 k8s 不完整：pod 名跟著 kind 變、`kube down --force` 漏刪卷、建一半的 pod 占埠、不支援 StatefulSet、沒有「停服務留卷」 | 要自己維護翻譯器，manifest 用到新欄位時要先擴充翻譯器 | `internal/quadlet` 的 `TestRejectsUnknownField`、`TestTranslateAllServices` |
| 多容器用 pod 服務加 `--pod`，不用 `Network=container:` | podman 4.9.3 沒有 `.pod` 單元。實驗結果：一個容器掛掉時，`--pod` 不影響其他容器，`Network=container:` 會連帶重建其他容器；主機埠綁在 pod 上 | pod 服務是一般 systemd 單元，放 `/etc/systemd/system/`，不在 Quadlet 目錄；pod 重建時 pod 裡的容器要一起 restart | `internal/quadlet` 的 `TestMultiContainerSharesPod`、`TestTranslateFilebrowserGolden` |
| 翻譯器只收白名單欄位，不認得就報錯 | 避免 k8s 與 podman 兩端靜默地不一樣（例如沒翻的 `resources.limits` 等於沒有限制） | manifest 加欄位前要先改翻譯器 | `internal/quadlet` 的 `TestRejectsUnknownField`、`TestRejectsHostPort` |
| 主機埠只寫在 `setting.conf`，manifest 不寫 `hostPort` | 主機埠只有一個來源；`setting.conf` 只放 manifest 表達不了的事實，不覆寫 manifest 的值 | 同一個埠的資訊分在兩處：容器埠在 manifest，主機埠在 `setting.conf` | `internal/setting` 的 `Validate`；`appctl check` 從 `setting.conf` 算撞埠 |
| podman 軌有 `stop`，k8s 軌沒有 | podman 軌的 `stop` 只是 `systemctl stop`，下次 `apply` 或重開機就回來，不會讓實況長期偏離正本；k8s 的命令式暫停會讓叢集實況與 manifest 分岔 | k8s 軌要暫停得退回 `kubectl scale --replicas=0`；DaemonSet 只能 `delete` | `internal/k8s` 的 `TestStopRejectedOnK8s`；`internal/podman` 的 `TestStopKeepsVolumes` |
| 機密檔存 demo 值並進版控，不再用 `.example` 補位 | 少一套「真值檔不在就補佔位值」的機制；渲染與 CI 不需要任何本機檔；demo 值本來就是低熵字串，公開也無妨 | repo 公開，demo 帳密也公開。實際環境直接改部署主機 clone 裡的檔，`git pull` 會衝突，主機上 `git commit -a` 會帶出真密碼 | podman 軌 `apply` 遇到 demo 值時印 `Info:`，不擋；無程式擋 |
| quay 的 `config.yaml` 也進版控 | `secretGenerator` 的 `files:` 不做變數替換，含密碼的設定檔只能整份維護；跟 `secret.env` 一樣存 demo 值 | DB 與 redis 密碼同時寫在 `secret.env` 與 `config.yaml`，要兩處一起改 | `appctl check` 與 `appctl check-secret quay` 比對兩處 |
| 服務子命令讀 repo 裡進版控的檔 | gmm 與 quay 子命令在操作者主機的 clone 跑，機密從 clone 裡的檔讀，manifest 不放真值，也不在執行期 clone repo | 一定要在 clone 裡跑；要排程得靠主機的 cron | 無程式擋 |
| 不釘 `appctl` 版本 | `install.sh` 預設裝最新的正式版，少一份要同步的版本清單 | 部署主機的 binary 可能跟 clone 的 manifest 不同代，新欄位要等主機更新 binary 才翻得過 | 無程式擋 |
| Secret 與 ConfigMap 名稱不加內容雜湊 | podman 的 secret store 沒有 owner reference 與 GC，雜湊輪替會留下永久孤兒；`kubectl delete -f -` 也只刪得掉當下那個雜湊 | k8s 軌改機密或設定不會觸發滾動更新，要手動 `rollout restart`。podman 軌由部署紀錄比對 hash，有變的單元自動 restart | 無程式擋 |
| PVC 容量寫死在 `k8s/pvc.yaml`（minio 在 `volumeClaimTemplates`） | 單一環境下這個值不會變；抽成參數會讓它看起來可以隨時調整 | 要改容量得改這些檔，而且只在第一次部署生效 | 無程式擋 |
| 不留部署鉤子（`post-apply.sh`） | 這個機制存在過三個版本，始終沒有任何服務用到 | 真的需要「部署後才能做的初始化」時要重新設計，目前由服務子命令負責 | 無程式擋 |
| 映像優先用 tag 釘，不用 digest | 和其他服務的寫法一致，比較好讀 | 同一個 tag 可能被重推。全部容器 `imagePullPolicy: IfNotPresent`，同 tag 重推後已有快取的節點或 podman 主機不會重拉，`apply` 成功但跑的是舊層。minio 與 juicefs 還是 digest，因為 mirror 沒有版本 tag | 無程式擋 |
| push 護欄是黑名單、盡力而為的第二道防線 | 主防線是真值與浮動參照不寫進版控；黑名單只能擋已知的樣式 | 沒列進黑名單的寫法擋不到 | `.githooks/pre-push` 的 forbidden 掃描本身 |
| `appctl check-images` 不進 push 護欄與 CI | 它要逐一連 registry，不是幾秒內跑得完的檢查。完整參照的離線那一半在 `appctl check` | 映像還在不在 registry，靠人記得跑 | 無程式擋 |
| podman 軌的主機埠綁主機全部介面，對實驗室網段開放 | 實驗室其他主機要直接連服務；不寫死主機 IP，換主機照用 | 每個主機埠都是 HTTP 明文：gmm 的登入密碼與遠端桌面帳密都明文傳送；主機沒有防火牆 | 無程式擋 |
| 全部服務部署到 `default` namespace，`appctl` 不建 namespace | 單一環境不需要用 namespace 隔離；`delete` 列 PVC 時可以寫死 `-n default` | overlay 的 `namespace:` 改成叢集沒有的名字，`kubectl apply` 直接失敗 | `appctl check` 擋 overlay 的 `namespace:` 不是 `default` |
| quay 的 quay-app、postgres、redis 放同一個 pod | `config.yaml` 的 DB 與 redis host 都寫 `127.0.0.1`，兩軌共用同一份，不必由 overlay 改寫 | 三容器沒有啟動順序控制（待辦在 `docs/PROGRESS.md`）；quay-app 不能獨立擴 | 無程式擋 |
| podman 軌用具名卷，不用 hostPath | 免掉建目錄、`setfacl` 給容器 uid 寫入權、清理白名單、`.gitignore` 排除 runtime 目錄四件事；所有權與生命週期由 podman 管 | 資料不在 repo 目錄下，取檔要 `sudo podman volume inspect` 找 mountpoint，備份走 `podman volume export` | 無程式擋 |

## 5. 已知的結構性限制

- podman 4.9.3 的 Quadlet 沒有 `.pod` 單元，也不支援 drop-in。pod 由一般 systemd 單元建，見〈翻譯器的產物〉。
- 本 repo 沒有週期性工作負載（Job、CronJob），翻譯器也不收。週期動作寫進容器 `command`（ncb 的 certbot 每 12 小時 `renew`、nginx 每 168 小時 reload）。
- quay 用 `LocalStorage` 寫 `/datastorage/registry`，k8s 的 PVC 是 `ReadWriteOnce`，quay-app 只能 1 個副本。
- ncb 的 `server_name` 是 `ncb.local`：`.local` 是 mDNS 保留域（RFC 6762），ACME 不簽。自簽憑證沒有 renewal 設定檔，certbot 容器的 `renew` 迴圈沒有東西可續，回 0 不報錯。要用 Let's Encrypt 得改成公網網域，而公網 DNS 不在本 repo 範圍。certbot 容器的去留列在 `docs/PROGRESS.md`。
- gmm 的 `REMOTE_IP_VALVE_ENABLED=true` 讓 guacamole 從 `X-Forwarded-For` 取來源 IP，`BAN_ENABLED` 的暴力登入封鎖才封得到人而不是封掉 Ingress。podman 軌前面沒有代理，客戶端自帶的 `X-Forwarded-For` 會被信任，可偽造繞過封鎖。
- `kadm`、`xadm-lxqt`、`xadm-xfce` 經 LoadBalancer 對外開 SSH 或 RDP，與叢集最高權（`cluster-admin`）之間只隔兩樣東西：映像內建的帳密，與一把建置時留在映像層裡的 bigred 無密碼私鑰。私鑰的來龍去脈見 images 的 `docs/ARCHITECTURE.md` 裡講映像層私鑰那條，消除列在 images 的 `docs/PROGRESS.md`。本 repo 沒有在執行期改密碼，也沒有關密碼登入。只能放在信任網段。
- `iperf3` 的 5201 埠、`juicefs` 的 S3 9005 埠沒有額外認證，只能放在信任網段。juicefs 的 metadata 是 SQLite，只能單寫；它的 minio sidecar 是單節點。
