// Package setting 解析 services/<svc>/podman/setting.conf，
// 它放 k8s YAML 表達不了的 podman 專屬事實（目前只有主機埠）。
package setting

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PortMap 是一筆主機埠對應。Protocol 是 TCP 或 UDP。
type PortMap struct {
	ContainerName string
	HostPort      int
	ContainerPort int
	Protocol      string
}

// Setting 是 setting.conf 的內容。
type Setting struct {
	Ports []PortMap
}

// Parse 讀 INI 格式的 setting.conf。
func Parse(r io.Reader) (*Setting, error) {
	s := &Setting{}
	section := ""
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("第 %d 行：section 格式錯誤：%s", n, line)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section != "ports" {
				return nil, fmt.Errorf("第 %d 行：不支援的 section [%s]，只收 [ports]", n, section)
			}
			continue
		}
		if section != "ports" {
			return nil, fmt.Errorf("第 %d 行：項目必須放在 [ports] 底下", n)
		}
		name, val, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("第 %d 行：格式應為 <容器名> = <主機埠>:<容器埠>[/udp]：%s", n, line)
		}
		for _, item := range strings.Split(val, ",") {
			pm, err := parsePort(name, strings.TrimSpace(item))
			if err != nil {
				return nil, fmt.Errorf("第 %d 行：%w", n, err)
			}
			s.Ports = append(s.Ports, pm)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func parsePort(name, item string) (PortMap, error) {
	bad := fmt.Errorf("埠格式應為 <主機埠>:<容器埠>[/udp]：%q", item)
	proto := "TCP"
	if p, ok := strings.CutSuffix(item, "/udp"); ok {
		item, proto = p, "UDP"
	} else if p, ok := strings.CutSuffix(item, "/tcp"); ok {
		item = p
	}
	h, c, ok := strings.Cut(item, ":")
	if !ok {
		return PortMap{}, bad
	}
	hp, err1 := strconv.Atoi(strings.TrimSpace(h))
	cp, err2 := strconv.Atoi(strings.TrimSpace(c))
	if err1 != nil || err2 != nil || hp < 1 || hp > 65535 || cp < 1 || cp > 65535 {
		return PortMap{}, bad
	}
	return PortMap{ContainerName: name, HostPort: hp, ContainerPort: cp, Protocol: proto}, nil
}

// Load 讀 services/<svc>/<track>/setting.conf。
func Load(root, svc, track string) (*Setting, error) {
	path := filepath.Join(root, "services", svc, track, "setting.conf")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("讀不到 %s：%w", path, err)
	}
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s：%w", path, err)
	}
	return s, nil
}

// Validate 對照渲染後的文件，檢查 setting 的容器名與容器埠都存在。
func Validate(s *Setting, docs []map[string]any) error {
	if len(s.Ports) == 0 {
		return fmt.Errorf("setting.conf 的 [ports] 是空的")
	}
	declared := map[string]map[string]bool{} // 容器名 -> "埠/協定"
	for _, d := range docs {
		for _, c := range containers(d) {
			name, _ := c["name"].(string)
			if declared[name] == nil {
				declared[name] = map[string]bool{}
			}
			ports, _ := c["ports"].([]any)
			for _, p := range ports {
				pm, _ := p.(map[string]any)
				num, ok := toInt(pm["containerPort"])
				if !ok {
					continue
				}
				proto, _ := pm["protocol"].(string)
				if proto == "" {
					proto = "TCP"
				}
				declared[name][fmt.Sprintf("%d/%s", num, strings.ToUpper(proto))] = true
			}
		}
	}
	for _, p := range s.Ports {
		ports, ok := declared[p.ContainerName]
		if !ok {
			return fmt.Errorf("setting.conf 的容器 %q 不在 manifest 裡", p.ContainerName)
		}
		if !ports[fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol)] {
			return fmt.Errorf("setting.conf 的容器 %q 對應 %d/%s，但 manifest 沒有宣告這個 containerPort", p.ContainerName, p.ContainerPort, p.Protocol)
		}
	}
	return nil
}

// containers 取出 Pod、Deployment、DaemonSet、StatefulSet 的 containers[]。
func containers(doc map[string]any) []map[string]any {
	spec, _ := doc["spec"].(map[string]any)
	switch doc["kind"] {
	case "Pod":
	case "Deployment", "DaemonSet", "StatefulSet":
		tpl, _ := spec["template"].(map[string]any)
		spec, _ = tpl["spec"].(map[string]any)
	default:
		return nil
	}
	var out []map[string]any
	list, _ := spec["containers"].([]any)
	for _, c := range list {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
