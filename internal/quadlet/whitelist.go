package quadlet

import (
	"fmt"
	"sort"
	"strings"
)

// 白名單：翻譯器只認得下面這些葉節點路徑。陣列索引寫成 []；
// 以 .* 結尾的樣式代表底下任意鍵（labels、data 的鍵名不固定）。
// 不在白名單的欄位一律報錯、不產生單元：漏翻的欄位在 podman 端會被靜默丟掉，
// 例如 resources.limits 沒翻就等於沒有限制。

var common = []string{
	"apiVersion",
	"kind",
	"metadata.name",
	"metadata.namespace",
	"metadata.labels.*",
}

// podSpec 是 Pod 的 spec，或工作負載的 spec.template.spec。
var podSpec = []string{
	"containers[].name",
	"containers[].image",
	"containers[].imagePullPolicy",
	"containers[].command[]",
	"containers[].args[]",
	"containers[].env[].name",
	"containers[].env[].value",
	"containers[].env[].valueFrom.secretKeyRef.name",
	"containers[].env[].valueFrom.secretKeyRef.key",
	// 只收 containerPort 與 protocol。hostPort 刻意不收：主機埠只有 setting.conf 一個來源。
	"containers[].ports[].containerPort",
	"containers[].ports[].protocol",
	"containers[].volumeMounts[].name",
	"containers[].volumeMounts[].mountPath",
	"containers[].volumeMounts[].subPath",
	"containers[].securityContext.privileged",
	"volumes[].name",
	"volumes[].persistentVolumeClaim.claimName",
	"volumes[].configMap.name",
	"volumes[].secret.secretName",
	"volumes[].emptyDir", // emptyDir: {}
	"volumes[].emptyDir.medium",
	"volumes[].emptyDir.sizeLimit",
	"restartPolicy",
	// serviceAccountName 在 podman 端沒有意義：認得但不翻。只有這一個欄位這樣處理，
	// 其他不翻的欄位都要報錯。
	"serviceAccountName",
}

var workloadExtra = map[string][]string{
	"Pod": nil,
	"Deployment": {
		"spec.replicas",
		"spec.selector.matchLabels.*",
		"spec.template.metadata.labels.*",
	},
	"DaemonSet": {
		"spec.selector.matchLabels.*",
		"spec.template.metadata.labels.*",
	},
	"StatefulSet": {
		"spec.replicas",
		"spec.serviceName",
		"spec.podManagementPolicy",
		"spec.selector.matchLabels.*",
		"spec.template.metadata.labels.*",
		"spec.volumeClaimTemplates[].metadata.name",
		"spec.volumeClaimTemplates[].spec.accessModes[]",
		"spec.volumeClaimTemplates[].spec.resources.requests.storage",
	},
}

var otherKinds = map[string][]string{
	"ConfigMap": {"data.*"},
	"Secret":    {"type", "data.*"},
}

// allowed 回傳 kind 的白名單；kind 不支援時回 nil, false。
func allowed(kind string) ([]string, bool) {
	out := append([]string{}, common...)
	if extra, ok := otherKinds[kind]; ok {
		return append(out, extra...), true
	}
	extra, ok := workloadExtra[kind]
	if !ok {
		return nil, false
	}
	out = append(out, extra...)
	prefix := "spec.template.spec."
	if kind == "Pod" {
		prefix = "spec."
	}
	for _, p := range podSpec {
		out = append(out, prefix+p)
	}
	return out, true
}

// flatten 把文件攤平成葉節點路徑。空 map 與空陣列本身算葉節點。
func flatten(v any, path string, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			*out = append(*out, path)
			return
		}
		for k, c := range t {
			p := k
			if path != "" {
				p = path + "." + k
			}
			flatten(c, p, out)
		}
	case []any:
		if len(t) == 0 {
			*out = append(*out, path)
			return
		}
		for _, c := range t {
			flatten(c, path+"[]", out)
		}
	default:
		*out = append(*out, path)
	}
}

func match(patterns []string, path string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, ".*"); ok {
			if strings.HasPrefix(path, prefix+".") {
				return true
			}
		} else if p == path {
			return true
		}
	}
	return false
}

// checkWhitelist 檢查每份文件，回傳全部不合規的項目（已排序、去重）。
func checkWhitelist(docs []map[string]any) []string {
	seen := map[string]bool{}
	for _, d := range docs {
		id := docID(d)
		kind, _ := d["kind"].(string)
		pats, ok := allowed(kind)
		if !ok {
			seen[id+"：不支援的 kind"] = true
			continue
		}
		var leaves []string
		flatten(d, "", &leaves)
		for _, l := range leaves {
			if !match(pats, l) {
				seen[id+"："+l] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func docID(d map[string]any) string {
	md, _ := d["metadata"].(map[string]any)
	return fmt.Sprintf("%v/%v", d["kind"], md["name"])
}
