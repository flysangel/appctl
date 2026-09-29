# 架構

本檔說明 app-services 的組織方式、渲染管線與設計取捨。使用方式看 `README.md`，進度看 `PROGRESS.md`。

文中的「企業 AI 實務架構定案第 N 題」「第 NN 張」指的是工單，已移出版控。用 `git show ef20562:docs/tickets/2026-09-17-企業-AI-實務架構/` 列出檔名，再用 `git show ef20562:docs/tickets/2026-09-17-企業-AI-實務架構/<檔名>` 取回。定案題號在 `00-定案摘要.md`。

## 1. 一句話架構

**每個服務一個目錄，目錄裡是各部署路徑的 kustomize 正本（映像字面值與機密宣告都寫在 `kustomization.yaml` 裡）；一支控制腳本只做守衛與確認，部署本身交給 `kubectl apply -k` 與 `podman kube play`。**

沒有中央引擎、沒有自訂 schema、沒有模板語言，**連字串替換都沒有**。`bin/render.py`（538 行）已於 v1.13.0 退場；接手的「`kubectl kustomize` ＋ `envsubst`」組合也已退場一半——`envsubst` 全面移除，佔位符僅有的兩種用途（image 與 namespace）改由 kustomize 原生欄位 `images:` 與 `namespace:` 表達。

## 2. 目錄佈局

```
Taskfile.yaml              唯一入口，任務清單用 task --list-all 看

bin/
  svc.sh                   ★ 部署控制腳本（唯一正本，65 行）
  assert-images.sh         讀 kubectl kustomize 產物：每個 image 須為完整參照，且仍存在於來源 registry
  manifest-diff.py         語意零漂移比對器（改造期工具，非日常流程）
                           以上皆為本 repo 自有

.gitattributes             腳本強制 LF
tests/                     離線單元測試，各支的覆蓋範圍見〈測試〉
                           ＋ live_gateway.py：litellm 的實機回歸，由 task litellm:check 呼叫，不離線

services/<svc>/
  base/                    ★ 工作負載正本，兩軌共用；檔名＝kind 小寫
    kustomization.yaml     ★ resources: 工作負載 ＋ images:（映像完整字面值）
    config.yaml            （僅 litellm）模型清單與 DLP 設定，整份入庫，產生 ConfigMap litellm-config
  {podman,k8s}/            ★ overlay：可直接 kubectl kustomize
    kustomization.yaml     ★ namespace: ＋ resources: [../base]；podman 軌另有 hostPort 的 JSON6902 patch
                             有機密者另加 ../secret；minio 的 podman 軌不引用 ../base（見 §5）
  secret/                  （僅 gmm／grafana／juicefs／litellm／minio／openwebui／quay 七支）機密宣告，兩軌共用
    kustomization.yaml     secretGenerator ＋ disableNameSuffixHash: true（入庫）
    secret.env.example     機密範本（入庫，佔位值）
    secret.env             機密真值（gitignored）
    config.yaml{,.example} （僅 quay）檔案型機密，真值 gitignored、範本入庫
  sync.sh                  （選配）營運工具，哪些服務有見 ls services/*/sync.sh
  agents/                  （僅 litellm）coding agent 的設定範例與發 key 的告知文字，不進 kustomize
```

`build/` 中介產物已廢除：k8s 軌直接 `kubectl apply -k`，podman 軌把 `kubectl kustomize` 的輸出經 stdin 餵給 `podman kube play`。要看渲染結果就現跑 `kubectl kustomize services/<svc>/<軌>`，base64 機密不再落地成檔案。單一服務的完整工作負載得從 `services/<svc>/base/` 讀起——overlay 只帶該軌差異。

`bin/` 現在只剩本 repo 自有的腳本，清單見 `ls bin/`。push 護欄不在本 repo 裡，見〈防護規則（guardrail）〉。

沿革：`render-core.sh`、`validate-cronjob.sh` 與 `template/*.yaml` 曾由 platform 分發，在本 repo 零消費者。2026-08-05 已刪除，要找回就跑 `git log --all -- template/ bin/render-core.sh bin/validate-cronjob.sh`。

## 3. 部署管線

`task apply SVC=grafana TRACK=k8s` 實際發生的事（`bin/svc.sh`）：

```
① 守衛       services/<svc>/<軌>/ 目錄存在？（一道檢查涵蓋服務名打錯與該軌不支援） svc.sh:9
      ↓
② 機密補位   secret/{secret.env,config.yaml} 缺席則自同名 .example 複製佔位值      svc.sh:13-26
      ↓
③ 執行       k8s：kubectl apply -k｜podman：kubectl kustomize | podman kube play  svc.sh:37-39
```

**沒有第四步。** 渲染整段不在腳本裡：`apply -k` 由 kubectl 內建的 kustomize 直接吃目錄；podman 軌只因為 `podman kube play` 不懂 kustomize，才多一次 `kubectl kustomize` 把產物經 stdin（`-`）餵過去。舊制的白名單生成、缺值檢查、殘留佔位檢查、帶外 Secret 生成、檔案型機密的二次 `envsubst`、`build/` 落地，整批消失。

`delete` 多兩件事（`svc.sh:41-62`）：先要求輸入服務名確認（`confirm()`，無 tty 時 `read` 讀到 EOF 即中止、什麼都不刪），再走 `kubectl delete -k --ignore-not-found` 或 `podman kube down --force -`。兩個軌各有一道原生語意要處理：

- **k8s**：`StatefulSet` 的 `volumeClaimTemplates` PVC 由叢集動態建立、不在 manifest 內，因此不隨 `delete` 移除。腳本刪完會把這些 PVC 列出來並提示手動清除的指令（全 repo 僅 minio 命中）。
- **podman**：podman 4.9.3 對含 `PersistentVolumeClaim` doc 的 yaml，`kube down --force` 會印 `Volumes removed` 之後又以 125 退出、報 `no such volume`——卷其實已刪，是假錯誤（用檔案參數 100% 重現，與 stdin 無關）。`set -e` 下不容錯會誤中止，所以那條命令後面掛了 `|| echo "Info: ..."`。**這道容錯至今未實戰觸發**：現行 podman 軌產物都沒有 `PersistentVolumeClaim` doc。

### 為什麼短名一律帶服務名

manifest 裡的 `image:` 寫的是**短名**（`filebrowser`、`grafana-mariadb`），完整字面值只出現在同軌 `kustomization.yaml` 的 `images:` 區塊。短名不是佔位符——它本身就是一個合法的容器映像參照，`images:` 沒對上時會被解析成 Docker Hub `library` 命名空間下的官方映像。

`2026-07-28` 那案正是為此拒絕短名：它是**偽裝成合法值的佔位**，誤用未經轉換的正本會跑起錯版本、錯來源，且不產生任何錯誤訊息。本次調查把這個批評坐實得更難看：

- kustomize 對 `images:` 的 `name:` 對不上時 **rc=0、無警告、產物原封不動**（靜默 no-op）
- skopeo 實測：`nginx`、`mariadb`、`redis` 三個名字在 Docker Hub 的 `library` 命名空間**真實存在**

兩件事湊在一起，代表若用裸容器名當短名（ncb 的 `nginx`、grafana 的 `mariadb`、quay 的 `redis`），只要 `images:` 漏列或 `name:` 打錯，就會拉到**能正常啟動的錯映像**——比跑不起來更糟。

本案採兩道對策同時成立，才敢翻轉前案的決定：

1. **短名一律帶服務名**：單容器服務用 `<svc>`，多容器服務用 `<svc>-<容器名>`，一句話零例外。`ncb-nginx`、`grafana-mariadb`、`quay-redis` 在 Docker Hub 的 `library` 底下都不存在，漏列時 fail loud。代價是 `grafana-grafana`、`quay-quay-app` 這類重複字樣讀起來笨拙，換來的是規則不需要 lint 執法。
2. **渲染產物守衛**：`bash bin/assert-images.sh <svc>` 讀 `kubectl kustomize` 的輸出，任一 `image:` 未解析為完整參照（不含 `/` 也不含 `@sha256:`）即 rc=1；零命中同樣視為失敗（舊制在 `service.conf` 沒有鍵時會零次疊代卻印成功，是永久假綠燈）。這道檢查取代舊制的 `${` 殘留檢查。

⚠️ 第二道**不在 pre-push 執行**——它放在 `bin/`，只有 `task image:check SVC=<svc>` 會跑。pre-push 是工作區共用的護欄，本 repo 不能自行加規則（見〈防護規則（guardrail）〉與 `AGENTS.md` 地雷區第 5 條）。

### 缺機密檔會讓整條路徑跑不動

`secret/` 目錄的 `secretGenerator` 讀的是 `secret.env`／`config.yaml` 這兩個 gitignored 的真值檔。它們缺席時 kustomize 直接 rc=1（`evalsymlink failure`），**連 `kubectl kustomize` 這種純本地語法驗證都跑不動**，`apply -k`、`delete -k` 一併壞掉。

`bin/svc.sh:13-26` 因此在做任何事之前先補位：真值檔缺席就自同名 `.example` 複製佔位值。`apply` 時另外印兩行 `Info:`，說明現在用的是佔位值、不是真密碼——**那兩行不是雜訊**。它代表這次 apply 出去的服務會連不上相依元件；minio 更會拿假的 root 帳密把資料卷建起來，之後換真值得砍卷重來。

## 4. 機密的三條路徑

正本目錄不含機密**真值**，但**確實有機密宣告**。`services/<svc>/secret/` 自帶一份 `kustomization.yaml`，裡面就是 `secretGenerator`。兩軌以 `resources: [../secret]` 引用同一份宣告。七支有機密的服務都是這樣：gmm、grafana、juicefs、litellm、minio、openwebui、quay。其餘服務**不建**這個目錄。`resources` 指向不存在的目錄會讓 kustomize 直接失敗，為了結構統一建空殼純屬儀式。

> **這推翻了本節原本的核心主張。** v1.15.0 立下的是「正本零 `secretGenerator`、機密一律帶外注入」，理由是 kustomize 讀不到目錄外的機密檔。讓翻轉成立的關鍵事實是：**kustomize 的 load restrictor 擋的是裸檔案路徑，不擋目錄型 resource**——`envs: [../secret.env]` rc≠0，但 `resources: [../secret]` 成立（被指向的目錄自成一個 kustomize root），且 `namespace:` 會正確下傳到生成的 Secret。因此**不需要** `--load-restrictor LoadRestrictionsNone`，`kubectl apply -k` 可以直接使用，帶外的 `kubectl create secret` 整段退場。

| 形式 | 真值放哪 | 怎麼進容器 | 誰有 |
|---|---|---|---|
| **env 型機密** | `services/<svc>/secret/secret.env`（gitignored） | `secretGenerator` 的 `envs:` 生成 Secret；正本以 `secretKeyRef`／`envFrom` 引用 | gmm、grafana、juicefs、minio、quay |
| **檔案型機密** | `services/<svc>/secret/config.yaml`（gitignored），範本 `.example` 入庫 | `secretGenerator` 的 `files:` 直吃真值檔；workload 以 `volumes[].secret` 掛成檔案 | 僅 quay（Secret `quay-conf`） |
| **非機密設定檔** | 真檔直接放在正本目錄內 | kustomization 的 `configMapGenerator`，workload 以 `configMap.name` 掛載 | quay（`initdb.sql`）、ncb（`default.conf`）、litellm（`config.yaml`） |

檔案型機密**沒有變數替換**——kustomize 的 `files:` 不做替換，這曾是 `envsubst` 最後還活著的理由。改為手動維護真值檔後，`bin/svc.sh` 的 32 行檔案型分支整段消失；代價是 quay 那份 47 行 `config.yaml` 不進版控，換機器要照 `.example` 手抄並填回機密欄位。

litellm 的 `config.yaml` 不含機密：金鑰都寫成 `os.environ/` 參照，公司關鍵字 `KEYWORDS` 是示範值。所以它整份入庫，放在 `base/`，由 `configMapGenerator` 產生 ConfigMap `litellm-config`。名稱照 quay 的 `quay-pg-init` 關掉雜湊，manifest 才能寫死引用。代價是 k8s 軌改設定後 `apply` 不會重啟 pod，要手動 `kubectl rollout restart deployment/litellm`。podman 軌的 `kube play --replace` 每次都重建 pod，不受影響。

Secret 名稱**不加內容 hash**（`generatorOptions.disableNameSuffixHash: true`）。podman 的 secret store 沒有 owner reference 與 GC，hash 輪替會留下永久孤兒；`delete -k` 也只刪得掉「當下這個 hash」。這不是回歸——舊制 `kubectl create secret generic <svc>` 本來就是固定名，改機密同樣不會觸發滾動更新。代價是 hash 政策寫在兩軌共用的 `secret/`，無法分軌設定。

`delete` 不再需要「依名稱顯式刪 Secret」的十行：`kubectl delete -k` 涵蓋 `secretGenerator` 產出的 Secret，`kustomization` 的 `namespace` 也正確套用到 DELETE 請求。代價是 `delete` 從此依賴機密檔在場（`secret/` 讀不到真值檔就整條失敗），由第 3 節的 `.example` 補位兜底。

## 5. 兩條部署路徑的關係

**兩條路徑是各自獨立維護的原生 kustomize 正本，不是從同一來源機械導出的。**

慣例上共用映像／env／port／容器結構，差別集中在下表四處：

| | podman | k8s |
|---|---|---|
| 儲存 | 具名卷（named volume）——`persistentVolumeClaim.claimName` 經 `podman kube play` 對應到本機具名卷 | PVC（叢集 StorageClass 動態供應）；`StatefulSet` 另有 `volumeClaimTemplates` |
| 對外 | hostPort，綁主機所有介面；AI 服務（`litellm`、`openwebui`）因此對實驗室網段開放 | Service ＋ Ingress |
| 跨 pod 名稱 | 「Deployment 名加 `-pod`」，例如 `litellm-pod`；base 寫死 Service 名的 env 由 overlay 改寫 | Service 名，例如 `litellm` |
| GPU 資源鍵 | CDI 名稱 `nvidia.com/gpu=all`，由 overlay 從 base 的 K8s 鍵換過來 | `nvidia.com/gpu` |

後兩列的坑與實測版本見 `AGENTS.md` 地雷區第 12 條。

拓樸不同的服務，兩邊各自宣告自己的 workload kind：

- `minio`：podman 單節點 `Pod` / k8s `StatefulSet` replicas 2 ＋ 4 個 `volumeClaimTemplates` ＋ headless Service
- `iperf3`：podman 單 `Pod` / k8s `DaemonSet`（每個 worker 節點一份）＋ headless Service

工作負載與 `images:` 都收在 `services/<svc>/base/`，每服務各一份，「兩軌是否一致」的問題結構性消失。唯一例外是 `minio`：`podman kube play` 不支援 `StatefulSet`，兩軌無可共用的工作負載，它的 podman 軌自帶一份 `images:`；juicefs 與 minio 共用的那顆 digest 因此仍散在 3 份 `kustomization.yaml`（`juicefs/base`、`minio/base`、`minio/podman`）。

`bin/manifest-diff.py` 是改造期用的語意零漂移比對器（依 `(kind, name)` 排序、遞迴排序鍵、podman 路徑把 hostPath 正規化為 PVC 引用；預設剔除 Secret，加 `--include-secrets` 則把 Secret 也納入，base64 解碼後逐鍵比對並正規化 kustomize 的長值折行），**不在日常流程裡**。

## 6. 卷策略

卷宣告直接寫在正本 workload 的原生 `volumes:`／`volumeMounts:`，標準 k8s 語法，沒有額外自訂 schema。

- **共享卷**＝同一個 volume 被多個容器掛載（同一個底層 PVC，多處掛載）。例：gmm 的 guacamole 與 guacd 共用 `recordings`。沿用 `ReadWriteOnce`（同 pod 同節點即可）；多副本跨節點共享需要 RWX，非目前形態。
- **暫時卷**：`emptyDir` ＋ `medium: Memory` ＋ `sizeLimit`，用於 `/dev/shm`（xadm-lxqt／xadm-xfce 各 2Gi）。隨 pod 重啟重置。
- **PVC 容量寫死在正本 `pvc.yaml`，刻意不抽成參數**——單一環境下這個值不會變，抽出來反而讓它看起來像可以隨時調整。實際上叢集預設 StorageClass 未啟用 `allowVolumeExpansion`，**容量只在首次部署生效**，改了再 apply 會直接失敗（見 `PROGRESS.md` 的交接須知）。

## 7. 上游契約

app-services 不釘任何上游版本，沒有 `versions.conf`。images repo 建好的映像推上 registry 後，版號直接寫進下面說的 `images:` 區塊，不讀 images 的版本清單（image manifest）。

push 護欄不算上游契約：它不經版本釘選，全部 repo 共用工作區根 repo 的那一份（見〈防護規則（guardrail）〉）。

各服務映像的完整字面值（含 registry host）寫在 **`services/<svc>/base/kustomization.yaml` 的 `images:` 區塊**，兩軌共用，manifest 裡只留短名。

舊制寫在每服務一份的 `service.conf`，取的是「一行讀完這個服務跑哪一版」；kustomize 原生化時這個好處一度讓了出去（版號變成每軌 1 處），共用 `base/` 後**收了回來**——版號回到每服務 1 處，兩軌 `images:` 不一致的問題結構性消失。僅 `minio` 因兩軌無法共用 `base/` 而仍有 2 處。換來的是零字串替換、正本可直接 `kubectl apply -k`。

`images:` 本身有兩個寫法上的硬規則：`newTag:` 的值一律加雙引號（純數字 tag 如 `13.23` 不加引號是 rc=1 硬錯誤），digest 型映像用 `newName:` ＋ `digest:` 兩欄、**不給 `newTag:`**（兩者同時給會產出 `repo:TAG@sha256:...`，runtime 只認 digest、tag 淪為假資訊）。

forbidden 掃描對 registry host 只擋 Docker Hub 主機名，樣式見根 repo 的 `.githooks/forbidden-patterns.txt`。本 repo 的 `images:` 走 `quay.io/`、`ghcr.io/` 與 `public.ecr.aws/`，零命中。

⚠️ 零命中指的是**整個 repo 的新增行**，不只 `images:` 的值。本次改造一度打破零命中。`services/static-nginx/base/kustomization.yaml` 的檔頭註解為了解釋「短名為什麼不能用裸容器名」，寫出帶尾斜線的 Docker Hub 主機路徑。forbidden 掃描照掃 `services/`。已改寫為不命中樣式的措辭（資訊未刪減）。**註解也算新增行**，這是最容易忘的一類命中。

## 8. 防護規則（guardrail）

push 時跑的護欄是工作區共用的，本 repo 內沒有護欄檔。護欄的行為、例外（行尾 `gitleaks:allow`）與接線，見根 repo 的 `AGENTS.md` 與 `.githooks/`。接線由根 repo `dot/.gitconfig` 的 `includeIf` 提供，本 repo 不設 local 的 `core.hooksPath`，設了反而會蓋過 includeIf。

⚠️ **`bin/assert-images.sh` 不在護欄裡。** 它是本次改造新增的守衛（渲染產物的每個 `image:` 必須解析為完整參照）。共用護欄只收全部 repo 都適用的檢查，本 repo 自有的守衛加不進去。它因此只能放進 `bin/`、由 `task image:check SVC=<svc>` 呼叫——**要記得跑，push 不會幫你跑**。

沿革：2026-09-16 之前護欄由 platform 分發到本 repo 的 `.platform/`。拆除後改由工作區根 repo 共用一份。

防護規則是**黑名單、最佳努力**的第二道防線。主防線是「真值與浮動參照永不寫死入庫」。

## 9. 設計決策與理由

| 決策 | 理由 | 代價 |
|---|---|---|
| 只有 `apply`／`delete` 兩個動詞，**不提供 `stop`** | 命令式的暫停會讓叢集實況與 manifest 正本分岔，下次 `apply` 會靜默把服務拉回 | 要暫停得退回原生工具（`kubectl scale --replicas=0`／`podman pod stop <svc>-pod`；`ncb` 與 minio podman 軌的 pod 名仍是 `<svc>`） |
| 工作負載收在共用 `base/`，兩條路徑只寫各自差異 | 兩軌工作負載對齊 kind 後，kustomize 原生 base／overlay 就抽得乾淨，不需自訂抽象。2026-07-23 的 R1 曾以「跨 kind（Pod vs Deployment）轉換做不到」否決此案，而那個前提正是 podman 軌用 `Pod`——A1（`kube play` 吃 `Deployment`）未驗證時留的備援路線，屬循環論證；A1 實測通過後前提消失 | `minio` 兩軌拓樸無法對齊（`kube play` 不支援 `StatefulSet`，實測 exit 125），是結構性例外；podman 軌的 `hostPort` 得靠 overlay patch 帶入 |
| 映像字面值寫在各軌 `kustomization.yaml` 的 `images:`，不做 `${REGISTRY_PREFIX}` 間接層，也不再經 `envsubst` | 零字串替換、正本可直接 `kubectl apply -k`；佔位符只有 image／namespace 兩類，kustomize 原生欄位 100% 覆蓋 | 版號收斂為每服務 1 處（`base/kustomization.yaml`）；僅 `minio` 因兩軌無法共用 base 而仍有 2 處 |
| manifest 只寫短名，短名一律帶服務名（`<svc>`／`<svc>-<容器名>`） | 短名在 Docker Hub 的 `library` 命名空間一律不存在，`images:` 漏列時 fail loud；規則零例外，不需要 lint 執法 | `grafana-grafana`、`quay-quay-app` 這類重複字樣讀起來笨拙 |
| 機密以 `secretGenerator` 宣告在 `services/<svc>/secret/`，兩軌 `resources: [../secret]` 共用 | `kubectl apply -k`／`delete -k` 直接涵蓋 Secret，腳本不必自己建、自己刪 | 真值檔缺席時整條路徑（含純本地 `kubectl kustomize`）rc=1，得靠 `.example` 佔位值兜底 |
| PVC 容量寫死正本 | 單一環境不會變；抽成參數會誤導成可調 | 要改容量得改正本，且只在首次部署生效 |
| 部署鉤子 `post-apply.sh` 刪除 | 機制存在了三個版本，全 repo 始終**零個**服務具備；註解提到的 openwebui 用法從未真的做出來 | 日後真需要「部署後才能做的初始化」，得重新設計（`task seed` 目前扮演這個角色） |
| 自動分流用 LiteLLM 內建的 `complexity_router`，由 `chat-local` 以 LLM 判斷難度。設定在模型清單 `chat-auto` 那一筆，簡單送 `chat-local`、困難送 `chat-cloud`，判斷失敗留在地端 | 2026-09-19 改用內建功能。這推翻了企業 AI 實務架構定案第 14 題的分流部分，該題是 DLP 與自動分流合成一支自寫 hook。實測兩種判斷方式。LLM 判斷在第 05 張的情境全對，另 10 題探測對 7～8 題。heuristic 加中文關鍵字的探測只對 5 題，沒命中關鍵字的困難題都留在地端 | 分流不看圖片。含圖片的困難題分到雲端時，`tw-dlp` 阻擋它，再經 fallback 由地端回答，圖片不會送出去。知識庫聊天整段送判斷模型。MEDIUM 送地端，部分困難題會留在地端。判斷模型的呼叫由 LiteLLM 內部發出，升版要重驗 |
| DLP 用 LiteLLM 內建的 guardrail：一段 `custom_code` 叫 `tw-dlp`，只綁在 `chat-cloud` 與 `chat-cloud-alt` 上，阻擋個資、公司關鍵字與圖片。直接指定雲端別名時命中就阻擋；`chat-auto` 帶個資或圖片時改由地端回答 | 2026-09-19 改用內建功能，推翻企業 AI 實務架構定案第 12 題與第 14 題。實測五個候選，只有模型層 guardrail 沒有外洩、也不連地端一起阻擋。`content_filter` 與只看 `model` 的 `custom_code` 都有路徑把個資送上雲端。對所有模型都檢查的 `custom_code` 會連送地端的請求一起阻擋。模型層 guardrail 在 fallback 到雲端時也會檢查。`chat-auto` 帶個資改由地端回答，是使用者在實作時選的 | 阻擋時回 200 加一則合成回答，agent 看到的不是錯誤。`chat-auto` 改由地端回答時沒有提示，靠 `chat-auto` 到 `chat-local` 的 fallback 才成立。`chat-auto` 不能綁任何 guardrail，綁了雲端別名的 `tw-dlp` 就不會跑。新增雲端模型忘了綁 `tw-dlp` 就不檢查。LiteLLM 升版要重跑 `task litellm:check` |
| podman 軌的 `litellm`、`openwebui` 不設 hostIP，對實驗室網段開放。2026-09-18 定案、2026-09-19 起開放，推翻企業 AI 實務架構定案第 56 題「只給主機本機連」 | 實驗室其他主機要直接用網頁與 agent。manifest 不寫死主機 IP，換主機照用；本機的 127.0.0.1 照常能連 | 連線是 HTTP 明文，LiteLLM 的 `/ui` 用 master key 登入，master key 也會明文傳送。主機沒有另設防火牆，連得到這台主機的網段都能連 8080 與 4000。OpenWebUI 開放註冊，陌生的待核准帳號要由 admin 刪除 |
| LiteLLM 不上 Redis，只跑 1 個 worker 與 1 個副本，並關掉 `/ui` 的 Redis 提示 | 限流、預算與路由狀態，加了 worker 或副本之後才需要共用。企業 AI 實務架構定案第 36 題是「單副本，零停機以後再考慮」。LiteLLM 只轉送請求，推論在 vLLM，加 worker 或副本也不會變快 | 重新部署 LiteLLM 時服務中斷，沒有零停機。加 worker 或副本時要上 Redis，並拿掉關提示的環境變數；加副本還要把 PostgreSQL 搬出 pod。見 `AGENTS.md` 地雷區第 17 條 |

## 10. 測試

| 檔案 | 覆蓋 |
|---|---|
| `tests/test_svc.py` | 軌別守衛與錯誤訊息、`delete` 無 tty／輸入不符即中止、機密檔自 `.example` 補位（含既有檔不覆寫、`apply` 才印提醒）、短名解析為完整參照、裸 `$VAR` 存活、`images:` 的 `name:` 對不上時靜默 no-op、Secret 名稱無 hash 後綴、缺 `secret.env` 時渲染失敗、`assert-images.sh` 對未解析短名與零 image 皆 rc=1 |
| `tests/test_litellm_config.py` | litellm 設定入庫：k8s 與 podman-4060ti 兩軌都渲染出 ConfigMap `litellm-config` 且內容等於 `base/config.yaml`、Secret `litellm` 不含 `config.yaml`、Deployment 的設定卷掛 ConfigMap、`base/config.yaml` 沒被 `.gitignore` 排除 |
| `tests/test_manifest_diff.py` | 比對器的允許清單、鍵序無關、podman hostPath 正規化、真差異必須抓到、`--include-secrets` 的預設關閉／值漂移偵測／base64 折行正規化 |

上表各支皆**離線**，都把 `KUBECONFIG` 指向不存在的路徑。`kubectl kustomize` 是純本地操作，不受影響。需要連叢集的操作會失敗，測試不可能動到真實資源。2026-09-19 前另有 `test_auto_route.py` 與 `test_dlp.py` 測自寫 hook，自寫 hook 拿掉時一起刪了。

上表各支都能以兩種方式執行：`task test`（`python3 tests/<檔>.py`，逐支印 `[OK]`、收尾印 `ALL <n> PASS`）與 `python3 -m pytest tests/`。**兩條路徑都得能跑**。`test_svc.py` 曾經漏掉檔尾的 `__main__` 執行段，`task test` 對它零輸出、rc=0。結果當時那 18 項一項都沒跑，卻報成功（見 `PROGRESS.md` 版本沿革）。判斷有沒有真的在跑，看 `ALL <n> PASS` 那一行。

另有一支**不離線**的實機回歸 `tests/live_gateway.py`，由 `task litellm:check` 呼叫，不在上表裡：

| 檔案 | 則數 | 覆蓋 |
|---|---|---|
| `tests/live_gateway.py` | 26 | 對執行中的 litellm 跑 DLP 18 則與分流 8 則。DLP 那組取自企業 AI 實務架構第 07 張。另加的有串流、困難題帶個資、fallback 外洩探測、檢查碼對錯與圖片。分流那組取自第 05 張，含圖片、知識庫聊天與圖片經 fallback 的探測。阻擋比對 200 合成回答的開頭。放行比對回應標頭 `x-litellm-model-name`：`hosted_vllm/` 開頭算地端，其餘算雲端 |

檔名刻意不以 `test_` 開頭，`python3 -m pytest tests/` 不會收進去，離線測試不會打到正式服務。它每則都印結果，全部跑完才決定退出碼，所以和上表各支不同，不靠例外往上拋。
