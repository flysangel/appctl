# 進度

## 現況快照

| 項目 | 狀態 |
|---|---|
| 最新 tag | `v2.0.0`（2026-08-06）；HEAD 與 tag 同一個 commit |
| 服務 | 清單見 `git ls-tree --name-only HEAD services/`，各服務支援哪些部署路徑見 `README.md`〈服務一覽〉 |
| 引擎狀態 | 雙軌 kustomize 全遷完成（v1.13.0）；`bin/render.py` 已退場。**`envsubst` 亦已全面退場**（2026-08-04），渲染由 kustomize 原生 `namespace:` ＋ `images:` 表達 |
| 部署介面 | 兩動詞 `apply`／`delete` × 兩部署路徑，單一控制腳本 `bin/svc.sh`（**65 行**）；`build/` 中介產物已廢除 |
| 單元測試 | 全過，離線；測試檔見 `git ls-files 'tests/test_*.py'`。litellm 另有實機回歸 `task litellm:check`。`task test` 與 `python3 -m pytest tests/` 兩條路徑皆已驗證會真的執行 |
| 正本語法 | 全部部署路徑 `kubectl kustomize` 全過（2026-09-27）；工作負載正本收在 `services/<svc>/base/`，兩軌為 overlay（`minio` 例外，見 v2.0.0 條目） |
| CI | 無 |
| 工作區位置 | 本機 ext4（`/dev/sda2`），`core.filemode=true` |
| 防護規則 | 工作區共用的 pre-push。行為、例外（行尾 `gitleaks:allow`）與接線見根 repo 的 `AGENTS.md` 與 `.githooks/`；2026-09-16 之前是 platform 分發 |

服務落地狀態：**14 個全數完成雙部署路徑真環境驗證，驗畢即清**（叢集與本機皆無殘留實例）。

## 待辦

### 【建議】minio／juicefs 兩行 digest pin 改 tag pin｜代價：與其餘 12 服務形態不一致

minio 映像仍是 digest pin（同一顆 `sha256:a1a8bd4a…`）——mirror 只同步了 `latest`、沒有版本 tag 可釘。待 images repo 補 sync 規則後另輪處理。

共用 `base/` 之後這顆 digest 收斂到 **3 份** `kustomization.yaml`（minio 兩軌無法共用 base，podman 軌自帶一份）：

```bash
$ grep -rln 'digest: sha256:a1a8bd4a' services/*/*/kustomization.yaml
services/juicefs/base/kustomization.yaml
services/minio/base/kustomization.yaml
services/minio/podman/kustomization.yaml
```

## 技術債清冊

已知、已評估、刻意延後的項目。每條寫明：問題是什麼、目前為何可接受、什麼條件下必須處理。

**新增條目請附證據**（`檔案:行` 或一條命令與其輸出）。寫不出具體後果的項目不該進這份清冊。

| # | 條目 | 觸發條件 |
|---|---|---|
| 1 | quay 三容器零啟動順序控制，quay-app 會因 redis 未就緒重啟一次 | 上游改動啟動時序 |
| 2 | `gmm/sync.sh` 用禁用的 `[[ cond ]] && { ...; }` 形式 | 日後為這支腳本加上 `errexit` |
| 3 | ncb 自簽預鋪的順序要求與實測不符 | 下次動 ncb 啟動流程 |
| 4 | 兩軌 `images:` 一致性無任何機制驗證 | 改映像版本時只改了一軌 |
| 5 | `bin/assert-images.sh` 不在 pre-push 執行 | 有人改了 `images:` 卻沒跑 `task image:check` |
| 6 | quay 的 `config.yaml`（47 行）不進版控 | 換機器、或重建開發環境 |
| 7 | `DB_PASSWORD`／`REDIS_PASSWORD` 同時存在於 quay 的兩個機密檔，無一致性檢查 | 只改了其中一處 |
| 8 | K12 的 podman exit 125 假錯誤防護未經實戰 | 日後有服務的 podman 軌產出 `PersistentVolumeClaim` doc |
| 9 | kustomize 原生化執行期 deferred 的一批小項 | 動到 `bin/assert-images.sh`、`bin/svc.sh` 或 `.gitignore` 時 |

**2026-08-02 隨 `platform_ref` v4.0.0 升版關閉三條**：

| 原 # | 條目 | 怎麼解的 |
|---|---|---|
| 1 | `forbidden-patterns.txt` 與 platform 受管版本分歧 | v4.0.0 把 6 條 registry host 樣式收斂成 `docker\.io/` 一條，本 repo 零命中；sync 後本地與受管版本逐字一致，不再需要本地改動 |
| 3 | `.platform/hooks/pre-push` 的 `scripts-lint` 掛線需向上游同步 | v4.0.0 的 `pre-push` 已無該呼叫，自動對齊 |
| 5 | `hooks/` 沒有 `taiwan-patterns.txt`（當時佈局） | v4.0.0 下發，台灣化違規自此有自動攔截。**但既有 `docs/` 的違規變成日後的 push 阻塞點**，轉為待辦 1 追蹤。2026-09-16 台灣化掃描退場，該待辦隨之刪除 |

### 1. quay 三容器零啟動順序控制

*來源：image 全面拉到映像庫現存最高，2026-07-31，plan `git show 4137c7b:docs/superpowers/plans/2026-07-31-app-services-image-bump-latest.md`*

`services/quay/k8s/deployment.yaml:17-55` 與 `services/quay/podman/pod.yaml:9-48` 把 quay-app／postgresql／redis 三個容器平鋪在 `containers:` 底下，`grep -nE 'readinessProbe|livenessProbe|startupProbe|initContainers'` 對兩個檔零命中。

quay-app 一起來就打 `127.0.0.1:6379`，redis 尚未監聽即 `connection refused`，容器**重啟一次後自癒**。雙軌煙測皆通過（自癒後 3/3 Running），且此行為與釘哪個 tag 無關、非升級引入。但既然已把 quay 從 v3.8.0 跨 7 個 minor 拉到 v3.15.6，日後上游若改動啟動時序，這裡是第一個爆點。

### 2. `gmm/sync.sh` 使用禁用的 AND-list 形式

*來源：kustomize ＋ envsubst 遷移，2026-07-28，spec `git show 0bdb938:docs/superpowers/specs/2026-07-28-app-services-kustomize-envsubst-design.md`*

`services/gmm/sync.sh:69,86,103,107,110` 使用 `[[ cond ]] && { ...; }` 作為獨立語句。原本另一支 `services/ollama/sync.sh` 已在 2026-09-18 隨 ollama 服務移除。該形式在條件為假時整段回傳非零，日後若有人為這支腳本加上 `errexit` 會靜默中止。而 gmm 的 sync 具破壞性（會刪除 `mapping.list` 以外多餘的 user／group／connection）。屬既有營運腳本，不在任何案子範圍內——**別順手改**。

### 3. ncb 自簽預鋪的順序要求與實測不符

*來源：同上*

`git show ceba8ef:docs/superpowers/specs/2026-06-14-app-services-ncb-design.md` 第 207 行標「自簽預鋪（強制順序，在 up 之前）」，但實測是在 `task apply` 之後補鋪、靠 `restartPolicy=always` 自癒達成同一終態。

兩者其一不精確：不是設計文件的「強制」寫太死，就是自癒掩蓋了真需求。下次動 ncb 啟動流程會先在這裡踩到。

### 4. 兩軌 `images:` 一致性無任何機制驗證

*來源：改用 kustomize 原生，2026-08-04，spec `git show d27a282:docs/superpowers/specs/2026-08-04-app-services-kustomize-native-design.md`（K3、§10 假設 5）*

映像字面值從「每服務一份 `service.conf`」搬到「每軌一份 `kustomization.yaml` 的 `images:`」之後，雙軌服務的同一顆版號變成 2 處，juicefs 與 minio 共用的 minio digest 是 4 處（清單見上方待辦）。「兩軌 `images:` 區塊必須逐字相同」目前**只是口頭要求**，無 lint、無測試、無 hook。

只改一軌不會被任何東西攔下：兩軌各自渲染都合法、`bin/assert-images.sh` 兩軌都會通過（它只問「是不是完整參照、registry 上有沒有」，不問「兩軌是否相同」）。要發現只能靠人眼 `diff`。

`bin/manifest-diff.py` 比的是「改動前 vs 改動後」的同一軌，不是「k8s 軌 vs podman 軌」——它幫不上這個忙。

**已解決（2026-08-06，分支 `refactor/shared-base-overlay`，spec `git show def6982:docs/superpowers/specs/2026-08-06-app-services-shared-base-overlay-design.md`）**：映像字面值收進 `services/<svc>/base/kustomization.yaml`，每服務只剩一處，「兩軌是否一致」的問題結構性消失。僅 `minio` 因 `podman kube play` 不支援 `StatefulSet`、兩軌無法共用 `base/` 而仍有 2 處。節與編號保留（他處有「技術債第 N 條」引用）。

### 5. `bin/assert-images.sh` 不在 pre-push 執行

*來源：同上（K18、spec §9 界線）*

這道守衛是本次改造用來取代舊制 `${` 殘留檢查的東西——沒有它，`images:` 的 `name:` 打錯就是靜默 no-op。但 pre-push 是工作區根 repo `.githooks/` 那份護欄，`/home/desktop/wk/workspace/bin/repos.txt` 列的全部 repo 共用，本 repo 自有的檢查加不進去。所以它只能放進 `bin/` ＋ Taskfile，**由人記得跑 `task image:check SVC=<svc>`**。

這個限制在 2026-09-16 拆除 platform 前後都成立，共用護欄沒有掛各 repo 自有檢查的機制。這是一道「靠紀律執行」的守衛——與本 repo 其他防護規則的「靠機制執行」不同級。

### 6. quay 的 `config.yaml` 不進版控

*來源：同上（K8）*

kustomize 的 `secretGenerator` `files:` **不做變數替換**，這是 `envsubst` 最後還活著的理由。改為手動維護真值檔後，`bin/svc.sh` 的 32 行檔案型機密分支整段消失，代價是那份 47 行的 `services/quay/secret/config.yaml` 只存在於本機。

換一台機器要照 `services/quay/secret/config.yaml.example` 手抄，並填回 4 個機密欄位（`DATABASE_SECRET_KEY`、`SECRET_KEY`，以及 `DB_URI`／`BUILDLOGS_REDIS` 裡內嵌的密碼）。`bin/svc.sh` 的 `.example` 補位只保證跑得起來，**不保證跑得對**。

另外一個尾巴：本機現存的 `config.yaml` 是在檔頭註解修正之前生成的，首行仍是舊版描述。功能零影響（純註解），但若有人從新版 `.example` 重新生成，內容就會與 Task 12 的零漂移基線不同——**quay 的零漂移結論只對本機那一份手維檔成立**。

### 7. quay 的兩個密碼在兩個檔案裡各寫一次，無一致性檢查

*來源：同上（K8）*

`DB_PASSWORD` 與 `REDIS_PASSWORD` 同時出現在 `services/quay/secret/secret.env`（給 postgresql／redis 容器當環境變數）與 `services/quay/secret/config.yaml`（給 quay-app 自己連線用，內嵌在 `DB_URI`／`BUILDLOGS_REDIS` 字串裡）。兩處必須填相同的值。

不一致時**渲染階段零徵兆**：兩個 Secret 都產得出來、`kubectl apply -k` 也會成功，quay 起得來但連不上 DB。目前只由 `services/quay/secret/kustomization.yaml` 的註解提醒。

### 8. K12 的 podman exit 125 假錯誤防護未經實戰

*來源：同上（K12）*

`bin/svc.sh:61-62` 對 `podman kube down --force` 的非零退出碼容錯，理由是 podman 4.9.3 對含 `PersistentVolumeClaim` doc 的 yaml 會印 `Volumes removed` 之後又以 125 退出報 `no such volume`（卷其實已刪）。

但**本 repo 的 podman 軌產物一個 `PersistentVolumeClaim` doc 都沒有**——卷只以 pod `volumes[].persistentVolumeClaim.claimName` 引用。真實部署驗證（filebrowser）跑下來 rc=0，這條容錯沒被觸發過。

保留的理由是它不花成本、且日後真有服務在 podman 軌宣告 PVC 時會需要；但要清楚：**它是未經實戰的防護**，別把它當成已驗證的行為。順帶一提，同一次驗證確認了具名卷確實會被移除（`filebrowser-data`，卷數 4→3）——podman 即使 yaml 內無 PVC doc，也能從 `claimName` 追到本機具名卷。

### 9. kustomize 原生化執行期 deferred 的一批小項

*來源：同上；逐條原始紀錄在 `.superpowers/sdd/2026-08-04-app-services-kustomize-native/progress.md`（該目錄被 gitignore，不進版控，故摘要於此）*

13 個 task 的審查過程中判定「真實但不值得當場修」的項目，動到對應檔案時一併處理：

**`bin/assert-images.sh`**

- process substitution 內的 `kubectl kustomize` 失敗**不可見**——雙軌服務只要有一軌渲染壞掉，另一軌照樣產出 image、`n>0`、rc=0，仍印 `Success`
- 錯誤訊息只講 skopeo，實際上 `kubectl` 那一半失敗時訊息會誤導
- 完整參照的判準 `*/*:*` 對 `localhost:5000/foo` 這類「host 帶埠、repo 無 tag」的參照會誤判
- 抽取正則理論上可能誤抓 YAML 字串值中形似 `image:` 的行（舊制既有問題，非本次引入）

**`bin/svc.sh`**

- 動詞在驗證之前就會寫入工作區——`bash bin/svc.sh quay restart k8s` 這種打錯動詞的呼叫，仍會先建出 `secret.env` 才報「用法錯誤」
- `cp` 產出的機密檔權限是 `0664`（跟隨 umask），不是 `0600`

**`.gitignore`**

- 單數 `secret/` 的放行規則只涵蓋第一層（`!services/*/secret/*.example`），與上方複數 `secrets/` 區塊的遞迴寫法（`**/`）不對稱。目前五支服務的機密檔都在第一層，沒踩到
- `.gitignore:31` 的註解寫「複數 `secrets/` 的規則留給遷移期，遷移完成後可一併清掉」——**遷移已經完成了**，那組規則（含 `.gitignore:12` 的 `build/`）現在都沒有對應的目錄，是可以收掉的殘留；留著無害但那句話已經在等人動手

**測試與工具**

- `tests/test_manifest_diff.py` 新增的測試與檔內既有 helper 重複（`run_diff` vs `_run`、`ROOT/DIFF` vs `HERE/TOOL`），且有一處硬編 `"python3"` 而非 `sys.executable`
- `yamllint` 未安裝，24 份 `kustomization.yaml` 沒跑過語法 lint（`kubectl kustomize` 成功解析 ＋ 零漂移已間接證實語法正確）

### 交接須知（非債務）

**PVC 容量只在首次部署生效。** 叢集預設 StorageClass `local-path` 未啟用 `allowVolumeExpansion`。對既有 PVC 改容量再 apply 會直接失敗，而此時最自然的下一步（`kubectl delete pvc` 重建）**會刪掉資料**——叢集兩個 StorageClass（`local-path`、`csi-cephfs-sc`）的 `reclaimPolicy` 皆為 `Delete`。

已遷移服務的 PVC 容量寫死在正本 `pvc.yaml`、刻意不抽成參數（單一環境下此值不會變，抽出來反而讓它看起來像可以隨時調整），這條提醒因此只由本文件與 `docs/ARCHITECTURE.md` 第 6 節記錄。

### 10. GPU 推論引擎未入服務集（2026-09-18 解除）

*來源：AI 主機研究，2026-09-01，分支 `ai-stack`，主文件 runbook `apps/ai-host.md`*

解除：企業 AI 實務架構的 `ai-stack-template` 分支新增 `services/vllm/`，podman 軌以 CDI 名稱掛 GPU，在 podman 5.7.0 實測通過。ollama 服務同時移除。vllm 的 k8s 軌排在該分支工單的第 09 張。以下是解除前的原文，保留作沿革。

`services/ollama/base/deployment.yaml` 維持 CPU 形態、無 GPU 資源宣告。研究定案（該文件 §5.5）GPU 推理引擎跑**主機層 podman**（CDI 直通），不進服務集：單卡 16GB 下引擎進叢集不增任何容量（§10 實測），且 podman 軌的 `kube play` 對 GPU device 的表達方式無前例、未驗證。多卡進場時再評估 GPU overlay，屆時 taroko 的 gpu-worker 路線已驗證可用（infra-k8s `taroko/config/ai/setting.conf` 的 ai-w2）。

---

## 版本沿革

### v2.0.0（2026-08-06）

**兩軌共用 `base/`，podman 軌 `kind: Pod` 退場。**

*2026-08-06，分支 `refactor/shared-base-overlay`，spec `git show def6982:docs/superpowers/specs/2026-08-06-app-services-shared-base-overlay-design.md`（B1–B9）*

14 支服務的工作負載正本收進 `services/<svc>/base/`，兩條路徑目錄降為 overlay；`images:` 一併收斂，repin 從每軌一處變每服務一處。podman 軌工作負載與 k8s 軌對齊 kind（8 支 `Deployment` ＋ `iperf3` `DaemonSet`），`kind: Pod` 只剩 `ncb`（單軌無對齊對象）與 `minio` 的 podman 軌。`quay` 兩軌重複的 `initdb.sql` 一併去重。

解鎖這件事的是 A1 實測：`podman kube play` 對 `kind: Deployment` 支援完整（多容器／env／`secretKeyRef`／PVC→具名卷／共享卷／`hostPort` 全通過，`restartPolicy` 自動 `always`，`replicas: 2` 被夾成 1 並印警告）。2026-07-23 的 R1 曾以「跨 kind 轉換做不到」否決共用 base，而那個前提正是 A1 未驗證時留的備援路線——循環論證，A1 通過後前提消失。

`minio` 是結構性例外：`kube play` 不支援 `StatefulSet`（實測 `exit 125`），兩軌無可共用的工作負載。podman 軌的 pod 名稱因 kind 改變帶 `-pod` 後綴，`README.md` 與 `docs/ARCHITECTURE.md` 的 `podman pod stop` 用法已同步更新。

**`envsubst` 全面退場，渲染改由 kustomize 原生欄位表達。**

*2026-08-04，分支 `refactor/kustomize-native`，spec `git show d27a282:docs/superpowers/specs/2026-08-04-app-services-kustomize-native-design.md`（K1–K18）*

一個目標：把「渲染」這件事整段從 `bin/svc.sh` 移除。支撐這個判斷的實測是——全 repo 14 支服務的 manifest 佔位符**只有 `image` 與 `namespace` 兩類**，沒有第三種，而這兩類 kustomize 都有原生欄位（`images:`／`namespace:`）。

| 項目 | 之前 | 之後 |
|---|---|---|
| `bin/svc.sh` | 128 行（約 60 行在做渲染） | **64 行**，三步：守衛 → 機密補位 → `apply -k`／`kube play` |
| `service.conf` | 14 份 | **0**（映像字面值移入各軌 `kustomization.yaml` 的 `images:`） |
| `build/` 中介產物 | apply 與 delete:podman 都會產 | **廢除**（base64 機密不再落地成檔案） |
| 機密 | 帶外 `kubectl create secret` 串接在 workload 後 | `services/<svc>/secret/` 的 `secretGenerator`，兩軌 `resources: [../secret]` 共用 |
| 佔位符守衛 | `bin/svc.sh` 的 `${` 殘留檢查 | `bin/assert-images.sh` 讀渲染產物驗完整參照 |

整段消失的還有：envsubst 白名單生成、缺值檢查、檔案型機密的 32 行二次替換分支、顯式刪 Secret 的 10 行、`post-apply.sh` 鉤子（存在三個版本、全 repo 始終零個服務具備）。

**讓機密進 kustomize 成立的關鍵事實**：kustomize 的 load restrictor 擋裸檔案路徑（`envs: [../secret.env]` rc≠0），但**不擋目錄型 resource**（被指向的目錄自成一個 root）。因此不需要 `--load-restrictor LoadRestrictionsNone`，`kubectl apply -k` 可直接使用——這推翻了 v1.15.0「正本零 `secretGenerator`」的宣告。

**翻轉前案的代價與對策**：`2026-07-28` 那案拒絕短名，理由是短名是「偽裝成合法值的佔位」。這個批評成立，且被本次 skopeo 實測坐實——`nginx`、`mariadb`、`redis` 三個名字在 Docker Hub 的 `library` 命名空間真實存在，而 kustomize 對 `images:` 的 `name:` 對不上時是 rc=0 靜默 no-op。兩道對策同時成立才敢翻轉：短名一律帶服務名（漏列時 fail loud），加上 `bin/assert-images.sh` 這道渲染產物守衛。

**驗證**：

- **24 條服務-軌零漂移**：改動前後的 `kubectl kustomize` 產物語意逐一比對，`fail=0`。有機密的 5 支加跑 `--include-secrets`（base64 解碼後逐鍵比對）
- **真實部署**：filebrowser 叢集 `apply -k` → pod 1/1 Running → 容器實際拉到完整參照（非短名）→ `delete -k` 刪乾淨；filebrowser 與 quay 兩支的 podman 軌 `sudo podman kube play --replace -`（stdin）正常，quay 的 `config.yaml` 確實以檔案形式掛進 `quay-app`（`/conf/stack/config.yaml`），三容器全部 Up；quay 兩軌 `apply --dry-run=server` 8 個資源全過
- **`podman kube down --force` 對具名卷的行為**：本案開始時標記為「無證據支持」，實測**證實原宣稱為真**——`filebrowser-data` 確實被移除（卷數 4→3）。podman 即使 yaml 內無 `PersistentVolumeClaim` doc，也能從 pod 的 `claimName` 追到具名卷。`README.md`／`Taskfile.yaml`／`AGENTS.md` 三處的說法因此維持不動
- **單元測試**：`test_svc.py` 重寫為 kustomize 原生制的契約（12 支原本直接讀 `build/` 路徑的測試改為斷言 stdout），`test_manifest_diff.py` 補 `--include-secrets`；28 支全綠（`task test` 與 `python3 -m pytest tests/` 兩條路徑各驗過一次）
- **`task assert`**：14 支服務全數通過新版 `bin/assert-images.sh`

**執行期發現、寫進 spec 的兩件事**：kustomize 對長 base64 值輸出字面區塊純量（`config.yaml: |`）造成換行，而 `kubectl create secret --dry-run=client -o yaml` 輸出單行——以 `apply --dry-run=server -o json` 實證 API server 端一律正規化為無空白單行、解碼後明文相同，`bin/manifest-diff.py` 因此加了 base64 空白正規化；`podman kube down --force` 的 exit 125 假錯誤在本 repo 至今未觸發（產物無 PVC doc）。

**技術債 3 條 → 9 條**：新增 6 條（兩軌 `images:` 無一致性驗證、新守衛不在 pre-push、quay `config.yaml` 不進版控、quay 兩個密碼跨檔重複、exit 125 防護未經實戰、執行期 deferred 小項）。

**文件同步時抓到並當場修掉的三處問題**（都是本次遷移自己引入的，非既有問題）：

| 問題 | 成因 | 修法 |
|---|---|---|
| `services/static-nginx/k8s/kustomization.yaml:3` 命中 forbidden 樣式，**本分支 push 會被擋** | 該行新註解為了解釋「短名為什麼不能用裸容器名」，寫出帶尾斜線的 Docker Hub 主機路徑，正好命中 `hooks/forbidden-patterns.txt:12` 的 `docker\.io/`；`.platform/hooks/pre-push:80` 的豁免只有 `versions.conf`／`docs`／`hooks`／`taskfile`，**`services/` 不在內** | 改寫措辭為「Docker Hub 官方庫真的有一個叫 nginx 的映像」，資訊未刪減、樣式不命中（同 repo 的 `services/openwebui/k8s/kustomization.yaml:3` 一直是這種寫法）。全 `services/` 複掃確認僅此一處 |
| `task test` 是假綠燈，`test_svc.py` 18 支從沒在 `task test` 裡跑過 | **舊版 `tests/test_svc.py` 本來有 `__main__` 執行段**（`12b412e:tests/test_svc.py:459-467`），重寫為 pytest 形式時整段漏掉。`Taskfile.yaml:21` 的 `python3 tests/test_svc.py` 自此零輸出、rc=0。後續每個 reviewer 都直接跑 `pytest`（那條會動），所以沒人踩到 | 補回檔尾執行段，風格照 `tests/test_manifest_diff.py`：蒐集 `test_` 開頭的可呼叫物件、逐支跑、印 `[OK] <name>`、收尾印 `ALL 18 PASS`。**例外刻意不 catch**，讓失敗自然以非零退出——已用「注入一支必失敗測試」的對抗測試確認 rc=1 且印出 traceback，不是空轉 |

| `Taskfile.yaml:38` 仍描述已刪除的 `post-apply.sh` 鉤子 | K15 刪掉該機制、Taskfile 的其餘描述也在同批更新過，唯獨 `apply` 的 summary 漏了這一句 | 整行刪除——該段 summary 少了這句語意仍完整（前三句已交代轉派對象、兩軌各自的命令、namespace 來源），不需要改寫補話 |

前兩者的教訓已分別寫進 `AGENTS.md` 地雷區 7（註解也算新增行）與「驗證改動」段（新增測試檔必補 `__main__`，判據是有沒有印出 `ALL <n> PASS`）。

**文件漂移表複審**：`AGENTS.md` 的「已知的文件漂移」逐條複驗後從 4 條收到 **2 條**（`.platform/hooks/pre-push:3` 的道數——受管檔只能回 platform 端修；ncb spec 的順序敘述）。移除的三條裡，`Taskfile.yaml:38` 是清掉已刪機制的敘述，另兩條是 `bin/svc.sh` 的跨檔引用：

- `:19` 引用 `AGENTS.md:101`——那原本正是終端輸出慣例表的 `Info:` 列，**本次改寫 `AGENTS.md` 把它推到第 119 行**，引用就這樣無聲指到「語言」小節標題。這條漂移是本案自己造成的
- `:32` 引用 `.claude/rules/script-output.md:16-18`——**該檔在本 repo 根本不存在**（同名檔在工作區的另一個 Claude 設定目錄底下，不屬於本 repo），照著找一定撲空。這條比行號歪掉更嚴重，且早於本案

兩處都改成不帶行號的「見 `AGENTS.md` 的『終端輸出』慣例表」——**正解是拿掉行號而不是改行號**，否則下次任何人動 `AGENTS.md` 都會再打歪一次。`bin/` 現已無任何帶行號的跨檔引用（`grep -rn '\.md:[0-9]\+' bin/` 為空）。

這個失效模式的共通點是**壞掉時完全無聲**：散文行號隨改寫整批位移，而引用是單向的，被引的檔不知道誰指著它，沒有 lint／測試／hook 會發現。慣例已寫進 `AGENTS.md` 的「文件邊界與引用方式」段：引用散文檔只指章節名，行號留給程式碼；缺陷登記（漂移表、技術債清冊）因為要精確定位、且每次複驗會重新核對，屬明文例外。

---

**`platform_ref` v3.1.4 → v4.0.0，接回兩道防護規則。**

壓了一輪的升版阻塞在複驗時發現早已不成立：v4.0.0 把 6 條 registry host 樣式收斂成 `docker\.io/` 一條，本 repo 14 個 `service.conf` 全走 `quay.io/` 與 `ghcr.io/`、零命中；`pre-push` 也已無 `bin/scripts-lint.sh` 呼叫。sync 後本地受管檔與 platform 端逐字一致，**零本地改動**——這正是受管檔該有的狀態。

接回的兩道：

| 新檔 | 作用 |
|---|---|
| `.platform/hooks/taiwan-patterns.txt` | 台灣化 lint（14 個無歧義簡體字 ＋ 8 個大陸用語）。此前完全無自動攔截 |
| `.platform/hooks/junk-paths.txt` | 路徑面掃描，擋 `__pycache__/`、`*.pyc` 等建置殘留入庫 |

一併回填 `template/{pod,deployment,service}.yaml`——三者曾被當「未用範本」刪掉（`7d8680c`），但當時受管檔一體下發，本 repo 用不到不等於可以刪。（後續：platform v5.0.0 把 `template/` 與另兩支移出受管清單、所有權轉給各 repo，本 repo 於 2026-08-05 接 v6.2.0 時實測零引用而刪除。）

掃描時另外抓到兩件事：

- ~~**`platform-sync.sh` 的 `chmod +x` 清單漏列三個檔**~~ **已解決**：platform v6.0.0 改成從受管清單推導，不再寫死檔名
- **`AGENTS.md` 的用語對照表自己撞上 taiwan 掃描**——那一行為了教「別用這些詞」而把詞逐個列出，於是同時命中其中五個。改法不是開豁免，而是**別重列清單**：語言段改為指向 `.platform/hooks/taiwan-patterns.txt` 這份機器可讀的正本，只留清單刻意不收的有歧義詞。同一個坑在本檔的待辦 1 也踩過一次（原本寫死了統計表，改成給可重跑的指令）

**技術債 6 條 → 3 條**（關閉 `forbidden-patterns.txt` 分歧、`scripts-lint` 掛線、缺 `taiwan-patterns.txt`）。既有 `docs/` 的台灣化違規轉為待辦追蹤——不主動翻修歷史決策紀錄，踩到時順手正名。

**驗證**：`contract-lint` 通過；`task test` 26 項全過；`kubectl kustomize` ok=24 fail=0；**新的六道 pre-push 實跑 RC=0**。

---

**工作區從 CIFS 遷至本機 ext4，一批 CIFS 專屬的地雷隨之失效。**

`246be9f fix: 補回 5 個腳本的執行位`——`bin/manifest-diff.py`、`services/{ollama,openwebui}/sync.sh`、`tests/*.py` 五檔有 shebang 但入庫模式是 `100644`，無法直接 `./` 執行。現已全數 `100755`。

背景：舊工作區在 CIFS 掛載（`/home/desktop/fs/pv/GitWork`，已不存在），exec bit 表現不穩定且 `core.filemode=false` 讓它不入版控。當時的文件因此記了三條地雷——「pre-push 的 exec bit 會靜默掉、可能整批 push 未經掃描」「新增可執行檔要 `git update-index --chmod=+x` 手動補模式」「既有的 `100644` 不要順手修正」。

現況（2026-08-02 實測）：

```
$ findmnt -n -o SOURCE,FSTYPE --target .
/dev/sda2 ext4

$ git config --get core.filemode
true

$ git ls-files -s | awk '$1!="100644"' | wc -l
15        # 全部該可執行的檔都是 100755
```

**三條地雷已於本次文件收斂中整段移除**，不再保留失效敘述——AGENTS.md 的地雷區只留現在還會踩到的。2026-08-01 那次「push 完全未經掃描」的事故紀錄保存於此段，事後已手動補掃 RC=0。

**文件收斂**：`docs/tech-debt-inventory.md` 整份併入本檔的「技術債清冊」段後刪除。`docs/` 底下自此只有 `ARCHITECTURE.md`、`PROGRESS.md`、`superpowers/` 三處。

同批修掉的文件↔程式碼衝突：`ARCHITECTURE.md` 誤植「三個受管檔」（實為兩個）、`platform_ref` 升版阻塞的敘述已過期（v4.0.0 實測已放寬）、`.forbidden-allow` 的 `tests/test_overlay.py` 陳跡改列入待辦。

**驗證**：`task test` 26 項全過；24 個部署路徑 `kubectl kustomize` 全過（`ok=24 fail=0`）。

### v1.17.0（2026-08-02）

兩批內容：**全服務映像升版** ＋ **核心文件重建**。**零引擎改動**。

#### 一、全服務映像拉到映像庫現存最高

8 個服務 14 行。

| 服務 | 變更 |
|---|---|
| gmm | 三件套 v1.0.6→v1.0.7（當初因 mirror 未有而落後，阻礙已消失） |
| grafana | grafana 11.6.15→13.1.1；mariadb 11.4.12→12.3.2（12.3 為 LTS） |
| ncb／static-nginx | nginx 1.27.4→1.31.3（補上 CVE-2026-42533／CVE-2026-60005）；certbot v5.6.0→v5.7.0 |
| iperf3 | ubuntu knife-v1.0.3→v1.0.4（連帶 `images_ref` v1.2.0→v1.8.4） |
| ollama | 0.30.8→0.32.5 |
| openwebui | v0.9.6→v0.11.0 |
| quay | quay v3.8.0→v3.15.6；postgres 15.1→**13.23**（刻意降版，Quay 3.15 官方搭 postgresql-13）；redis 1-92→1-1785388756 |

ollama 與 openwebui 一併由 digest pin 改為 tag pin。**取捨**：與其餘 12 服務形態對齊、較可讀；代價是同一個 tag 可能被重推，不像 digest pin 保證不可變。

**誠實界限**：minio 兩行（minio 本體、juicefs 的 sidecar）仍為 digest pin——mirror 只同步 `latest`、無版本 tag 可釘。

**驗證**：8 服務 15 個部署組合雙路徑真環境煙測，驗畢即清。

#### 二、核心文件重建

原 `README.md`（29.6 KB／97 行）把系統簡介、用法、架構、逐版驗證細節全堆在一起，且多處與程式碼不符（宣稱 v1.17.0 已發版、`sync` 用法漏 `sync.secret.env`、仍描述已退場的 `template/svc` 三動詞重構、`start` 動詞舊名）。拆為四份各有職責的文件：

| 文件 | 內容 |
|---|---|
| `README.md`（7.8 KB） | 系統簡介、服務一覽、環境需求、用法、驗證 |
| `AGENTS.md` | 給 AI agent 的地雷區、開發規範、常見任務 |
| `docs/ARCHITECTURE.md` | 渲染管線逐步拆解、機密三條路徑、設計決策與理由 |
| `docs/PROGRESS.md` | 本檔——現況、待辦、版本沿革、各服務落地紀錄 |

一併修正 25 檔註解（24 個 `kustomization.yaml` ＋ quay 的 `config.yaml.example`）指向已於 v1.16.0 退場的 per-service 部署腳本，改指 `bin/svc.sh`；補上 README 從未描述的 v1.14.0／v1.15.0／v1.16.0（內容取自 tag 訊息）。

掃描時發現、原文件未載的兩項事實已寫入：pre-push 的 exec bit 會靜默失效（實測有一次 push 完全未經掃描），以及全工作區無 CI。

**驗證**：`task test` 26 項全過；24 個部署路徑 `kubectl kustomize` 全數通過；手動補跑 pre-push 四道 RC=0。

### 引擎與介面演進

| tag | 日期 | 內容 |
|---|---|---|
| `v1.16.0` | 2026-07-30 | **兩動詞介面 ＋ 腳本去重**。`start`／`stop`／`delete` 縮為 `apply`／`delete`；15 份逐字相同的部署腳本（md5 全等、零客製）收斂為單一 `bin/svc.sh`，服務名改由第一參數傳入。`stop` 退場——它是介面中唯一的命令式動詞，`replicas=0` 與 manifest 正本分岔且下次 `apply` 會靜默拉回。服務層級的分歧能力改由 `post-apply.sh` 鉤子提供。驗證：24 個（服務, 部署路徑）渲染產物改動前後逐字零差異、單元測試全綠、static-nginx 真跑 apply 與 delete 全程 |
| `v1.15.0` | 2026-07-30 | **三動詞單檔部署**。14 服務改用 `services/<svc>/<svc>`（125 行）取代 319 行共用腳本；機密改帶外注入（`kubectl create secret`），正本零機密宣告、24 個部署路徑可直接 `kubectl kustomize`；`service.conf` 直寫完整映像字面值與 `NAMESPACE`。`bin/` 退場 565 行、舊測試與範本退場 830 行、Taskfile 15→7 個 task |
| `v1.14.0` | 2026-07-26 | sync 密碼移出 `sync.conf` 至 `sync.secret.env` |
| `v1.13.0` | 2026-07-25 | **雙軌 kustomize 全遷**。14 服務從 `service.yaml`→`render.py` 引擎遷成 `services/<svc>/{podman,k8s}/` 原生 kustomize 正本；`render.py`（538 行）＋`test_render.py` 引擎退場；新增 `bin/manifest-diff.py` 零漂移比對器。順帶 rdp 家族 repin `k8sadm-v1.0.1`→`v1.0.2`。驗證：語意零漂移 ＋ 14 服務逐一雙路徑真環境煙測 |
| `v1.12.x` | 2026-06-29 ～ 07-05 | 接 platform v3.1.0～v3.1.4；用語在地化（大陸用語正名台灣慣用語、mirror 語境改用英文）；quay `SERVER_HOSTNAME` 移入設定檔 |
| `v1.11.x` | 2026-06-27 ～ 06-29 | admin 三件套改指 `k8sadm` 映像，補齊 in-pod kubectl／kubeconfig；接 platform v3.1.0 taskfile 解耦 |
| `v1.10.x` | 2026-06-18 ～ 06-26 | `render.sh` 歸位 `bin/`；抽 `_headless_tail` 收斂 StatefulSet／DaemonSet 三重複，並劃 `service.yaml` 範圍界線（封頂 `render.py`，不重造 kustomize） |

### 服務落地順序

第一批（Phase 5 ②，原 `oss_ncb`＋`oss_quay`）✅ 封印 v1.0.0，來源 repo 已封存（各 `archived-2026-06-14` tag）。
第二批（Phase 5 ③，原 `srv_config` add-on）✅ 封印 v1.12.x。由淺入深：filebrowser → static-nginx → minio → openwebui → ollama → admin 三件套 → juicefs → iperf3。
第三批（企業 AI 實務架構，分支 `ai-stack-template`）2026-09-18 完成實驗室 podman 軌：移除 ollama、新增 vllm，litellm 與 openwebui 改造。K8s 軌與 H100 暫停，還沒發版。

---

## 各服務落地紀錄

以下為各服務首次落地時的驗證內容與誠實界限。**誠實界限＝當時沒做到、或做到但不是它看起來那樣的部分**，不是待辦，是防止日後誤讀驗證結論。

### grafana（v0.1.0）
雙部署路徑驗證通過：health 200 且 `database=ok`、102 張表寫進 mariadb、dashboard 跨重啟存活。

### gmm（v0.2.0）
雙部署路徑驗證通過：guacamole 登入取 authToken、baked schema 自初始化、guacamole ＋ guacd 共用 `recordings`（k8s 共 2 個 PVC 而非 3）、`task sync` 冪等佈建連線（REST ＋ mariadb 對帳、nmap 驗活、cleanup 未映射）、連線跨重啟存活、k8s Ingress `gmm.local` 連通。

### quay（v0.3.0）
雙部署路徑驗證通過：檔案卷注入（ConfigMap `initdb.sql` 跑 pg_trgm、Secret `config.yaml` 渲染後 quay 讀取）、`/health/instance` 200（database／registry 全 ok）、superuser 初始化 ＋ OAuth、`podman login` ＋ busybox push／pull 落地 `/datastorage/registry`、repo 跨重啟存活、k8s 2 PVC ＋ Ingress `quay.local`。

pg-data 用具名卷解 postgres `0700`（CIFS 開發主機）；push／pull 走 http(:8080) `--tls-verify=false`。

### ncb（v0.4.0）
podman 路徑驗證通過：nginx ＋ certbot 反向代理前門、檔案卷 nginx conf 渲染成 ConfigMap、HTTP 80→301、HTTPS 443 自簽 TLS handshake ＋ cert CN、`proxy_pass` 真穿後端、cert 在具名卷跨重啟存活。

**誠實界限**：certbot 自動取證／續期需公網 ACME，隔離 lab 為 documented future work。k8s 路徑不產出——反向代理 ＋ TLS 由叢集 Ingress ＋ cert-manager 負責。

### filebrowser（v1.1.0，第二批首切）
雙部署路徑驗證通過：單容器網頁檔案總管、`command` 包裝 postStart（mkdir served root）、admin 登入 ＋ 上傳／列檔真落地、跨重啟存活、k8s PVC ＋ Ingress。**零引擎改動**。

### static-nginx（v1.2.0）
雙部署路徑驗證通過：單 nginx 容器服務內容卷、port 80、跨重啟存活、k8s PVC ＋ Ingress。零引擎改動；與第一批 ncb（反向代理前門）改名區隔。

### minio（v1.3.0）
雙部署路徑**異拓樸** S3：

- podman 單節點：`mc put`／`get` 物件、具名卷 `minio-data` 跨重啟存活
- k8s 分散式 `StatefulSet`：replicas 2 ＋ 4 個 `volumeClaimTemplates` ＋ headless Service ＋ `podManagementPolicy: Parallel`、跨節點 2 pod、`mc admin info` 顯示 **4 drives online EC:2**、刪 `minio-0` 後物件存活（PVC 持久）

**誠實界限**：lab 單主機 2-VM 下 erasure coding 是形狀驗證、**不是真容錯**——持久性源於 PVC 而非 EC。

### openwebui（v1.4.0）
單容器 ＋ 具名卷；admin 經 `task sync` REST 種子（冪等 signin／signup、token 不回顯、搶註偵測 fail-loud）。雙部署路徑驗證通過：`/health` 200、種下首位 admin（`ENABLE_SIGNUP=False` 仍可 bootstrap）、登入、跨重啟存活；k8s 刪 pod 後經 Ingress signin admin 仍在（PVC 持久）。

**安全時序**：部署完成→立即 sync 種 admin→才對外。

**誠實界限**：v1.4.0 當時不含 chat completion（ollama 後端另切）——**ollama v1.5.0 落地後經其後端全棧真 chat 打通**。k8s 路徑映像約 5GB，需 lab 主機磁碟餘量。

### ollama（v1.5.0）
LLM 推理後端，銜接 openwebui 的 `OLLAMA_BASE_URL`：單容器 Deployment ＋ ClusterIP Service `ollama:11434`、CPU-only、具名卷 `ollama-models`、模型經 `task sync` 拉（`POST /api/pull`，以 `/api/tags` 為冪等判據）。

驗證：podman 單體 sync 拉 `qwen2.5:0.5b` → `POST /api/chat` **真 LLM 推理回 "pong"**；k8s 全棧經 Service DNS → openwebui `/api/chat/completions` **真 chat completion 回 "Pong!"**——首次端到端真 LLM。**零引擎改動**。

**誠實界限**：CPU 推理很慢；openwebui 預設的 `DEFAULT_MODELS` 未拉，以顯式 `qwen2.5:0.5b` 證路徑連通；🔴 **podman hostPort 11434 零認證暴露，僅限信任網段**。

2026-09-18 移除，由 vllm 取代，見下方〈企業 AI 實務架構：實驗室 podman 軌〉。

### kadm（v1.6.0）
k8s 管理 SSH 堡壘 ＋ pod 內 podman：`ServiceAccount` ＋ `ClusterRoleBinding`→`cluster-admin`、Service `LoadBalancer`（`allocateLoadBalancerNodePorts: false`、22100→22）、容器 `privileged`。

驗證：`kubectl exec` 證 cluster-admin（`get nodes`／`auth can-i *`）、`podman version`（pod 內 podman）、PVC 可寫、sshd 握手、刪 pod 後 SA／CRB 叢集級持久。

**🔴 安全**：cluster-admin 經對外 SSH，「對外入口」與「叢集最高權」只隔一道**映像內建的 SSH 帳密**（不可輪換，外洩＝叢集淪陷）→ LoadBalancer／NodePort 嚴禁對非信任網段。

**誠實界限**：lab 無 LB provider（external-IP `<pending>`，真對外需 MetalLB）；須部署在 `default` namespace（CRB subject 釘死）。

### xadm-lxqt（v1.7.0）／xadm-xfce（v1.8.0）
k8s 圖形管理工作站，兩者同構：LXQt／XFCE4 桌面 via xrdp（3389）＋ sshd（22）、雙埠 Service `LoadBalancer`（33890→3389、22100→22）、RBAC cluster-admin、容器 `privileged`、`emptyDir` `/dev/shm`（`medium: Memory`、2Gi）＋ PVC `wk`。

驗證：Service 雙埠 jsonpath、exec 證 xrdp／sshd daemon ＋ listen、**RDP X.224 Connection Confirm 協定握手** ＋ SSH banner、`df /dev/shm` 證 tmpfs 2Gi、`CapBnd` 全展開證 privileged、刪 pod 後 cluster-admin ＋ PVC 持久。

**🔴 安全**：同 kadm——cluster-admin 經對外 RDP／SSH，只隔一道映像內建帳密。

**誠實界限**：無頭 lab 無法真渲染 GUI，只證 daemon ＋ listen ＋ 協定握手；v1.7.0／v1.8.0 當時的桌面映像**不自帶 kubectl／podman**（v1.11.0 改指 `k8sadm` 家族後補齊）；lab 無 LB provider；映像很大需 prune；`/dev/shm` 隨 pod 重啟重置；須部署在 `default` namespace。

### juicefs（v1.9.0）
JuiceFS 分散式檔案系統，**S3-gateway 形態**（pivot 離源 CSI Driver——CSI 是叢集 add-on，不適合單服務模型）。單 pod 雙容器自足：minio sidecar 物件後端（PVC `obj`）＋ juicefs gateway 前門（:9005，SQLite metadata PVC `meta`）；`command` wrapper 等 minio 起來→`juicefs format`（冪等、自動建 bucket）→`gateway`；127.0.0.1 loopback、cache 用 `emptyDir`。

驗證（雙部署路徑）：經 gateway `mc cp` 放進單一 bucket `jfs`→**直接 ls minio 後端只見 juicefs 切塊物件、無原檔名**（＝資料切塊與 metadata 分離）→`mc cat` 取回 byte 一致→重啟後 obj ＋ meta 卷持久、檔案仍在。**零引擎改動**。

**誠實界限**：SQLite metadata **單寫**（gateway 單副本；多寫／HA 需升 Redis，未做）；minio sidecar 單節點（非分散式 EC，持久源於 PVC）；🔴 **單一憑證對前後端共用**（簡化，生產應分離）；S3 :9005 認證僅靠 credentials，僅限信任網段。

### iperf3（v1.10.0）
網路頻寬測試 server（`iperf3 -s`，:5201）。**雙部署路徑異拓樸**：podman 單 Pod（單點測）／ k8s `DaemonSet`（每個 worker 節點一份，跨節點測）＋ headless Service。

驗證：podman `iperf3 -c 127.0.0.1` → **44.35 Gbps**；k8s DaemonSet 每 worker 一 pod（control-plane 因 taint 排除）＋ headless endpoints ＋ exec 對各節點 pod IP `iperf3 -c` → node→node Gbps 矩陣（同節點 58、跨節點 18.5 Gbps）。

**誠實界限**：lab 節點是同一台實體主機上的 VM，跨節點頻寬是 **VM-to-VM 虛擬路徑（flannel CNI）**、不是實體 NIC；DaemonSet 預設不容忍 control-plane taint；無認證（:5201 開放，僅限信任網段）；無狀態、無持久卷。

### 企業 AI 實務架構：實驗室 podman 軌（2026-09-18，未發版）

實驗室主機一張 RTX 4060 Ti 16GB，只走 podman 軌。工單在 `git show ef20562:docs/tickets/2026-09-17-企業-AI-實務架構/` 的 01 到 08，每張的〈執行紀錄〉有完整指令與輸出。

| 服務 | 本輪做了什麼 |
|---|---|
| vllm | 新增，僅 podman 軌。一個 pod 三個容器：chat、embed、rerank 同卡共存，以 CDI 名稱掛 GPU。權重經 `task sync SVC=vllm` 下載進具名卷，容器以離線模式啟動 |
| litellm | 別名改接 vllm，加 Cloudflare 與 Groq 兩個雲端來源。自寫 hook 先做 DLP 再做自動分流。保存完整 prompt 90 天，網頁使用者靠 email 標頭歸戶 |
| openwebui | 改接 pgvector 的 PostgreSQL，知識庫走 litellm 的 embedding 與 reranker。系統提示詞、常駐橫幅、語音開關由 `task sync` 寫入 |
| ollama | 移除 |

驗證：下列功能都在主機上實測過，含瀏覽器驗收。它們是網頁聊天、知識庫、請求紀錄、自動分流、coding agent（OpenCode）與 DLP。`task test` 共 57 項全過。

**誠實界限**：

- 自動分流不是資安邊界。使用者可以在訊息裡要判斷模型答 HARD，資料外洩靠 DLP 把關
- Claude Code 經 litellm 沒有實測，實驗室沒有 Claude 憑證
- 判斷模型逾時與出錯的路徑沒有實測
- Groq 免費方案每分鐘只有 8000 token，OpenCode 一次請求約 1 萬 token 就超過
- 實驗室不提供語音：語音轉文字放進去之後，GPU 沒有空間給朗讀
- vllm 的 k8s 軌還沒做，排在第 09 張

2026-09-19 起，litellm 與 openwebui 的 podman 軌對實驗室網段開放，推翻定案第 56 題。工單在 `git show 6bc35a1:docs/tickets/2026-09-18-實驗室網段開放-OpenWebUI-與-LiteLLM/`。

2026-09-19 起，litellm 不上 Redis，`/ui` 不再顯示 Redis 提示。要加 worker 或副本時，照 `AGENTS.md` 地雷區第 17 條做。工單在 `git show 2c6f5db:docs/tickets/2026-09-19-LiteLLM-不上-Redis-並關掉提示/`。

2026-09-19 起，DLP 與自動分流改用 LiteLLM 內建功能，自寫 hook 與兩支離線測試拿掉了。這推翻定案第 12、14 題。工單在 `git show 059639b:docs/tickets/2026-09-19-LiteLLM-內建功能取代自寫-DLP-與分流/`，三張的〈執行紀錄〉有候選打分與實測輸出。

- 自動分流用 `complexity_router`，由 `chat-local` 判斷難度，判斷失敗留在地端
- DLP 是 `custom_code` guardrail `tw-dlp`，只綁 `chat-cloud` 與 `chat-cloud-alt`，含圖片也阻擋
- 直接指定雲端別名時回一則 DLP 訊息，狀態碼 200
- `chat-auto` 命中時經 fallback 改由地端回答，沒有提示。使用者選這個做法，推翻本輪拷問第 5 題的「一律阻擋」
- 驗證改成 `task test` 28 項，加上 `task test:live` 對正式 litellm 跑 26 則
- 評估期間，測試用的假個資四次送到 Cloudflare，都是回歸腳本的假資料

### 正式環境 H100：podman-h100 軌首次安裝（2026-09-20，未發版）

正式主機 `ubs26041`（192.168.1.156）兩張 H100 NVL 94GB。工單在 `git show d7d5142:docs/tickets/2026-09-19-H100-正式主機首次安裝/`，設定用 `task h100:bundle` 打包成壓縮檔，USB 搬過去解開。

| 服務 | 本輪做了什麼 |
|---|---|
| vllm | 新增 `podman-h100` 軌。一個 pod 四個容器都掛 `nvidia.com/gpu=0`：chat（27B）、router（2B 分流小模型）、embed、rerank |
| vllm-omni | 新服務，只有 `podman-h100` 一軌。FLUX.2-klein-4B 生圖，別名 `image-local`，獨占 GPU 1 |
| litellm | 新增 `config.h100.yaml.example`，比實驗室多 `chat-router` 與 `image-local` 兩個別名，共八個。`chat-auto` 的判斷模型改成 `chat-router` |
| openwebui | 新增 `podman-h100` 軌，設定與實驗室相同 |

實驗室軌同時改名：`podman/` 改成 `podman-4060ti/`，`bin/svc.sh` 與 `bin/assert-images.sh` 改成認得 podman 開頭的每一個軌。其餘 12 個服務維持 `podman` 軌。

驗證：四個服務都在 H100 上起來，從別台電腦登入 OpenWebUI 聊得通、生得出圖。GPU 0 用掉 75,694MiB／95,830MiB，GPU 1 待機 16,845MiB。操作紀錄在 runbook 的 `operations/ai-services.md` 第 5 節。

**誠實界限**：

- 知識庫問答、`task test:live`、coding agent 三項這一輪沒驗，排在第 09 張〈H100 剩餘驗收〉
- 第 05 張的記憶體參數還沒寫回 repo，狀態仍是待實作
- 生圖的峰值只有目測，沒有留下 `nvidia-smi` 原文。原定案排 GPU 1 切 MIG 的前提是生圖吃掉 72.7GiB，實測差很多，下一輪要重新評估
- OpenWebUI 的圖片設定另有一份 `IMAGES_OPENAI_API_KEY`，與聊天的 `OPENAI_API_KEYS` 是兩個 PersistentConfig 鍵。manifest 讓兩者取同一個值，第一次啟動填錯就要打 API 改

### 正式環境 H100：兩張卡重新分工（2026-09-20，未發版）

接續上一條。上一輪四個 vLLM 容器全擠 GPU 0、生圖獨占 GPU 1；這一輪按實際角色重分，順便把向量模型升級、把微調搬上 H100。工單在 `git show e94f8d4:docs/tickets/2026-09-20-H100-兩張卡重新分工/`。

兩張卡的新分工：

| 卡 | 跑什麼 | 比例 |
|---|---|---|
| GPU 0 | chat（`Qwen3.8-27B-FP8`）、router（`Qwen3.5-2B`） | 0.82、0.10 |
| GPU 1 | embed（`Qwen3-Embedding-4B`）、rerank（`Qwen3-Reranker-4B`）、生圖 | 0.12、0.12、0.70 |

| 服務 | 本輪做了什麼 |
|---|---|
| vllm | chat 的 context 從 131072 拉到原生 262144 並改用 fp8 KV cache，比例 0.72 升 0.82。embed 與 rerank 從 0.6B 換 4B，改掛第二張卡。router 一行沒動 |
| vllm-omni | 補上 `--gpu-memory-utilization 0.70`。原本沒設，獨占 GPU 1 時沒事；多了兩個鄰居之後會用預設比例去搶記憶體 |
| openwebui | H100 軌補 `RAG_EMBEDDING_QUERY_PREFIX`。Qwen3-Embedding 的查詢端要帶英文 instruction，值含換行 |
| finetune | `podman/` 改名 `podman-4060ti/`，新增 `podman-h100/`：微調 `Qwen3.8-27B`、4bit 關掉、批次 4、序列 4096、掛 GPU 1。`sync.sh` 改吃 `TRACK`，不給預設軌 |
| vllm 的微調變體 | `finetuned/` 改名 `finetuned-4060ti/`，新增 `finetuned-h100/` 掛合併後的 BF16 權重。BF16 的 27B 比 FP8 多吃 21 GiB，所以這個變體的 context 降回 131072 |
| 打包 | `bin/bundle-h100.sh` 的服務清單加 `finetune`，軌掃描補 `finetuned*` 並保住 `finetuned-h100`。`tests/test_bundle.py` 從四個服務改五個 |
| train.py | `per_device_train_batch_size` 與 `load_in_4bit` 改讀環境變數，兩軌共用同一支腳本，預設值維持實驗室那組 |

微調與生圖不共存：訓練時 GPU 1 要清空，連帶整個 `vllm` 都要停，所以**訓練期間聊天與知識庫都不能用**。這是 pod 層級的限制，podman 停不掉單一容器。

驗證：33 個部署路徑 `kubectl kustomize` 全過、`task test` 89 項全過、三支 `assert-images.sh` 全過、`task h100:bundle` 產得出壓縮檔且同一個 commit 打兩次逐位元相同。

**誠實界限**：

- **實機一步都沒跑。** 所有記憶體數字都是推估，實機部署與量測由使用者做，步驟在第 05 張〈H100 實機清單〉
- 同一個 pod 的容器各掛不同卡沒驗過；含換行的查詢前綴傳進容器之後是不是還是兩行也沒驗過。見上面〈H100 兩張卡重新分工的數字全是推估〉
- 上一輪留的四張舊工單狀態欄一張都沒碰，依使用者裁決「舊的忽略，開新的」。`2026-09-17-企業-AI-實務架構` 的第 11、12 張與 `2026-09-19-H100-正式主機首次安裝` 的第 05、09 張，狀態都還寫著待實作，內容卻已經被做掉或作廢
- 微調的批次 4 與序列 4096 沒有實測依據，第一次訓練要看記憶體再調

### task 依職責收斂（2026-09-24，未發版）

go-task 任務名依職責收斂，判準見工作區 `.claude/rules/yaml.md`〈五類職責〉與〈動詞表〉。

新名字：`image:check`（原 `assert`）、`litellm:check`（原 `test:live`）、`seed` 與 `seed:check`（原 `sync` 與 `sync -- test`）、`h100:build`（原 `h100:bundle`）。

`seed` 帶 `--` 之後的參數會被 `preconditions` 擋下，唯讀檢查改用 `task seed:check`。這避免舊寫法（`task sync SVC=gmm -- test`）默默漏掉 `-- test`，變成對 gmm 的破壞性寫入——gmm 的 seed 會刪除 `mapping.list` 以外多餘的帳號與連線。

### prompt-audit 稽核修正（2026-09-25，未發版）

`AGENTS.md` 地雷區第 5 條的「12 個 repo」改成指向工作區根 repo 的 `/home/desktop/wk/workspace/bin/repos.txt`，不寫死數字。其餘文件的舊數字見〈待辦〉的「五處文件還寫 12 個 repo」。
