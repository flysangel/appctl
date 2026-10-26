package check

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// secretRules 是有機密一致性規則的服務。
var secretRules = map[string]func(svcDir string) []string{"quay": checkQuay}

// Secret 比對同一個值在不同檔案之間是否一致。只回報鍵名，不含值。
// 目前只有 quay 有規則。
func Secret(root, svc string) []string {
	rule, ok := secretRules[svc]
	if !ok {
		names := make([]string, 0, len(secretRules))
		for n := range secretRules {
			names = append(names, n)
		}
		sort.Strings(names)
		return []string{fmt.Sprintf("%s 沒有一致性規則（有規則的服務：%s）", svc, strings.Join(names, "、"))}
	}
	return rule(filepath.Join(root, "services", svc))
}

// parseEnv 照 kustomize envs 的語意：跳過空行與 # 行，用第一個 = 切開。
func parseEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(k)] = v
	}
	return values, sc.Err()
}

func containerEnv(deployment, name string) (map[string]string, error) {
	b, err := os.ReadFile(deployment)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	spec, _ := doc["spec"].(map[string]any)
	tpl, _ := spec["template"].(map[string]any)
	pod, _ := tpl["spec"].(map[string]any)
	list, _ := pod["containers"].([]any)
	out := map[string]string{}
	for _, c := range list {
		m, _ := c.(map[string]any)
		if m["name"] != name {
			continue
		}
		envs, _ := m["env"].([]any)
		for _, e := range envs {
			em, _ := e.(map[string]any)
			n, _ := em["name"].(string)
			if v, ok := em["value"].(string); ok {
				out[n] = v
			}
		}
	}
	return out, nil
}

// opt 是可能不存在的字串值。
type opt struct {
	v  string
	ok bool
}

func str(v any) opt {
	s, ok := v.(string)
	return opt{s, ok}
}

func nested(conf map[string]any, key, field string) opt {
	m, _ := conf[key].(map[string]any)
	return str(m[field])
}

func checkQuay(svcDir string) []string {
	secret := filepath.Join(svcDir, "secret")
	envPath := filepath.Join(secret, "secret.env")
	confPath := filepath.Join(secret, "config.yaml")
	var errs []string
	for _, p := range []string{envPath, confPath} {
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			rel, _ := filepath.Rel(svcDir, p)
			errs = append(errs, "缺 "+rel)
		}
	}
	if len(errs) > 0 {
		return errs
	}
	env, err := parseEnv(envPath)
	if err != nil {
		return []string{"secret/secret.env 讀取失敗"}
	}
	b, err := os.ReadFile(confPath)
	if err != nil {
		return []string{"secret/config.yaml 讀取失敗"}
	}
	var conf map[string]any
	if err := yaml.Unmarshal(b, &conf); err != nil {
		return []string{"secret/config.yaml 不是合法的 YAML"}
	}
	pg, err := containerEnv(filepath.Join(svcDir, "base", "deployment.yaml"), "postgresql")
	if err != nil {
		return []string{"base/deployment.yaml 讀取失敗"}
	}
	envOpt := func(m map[string]string, k string) opt {
		v, ok := m[k]
		return opt{v, ok}
	}
	var user, pass, dbname opt
	if raw, ok := conf["DB_URI"].(string); ok {
		user, pass, dbname = splitDBURI(raw)
	}
	pairs := []struct {
		left  string
		lv    opt
		right string
		rv    opt
	}{
		{"secret.env 的 DB_PASSWORD", envOpt(env, "DB_PASSWORD"), "config.yaml 的 DB_URI 密碼", pass},
		{"secret.env 的 REDIS_PASSWORD", envOpt(env, "REDIS_PASSWORD"), "config.yaml 的 BUILDLOGS_REDIS.password", nested(conf, "BUILDLOGS_REDIS", "password")},
		{"secret.env 的 REDIS_PASSWORD", envOpt(env, "REDIS_PASSWORD"), "config.yaml 的 USER_EVENTS_REDIS.password", nested(conf, "USER_EVENTS_REDIS", "password")},
		{"deployment.yaml 的 POSTGRES_USER", envOpt(pg, "POSTGRES_USER"), "config.yaml 的 DB_URI 帳號", user},
		{"deployment.yaml 的 POSTGRES_DB", envOpt(pg, "POSTGRES_DB"), "config.yaml 的 DB_URI 庫名", dbname},
	}
	for _, p := range pairs {
		switch {
		case p.lv.ok && p.rv.ok && p.lv.v == p.rv.v:
		case p.lv.ok && p.rv.ok:
			errs = append(errs, fmt.Sprintf("%s 對 %s：不一致", p.left, p.right))
		default:
			errs = append(errs, fmt.Sprintf("%s 對 %s：有一邊沒有值", p.left, p.right))
		}
	}
	return errs
}

// splitDBURI 切開 scheme://user:pass@host/db。不用 net/url：密碼含保留字元時它會整條解析失敗，
// 而這種值正是要比對的對象。authority 以最後一個 @ 分隔 userinfo。
func splitDBURI(raw string) (user, pass, dbname opt) {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return
	}
	authority, path, _ := strings.Cut(rest, "/")
	dbname = opt{strings.SplitN(path, "?", 2)[0], true}
	i := strings.LastIndex(authority, "@")
	if i < 0 {
		return
	}
	u, p, hasPass := strings.Cut(authority[:i], ":")
	user = opt{u, true}
	pass = opt{p, hasPass}
	return
}

// Secrets 對每個有規則、且比對用的檔案都在的服務跑 Secret。
// 檔案不在的服務略過：機密真值檔不一定在每台機器上。
func Secrets(root string) []string {
	var errs []string
	names := make([]string, 0, len(secretRules))
	for n := range secretRules {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, svc := range names {
		secret := filepath.Join(root, "services", svc, "secret")
		have := true
		for _, f := range []string{"secret.env", "config.yaml"} {
			if fi, err := os.Stat(filepath.Join(secret, f)); err != nil || !fi.Mode().IsRegular() {
				have = false
			}
		}
		if !have {
			continue
		}
		for _, e := range Secret(root, svc) {
			errs = append(errs, svc+"："+e)
		}
	}
	return errs
}
