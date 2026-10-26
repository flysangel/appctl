// Package podman 用 Quadlet 部署、暫停與拆除 podman 軌的服務。
// 外部指令（podman、systemctl、git）都經 Env.Runner，測試換成 run.Fake。
package podman

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/flysangel/appctl/internal/confirm"
	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/run"
)

// recordFile 是 StateDir/<svc>/ 底下的部署紀錄（JSON，權限 0600）：
//
//	{
//	  "services": ["<svc>-<容器>.service", ..., "<svc>-pod.service"], // 停止順序：容器在前、pod 服務最後
//	  "units":    {"<單元檔絕對路徑>": "<已生效內容的 sha256>"},
//	  "configs":  {"<ConfigMap 名>": "<已生效內容的 sha256>"},
//	  "secrets":  {"<podman secret 名>": "<已生效值的 sha256>"},
//	  "volumes":  ["<卷名>", ...]                                     // 寫過的卷名只增不減，delete 才清
//	}
//
// apply 寫檔前先存一份「舊紀錄加這次 Bundle」的聯集，新出現的項目 hash 是空字串（還沒生效）；
// start/restart 全部成功後，才把 hash 換成這次的值。中途失敗時，下一次 apply 仍看得到「有變」而 restart，
// delete 也拿得到中途寫出的單元、secret 與卷。
const recordFile = "deployed.json"

type record struct {
	Services []string          `json:"services"`
	Units    map[string]string `json:"units"`
	Configs  map[string]string `json:"configs"`
	Secrets  map[string]string `json:"secrets"`
	Volumes  []string          `json:"volumes"`
}

func readRecord(env Env, svc string) (*record, error) {
	r := &record{}
	b, err := os.ReadFile(filepath.Join(env.svcState(svc), recordFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, r); err != nil {
			return nil, fmt.Errorf("部署紀錄壞了（%s）：%w", filepath.Join(env.svcState(svc), recordFile), err)
		}
	}
	if r.Units == nil {
		r.Units = map[string]string{}
	}
	if r.Configs == nil {
		r.Configs = map[string]string{}
	}
	if r.Secrets == nil {
		r.Secrets = map[string]string{}
	}
	return r, nil
}

func writeRecord(env Env, svc string, r *record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	dir := env.svcState(svc)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, recordFile), append(b, '\n'), 0o600)
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// union 回傳 a 加上 b 裡 a 沒有的項目，順序保留。
func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// stopOrder 把服務名排成停止順序：容器在前，pod 服務最後。
func stopOrder(svc string, names []string) []string {
	pod := svc + "-pod.service"
	var out []string
	for _, n := range names {
		if n != pod && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return append(out, pod)
}

// Env 是部署時的外部環境。測試把目錄指到 t.TempDir()，Runner 換成 run.Fake。
type Env struct {
	Runner run.Runner
	// UnitDir 放 .container、.volume（Quadlet 產生器讀這裡），預設 /etc/containers/systemd。
	UnitDir string
	// SystemdDir 放 <svc>-pod.service（一般 systemd 單元），預設 /etc/systemd/system。
	SystemdDir string
	// StateDir 取代 quadlet.ConfigRoot（/var/lib/appctl）：ConfigFiles 寫到這裡，狀態檔也放這裡。
	StateDir string
	// RepoRoot 是 repo 根目錄，用來比對機密檔是不是版控裡的 demo 值；空字串時不比對。
	RepoRoot string
	// Track 是軌名，只用在刪除確認的提示。
	Track  string
	IsRoot func() bool
	Stdin  *os.File
	Out    io.Writer
	Err    io.Writer
	// Confirm 是刪除前的確認；nil 時用 confirm.Ask。
	Confirm func(in *os.File, out io.Writer, svc, track string) error
}

// DefaultEnv 回傳正式環境用的 Env。
func DefaultEnv(repoRoot, track string) Env {
	return Env{
		Runner:     run.Exec{},
		UnitDir:    "/etc/containers/systemd",
		SystemdDir: "/etc/systemd/system",
		StateDir:   quadlet.ConfigRoot,
		RepoRoot:   repoRoot,
		Track:      track,
		IsRoot:     func() bool { return os.Geteuid() == 0 },
		Stdin:      os.Stdin,
		Out:        os.Stdout,
		Err:        os.Stderr,
		Confirm:    confirm.Ask,
	}
}

func (e Env) run(ctx context.Context, name string, args ...string) error {
	_, err := e.Runner.Run(ctx, name, args, nil)
	return err
}

// exists 跑一條 podman … exists 查詢：結束碼 0 回 true，1 回 false；
// 其他結束碼（例如 125 是 podman 自己出錯）與跑不起來都回錯，不能當成「不存在」。
func (e Env) exists(ctx context.Context, name string, args ...string) (bool, error) {
	err := e.run(ctx, name, args...)
	if err == nil {
		return true, nil
	}
	if code, isExit := run.ExitCode(err); isExit && code == 1 {
		return false, nil
	}
	return false, err
}

// active 回傳 systemd 服務是不是 active。is-active 對 inactive、failed、沒載入都回非零，一律算不在執行。
func (e Env) active(ctx context.Context, unit string) (bool, error) {
	err := e.run(ctx, "systemctl", "is-active", "--quiet", unit)
	if err == nil {
		return true, nil
	}
	if _, isExit := run.ExitCode(err); isExit {
		return false, nil
	}
	return false, err
}

// quiet 跑一條允許失敗的刪除指令：非零結束碼吸收，跑不起來才回錯。刪完要另外用 exists 確認。
func (e Env) quiet(ctx context.Context, name string, args ...string) error {
	err := e.run(ctx, name, args...)
	if _, isExit := run.ExitCode(err); isExit {
		return nil
	}
	return err
}

// stopUnits 停掉服務。systemctl stop 失敗時（例如單元沒載入），逐一查 is-active：
// 全部都不在執行就繼續，還有在執行的就回錯，呼叫端不能接著刪檔。
func stopUnits(ctx context.Context, env Env, units []string) error {
	if len(units) == 0 {
		return nil
	}
	err := env.run(ctx, "systemctl", append([]string{"stop"}, units...)...)
	if err == nil {
		return nil
	}
	if _, isExit := run.ExitCode(err); !isExit {
		return err
	}
	var still []string
	for _, u := range units {
		up, aerr := env.active(ctx, u)
		if aerr != nil {
			return aerr
		}
		if up {
			still = append(still, u)
		}
	}
	if len(still) > 0 {
		return fmt.Errorf("停不掉 %s，沒有刪任何東西：%w", strings.Join(still, "、"), err)
	}
	fmt.Fprintf(env.Out, "Info: systemctl stop 有錯，但單元都已不在執行，繼續：%v\n", err)
	return nil
}

func (e Env) svcState(svc string) string { return filepath.Join(e.StateDir, svc) }

func checkRoot(env Env, verb string) error {
	if env.IsRoot == nil || !env.IsRoot() {
		return fmt.Errorf("%s 要 root 權限，請改用 sudo appctl %s", verb, verb)
	}
	return nil
}

func (e Env) unitPath(f quadlet.File) (string, error) {
	switch f.Dir {
	case quadlet.DirQuadlet:
		return filepath.Join(e.UnitDir, f.Name), nil
	case quadlet.DirSystemd:
		return filepath.Join(e.SystemdDir, f.Name), nil
	}
	return "", fmt.Errorf("單元 %s 的 Dir 不認得：%q", f.Name, f.Dir)
}

// configPath 把翻譯器給的 /var/lib/appctl/... 換到 StateDir 底下。
func (e Env) configPath(name string) (string, error) {
	rel, ok := strings.CutPrefix(name, quadlet.ConfigRoot+"/")
	if !ok {
		return "", fmt.Errorf("ConfigFile 不在 %s 底下：%s", quadlet.ConfigRoot, name)
	}
	return filepath.Join(e.StateDir, filepath.FromSlash(rel)), nil
}

// serviceName 回傳單元檔對應的 systemd 服務名：Quadlet 把 X.container 產生成 X.service、
// X.volume 產生成 X-volume.service；.service 照原名。
func serviceName(file string) string {
	if n, ok := strings.CutSuffix(file, ".container"); ok {
		return n + ".service"
	}
	if n, ok := strings.CutSuffix(file, ".volume"); ok {
		return n + "-volume.service"
	}
	return file
}

func isContainer(f quadlet.File) bool { return strings.HasSuffix(f.Name, ".container") }
func isVolume(f quadlet.File) bool    { return strings.HasSuffix(f.Name, ".volume") }

// checkLegacy 擋下 podman kube play 留下的舊 pod：它們占著主機埠，新的 pod 起不來。
// 判斷規則：
//   - <svc>-pod 一定是舊的：kube play 把 Deployment、DaemonSet 起成 <svc>-pod，我們的 pod 叫 <svc>。
//   - <svc> 可能是 kube play（kind: Pod）留下的，也可能是我們的 <svc>-pod.service 建的。
//     <svc>-pod.service 是 active 時算我們的（允許再 apply），不是 active 時算舊的。
func checkLegacy(ctx context.Context, env Env, svc string) error {
	var legacy []string
	if yes, err := env.exists(ctx, "podman", "pod", "exists", svc+"-pod"); err != nil {
		return err
	} else if yes {
		legacy = append(legacy, svc+"-pod")
	}
	if yes, err := env.exists(ctx, "podman", "pod", "exists", svc); err != nil {
		return err
	} else if yes {
		active, err := env.active(ctx, svc+"-pod.service")
		if err != nil {
			return err
		}
		if !active {
			legacy = append(legacy, svc)
		}
	}
	if len(legacy) == 0 {
		return nil
	}
	var cmds []string
	for _, p := range legacy {
		cmds = append(cmds, "sudo podman pod rm -f "+p)
	}
	return fmt.Errorf("主機上有舊的 pod（podman kube play 留下的）：%s。它會占住主機埠，先清掉再 apply：\n  %s",
		strings.Join(legacy, "、"), strings.Join(cmds, "\n  "))
}

// writeIfChanged 在內容不同或檔案不存在時寫檔。
func writeIfChanged(p string, content []byte, perm fs.FileMode) error {
	old, err := os.ReadFile(p)
	if err == nil && bytes.Equal(old, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, content, perm)
}

// Apply 寫單元、ConfigFiles 與 secret，daemon-reload，再啟動服務。
// 「有變」是和部署紀錄裡已生效的 hash 比：自己的檔或它用到的卷、ConfigMap、secret 有變的單元 restart，
// 其餘 start（冪等）。上次部署有、這次 Bundle 沒有的單元先停掉再刪檔；卷不刪，留在紀錄裡給 delete。
func Apply(ctx context.Context, env Env, b *quadlet.Bundle) error {
	if err := checkRoot(env, "apply"); err != nil {
		return err
	}
	svc := b.Service
	if err := checkLegacy(ctx, env, svc); err != nil {
		return err
	}
	demoNotice(ctx, env, b)

	old, err := readRecord(env, svc)
	if err != nil {
		return err
	}
	paths := map[string]string{} // 單元檔名 → 主機路徑
	for _, u := range b.Units {
		p, err := env.unitPath(u)
		if err != nil {
			return err
		}
		paths[u.Name] = p
	}
	var services []string
	for _, u := range b.Units {
		if !isVolume(u) {
			services = append(services, serviceName(u.Name))
		}
	}
	services = stopOrder(svc, services)

	// 先存聯集紀錄：中途失敗時 delete 也找得到這次寫出的東西。
	pre := &record{
		Services: stopOrder(svc, union(old.Services, services)),
		Units:    maps.Clone(old.Units),
		Configs:  old.Configs,
		Secrets:  maps.Clone(old.Secrets),
		Volumes:  union(old.Volumes, b.Volumes),
	}
	for _, p := range paths {
		if !hasKey(pre.Units, p) {
			pre.Units[p] = ""
		}
	}
	for _, s := range b.Secrets {
		for key := range s.Data {
			if n := quadlet.PodmanSecretName(svc, s.Name, key); !hasKey(pre.Secrets, n) {
				pre.Secrets[n] = ""
			}
		}
	}
	if err := writeRecord(env, svc, pre); err != nil {
		return err
	}

	// 上次有、這次沒有的單元：停掉再刪檔。
	current := map[string]bool{}
	for _, p := range paths {
		current[p] = true
	}
	var stale []string
	for p := range old.Units {
		if !current[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	var staleSvcs []string
	for _, p := range stale {
		if !strings.HasSuffix(p, ".volume") {
			staleSvcs = append(staleSvcs, serviceName(filepath.Base(p)))
		}
	}
	if err := stopUnits(ctx, env, staleSvcs); err != nil {
		return err
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	// tokens 是「有變的依賴」在容器單元裡的寫法；單元內容含任一個就要 restart。
	var tokens []string
	changed := map[string]bool{}
	for _, u := range b.Units {
		p := paths[u.Name]
		if err := writeIfChanged(p, u.Content, 0o644); err != nil {
			return err
		}
		if old.Units[p] != sum(u.Content) {
			changed[u.Name] = true
			if isVolume(u) {
				tokens = append(tokens, "Volume="+quadlet.Escape(u.Name)+":")
			}
		}
	}
	configs, cmTokens, err := syncConfig(env, b, old.Configs)
	if err != nil {
		return err
	}
	tokens = append(tokens, cmTokens...)
	secrets, secTokens, err := syncSecrets(ctx, env, b, old.Secrets)
	if err != nil {
		return err
	}
	tokens = append(tokens, secTokens...)

	if err := env.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	podFile := svc + "-pod.service"
	var restart, start []string
	for _, u := range b.Units {
		if isVolume(u) {
			continue // 卷服務由容器單元的依賴拉起來
		}
		need := changed[u.Name]
		if isContainer(u) {
			// pod 重建時，pod 裡的容器會被一起移除，所以也要 restart。
			need = need || changed[podFile] || containsAny(u.Content, tokens)
		}
		if need {
			restart = append(restart, serviceName(u.Name))
		} else {
			start = append(start, serviceName(u.Name))
		}
	}
	if len(restart) > 0 {
		if err := env.run(ctx, "systemctl", append([]string{"restart"}, restart...)...); err != nil {
			return err
		}
	}
	if len(start) > 0 {
		if err := env.run(ctx, "systemctl", append([]string{"start"}, start...)...); err != nil {
			return err
		}
	}

	done := &record{Services: services, Units: map[string]string{}, Configs: configs, Secrets: secrets, Volumes: pre.Volumes}
	for _, u := range b.Units {
		done.Units[paths[u.Name]] = sum(u.Content)
	}
	return writeRecord(env, svc, done)
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}

func containsAny(content []byte, tokens []string) bool {
	for _, t := range tokens {
		if bytes.Contains(content, []byte(t)) {
			return true
		}
	}
	return false
}

// configSum 是一個 ConfigMap 全部鍵與內容的 hash。
func configSum(files map[string][]byte) string {
	h := sha256.New()
	for _, k := range sortedKeys(files) {
		fmt.Fprintf(h, "%d:%s%d:", len(k), k, len(files[k]))
		h.Write(files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// syncConfig 讓 StateDir/<svc>/configmap/ 和 Bundle.ConfigFiles 一致。
// 回傳這次每個 ConfigMap 的 hash，以及和已生效 hash 不同的 ConfigMap 在單元裡的寫法。
// 只重建磁碟上內容不同的目錄：容器 bind mount 的是目錄，整個刪掉重建會讓沒 restart 的容器看到已刪除的目錄。
func syncConfig(env Env, b *quadlet.Bundle, deployed map[string]string) (map[string]string, []string, error) {
	base := filepath.Join(env.svcState(b.Service), "configmap")
	want := map[string]map[string][]byte{} // ConfigMap 名 → 鍵 → 內容
	for _, f := range b.ConfigFiles {
		p, err := env.configPath(f.Name)
		if err != nil {
			return nil, nil, err
		}
		rel, err := filepath.Rel(base, p)
		cm, key, ok := strings.Cut(filepath.ToSlash(rel), "/")
		if err != nil || !ok || cm == ".." || strings.Contains(key, "/") {
			return nil, nil, fmt.Errorf("ConfigFile 路徑不是 %s/<ConfigMap>/<鍵>：%s", base, f.Name)
		}
		if want[cm] == nil {
			want[cm] = map[string][]byte{}
		}
		want[cm][key] = f.Content
	}
	have := map[string]map[string][]byte{}
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	for _, d := range entries {
		files, err := os.ReadDir(filepath.Join(base, d.Name()))
		if err != nil {
			return nil, nil, err
		}
		have[d.Name()] = map[string][]byte{}
		for _, f := range files {
			c, err := os.ReadFile(filepath.Join(base, d.Name(), f.Name()))
			if err != nil {
				return nil, nil, err
			}
			have[d.Name()][f.Name()] = c
		}
	}
	for cm := range have {
		if _, ok := want[cm]; !ok {
			if err := os.RemoveAll(filepath.Join(base, cm)); err != nil {
				return nil, nil, err
			}
		}
	}
	sums := map[string]string{}
	var tokens []string
	for _, cm := range sortedKeys(want) {
		sums[cm] = configSum(want[cm])
		if deployed[cm] != sums[cm] {
			host := quadlet.Escape(quadlet.ConfigDir(b.Service, cm))
			tokens = append(tokens, "Volume="+host+":", "Volume="+host+"/")
		}
		if sameFiles(have[cm], want[cm]) {
			continue
		}
		dir := filepath.Join(base, cm)
		if err := os.RemoveAll(dir); err != nil {
			return nil, nil, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
		for key, c := range want[cm] {
			if err := os.WriteFile(filepath.Join(dir, key), c, 0o644); err != nil {
				return nil, nil, err
			}
		}
	}
	return sums, tokens, nil
}

func sameFiles(a, b map[string][]byte) bool {
	if a == nil || len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if old, ok := a[k]; !ok || !bytes.Equal(old, v) {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// syncSecrets 每次都先 rm 再 create（podman 4.9.3 的 create --replace 在 secret 不存在時會失敗），
// 值走 stdin。值有沒有變，比對部署紀錄裡已生效的 hash，不從 podman 讀回機密。
// 紀錄裡有、這次沒有的 secret 一起刪。回傳這次的 hash 與有變的 secret 在單元裡的寫法。
func syncSecrets(ctx context.Context, env Env, b *quadlet.Bundle, deployed map[string]string) (map[string]string, []string, error) {
	now := map[string]string{}
	var tokens []string
	for _, s := range b.Secrets {
		for _, key := range sortedKeys(s.Data) {
			name := quadlet.PodmanSecretName(b.Service, s.Name, key)
			if err := env.quiet(ctx, "podman", "secret", "rm", name); err != nil {
				return nil, nil, err
			}
			if _, err := env.Runner.Run(ctx, "podman", []string{"secret", "create", name, "-"}, s.Data[key]); err != nil {
				return nil, nil, err
			}
			now[name] = sum(s.Data[key])
			if deployed[name] != now[name] {
				tokens = append(tokens, "Secret="+quadlet.Escape(name)+",")
			}
		}
	}
	for _, name := range sortedKeys(deployed) {
		if _, ok := now[name]; !ok {
			if err := env.quiet(ctx, "podman", "secret", "rm", name); err != nil {
				return nil, nil, err
			}
		}
	}
	return now, tokens, nil
}

// demoNotice 對這次渲染用到的機密檔（secret/kustomization.yaml 的 secretGenerator 列的 envs 與 files），
// 內容和 git HEAD 的版本相同時，提醒還是 demo 值。git 不能用或檔案沒進版控時安靜跳過。
func demoNotice(ctx context.Context, env Env, b *quadlet.Bundle) {
	if env.RepoRoot == "" || len(b.Secrets) == 0 {
		return
	}
	dir := path.Join("services", b.Service, "secret")
	k, err := os.ReadFile(filepath.Join(env.RepoRoot, filepath.FromSlash(dir), "kustomization.yaml"))
	if err != nil {
		return
	}
	var kz struct {
		SecretGenerator []struct {
			Envs  []string `yaml:"envs"`
			Files []string `yaml:"files"`
		} `yaml:"secretGenerator"`
	}
	if yaml.Unmarshal(k, &kz) != nil {
		return
	}
	var rels []string
	for _, g := range kz.SecretGenerator {
		rels = append(rels, g.Envs...)
		for _, f := range g.Files {
			if _, p, ok := strings.Cut(f, "="); ok {
				f = p // files 可以寫成 <鍵>=<路徑>
			}
			rels = append(rels, f)
		}
	}
	for _, r := range rels {
		rel := path.Join(dir, r)
		disk, err := os.ReadFile(filepath.Join(env.RepoRoot, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		head, err := env.Runner.Run(ctx, "git", []string{"-C", env.RepoRoot, "show", "HEAD:" + rel}, nil)
		if err != nil {
			continue
		}
		if bytes.Equal(disk, head) {
			fmt.Fprintf(env.Out, "Info: %s 還是版控裡的 demo 值，實際環境要改成真實值\n", rel)
		}
	}
}

// Stop 停掉容器單元與 pod 服務，不刪卷。沒有部署紀錄時只停 pod 服務：
// 容器單元 Requires= pod 服務，systemd 會一起停。
func Stop(ctx context.Context, env Env, svc string) error {
	if err := checkRoot(env, "stop"); err != nil {
		return err
	}
	rec, err := readRecord(env, svc)
	if err != nil {
		return err
	}
	return env.run(ctx, "systemctl", append([]string{"stop"}, stopOrder(svc, rec.Services)...)...)
}

// Delete 確認後，對這次 Bundle 與部署紀錄的聯集：停單元，刪單元檔與 secret，daemon-reload，
// 刪卷，並確認 secret 與卷都已不在。全部確認乾淨後才刪 StateDir/<svc>（含 ConfigFiles 與紀錄）；
// 中途回錯時紀錄留著，修好後再跑一次 delete 就能接著清。
func Delete(ctx context.Context, env Env, b *quadlet.Bundle) error {
	if err := checkRoot(env, "delete"); err != nil {
		return err
	}
	svc := b.Service
	ask := env.Confirm
	if ask == nil {
		ask = confirm.Ask
	}
	track := env.Track
	if track == "" {
		track = "podman"
	}
	if err := ask(env.Stdin, env.Out, svc, track); err != nil {
		return err
	}
	rec, err := readRecord(env, svc)
	if err != nil {
		return err
	}

	var services []string
	unitPaths := sortedKeys(rec.Units)
	for _, u := range b.Units {
		if isContainer(u) {
			services = append(services, serviceName(u.Name))
		}
		p, err := env.unitPath(u)
		if err != nil {
			return err
		}
		unitPaths = union(unitPaths, []string{p})
	}
	services = stopOrder(svc, union(services, rec.Services))
	if err := stopUnits(ctx, env, services); err != nil {
		return err
	}
	for _, p := range unitPaths {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	secrets := sortedKeys(rec.Secrets)
	for _, s := range b.Secrets {
		for _, key := range sortedKeys(s.Data) {
			secrets = union(secrets, []string{quadlet.PodmanSecretName(svc, s.Name, key)})
		}
	}
	for _, name := range secrets {
		if err := env.quiet(ctx, "podman", "secret", "rm", name); err != nil {
			return err
		}
	}
	if err := leftovers(ctx, env, "secret", secrets); err != nil {
		return err
	}
	if err := env.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	volumes := union(b.Volumes, rec.Volumes)
	for _, v := range volumes {
		if err := env.quiet(ctx, "podman", "volume", "rm", v); err != nil {
			return err
		}
	}
	if err := leftovers(ctx, env, "volume", volumes); err != nil {
		return err
	}
	return os.RemoveAll(env.svcState(svc))
}

// leftovers 用 podman <kind> exists 逐一確認已刪除，還在的列出名字與手動指令。
func leftovers(ctx context.Context, env Env, kind string, names []string) error {
	var left, cmds []string
	for _, n := range names {
		yes, err := env.exists(ctx, "podman", kind, "exists", n)
		if err != nil {
			return err
		}
		if yes {
			left = append(left, n)
			cmds = append(cmds, "sudo podman "+kind+" rm "+n)
		}
	}
	if len(left) == 0 {
		return nil
	}
	label := map[string]string{"volume": "卷", "secret": "secret"}[kind]
	return fmt.Errorf("這些%s還在：%s。手動刪除：\n  %s", label, strings.Join(left, "、"), strings.Join(cmds, "\n  "))
}
