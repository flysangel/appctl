package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
	"github.com/flysangel/appctl/internal/setting"
)

type portKey struct {
	host  string
	port  int
	proto string
}

// loadGroups 讀 config/port-groups.conf：一行一組，# 開頭是註解。檔案不存在時回空。
func loadGroups(root string) []map[string]bool {
	b, err := os.ReadFile(filepath.Join(root, "config", "port-groups.conf"))
	if err != nil {
		return nil
	}
	var groups []map[string]bool
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		g := map[string]bool{}
		for _, f := range strings.Fields(line) {
			g[f] = true
		}
		groups = append(groups, g)
	}
	return groups
}

// hostOf 把軌名換成主機鍵：podman 是 default，podman-<host> 是 <host>。
func hostOf(track string) string {
	if track == "podman" {
		return "default"
	}
	return strings.TrimPrefix(track, "podman-")
}

func isSubset(set map[string]bool, group map[string]bool) bool {
	for s := range set {
		if !group[s] {
			return false
		}
	}
	return true
}

func checkNamespace(root, svc, track string) string {
	b, err := os.ReadFile(filepath.Join(root, "services", svc, track, "kustomization.yaml"))
	if err != nil {
		return fmt.Sprintf("%s/%s：讀不到 kustomization.yaml", svc, track)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return fmt.Sprintf("%s/%s：kustomization.yaml 解析失敗", svc, track)
	}
	if ns, _ := doc["namespace"].(string); ns != "default" {
		return fmt.Sprintf("%s/%s：kustomization.yaml 的 namespace 是 %v，要寫 default", svc, track, doc["namespace"])
	}
	return ""
}

func clusterAdminInfos(svc string, docs []map[string]any) []string {
	admin := false
	for _, d := range docs {
		ref, _ := d["roleRef"].(map[string]any)
		if d["kind"] == "ClusterRoleBinding" && ref["name"] == "cluster-admin" {
			admin = true
		}
	}
	if !admin {
		return nil
	}
	var infos []string
	for _, d := range docs {
		spec, _ := d["spec"].(map[string]any)
		if d["kind"] != "Service" || spec["type"] != "LoadBalancer" {
			continue
		}
		if ranges, _ := spec["loadBalancerSourceRanges"].([]any); len(ranges) > 0 {
			continue
		}
		md, _ := d["metadata"].(map[string]any)
		infos = append(infos, fmt.Sprintf("%s 綁 cluster-admin，LoadBalancer Service %v 沒有 loadBalancerSourceRanges，對外開放範圍只靠網段隔離", svc, md["name"]))
	}
	return infos
}

// Services 做服務層離線驗證：overlay 的 namespace、同主機撞埠、cluster-admin 服務的 LB 來源網段。
// 撞埠由 podman 軌的 setting.conf 算；cluster-admin 那條只是 info，不算錯誤。
func Services(root string) (errs []string, infos []string) {
	groups := loadGroups(root)
	used := map[portKey]map[string]bool{}
	svcs, err := repo.Services(root)
	if err != nil {
		return []string{err.Error()}, nil
	}
	for _, svc := range svcs {
		tracks, err := repo.Tracks(root, svc)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, track := range tracks {
			if e := checkNamespace(root, svc, track); e != "" {
				errs = append(errs, e)
			}
			out, err := render.Render(root, svc, track)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s/%s：渲染失敗：%s", svc, track, firstLine(err.Error())))
				continue
			}
			if track == "k8s" {
				docs, err := render.Docs(out)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s/%s：%v", svc, track, err))
					continue
				}
				infos = append(infos, clusterAdminInfos(svc, docs)...)
				continue
			}
			s, err := setting.Load(root, svc, track)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s/%s：%v", svc, track, err))
				continue
			}
			for _, p := range s.Ports {
				k := portKey{hostOf(track), p.HostPort, p.Protocol}
				if used[k] == nil {
					used[k] = map[string]bool{}
				}
				used[k][svc] = true
			}
		}
	}
	keys := make([]portKey, 0, len(used))
	for k := range used {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.host != b.host {
			return a.host < b.host
		}
		if a.port != b.port {
			return a.port < b.port
		}
		return a.proto < b.proto
	})
	for _, k := range keys {
		set := used[k]
		if len(set) < 2 {
			continue
		}
		allowed := false
		for _, g := range groups {
			if isSubset(set, g) {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		errs = append(errs, fmt.Sprintf("主機 %s 的 %d/%s 有 %s 同時使用，而且不在 config/port-groups.conf 的互斥清單裡",
			k.host, k.port, k.proto, strings.Join(names, "、")))
	}
	return errs, infos
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "（沒有訊息）"
	}
	return strings.SplitN(s, "\n", 2)[0]
}
