// Package podman 用 Quadlet 部署、暫停與拆除 podman 軌的服務。
// 外部指令（podman、systemctl、git）都經 Env.Runner，測試換成 run.Fake。
package podman

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/flysangel/appctl/internal/confirm"
	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/run"
)

// StateDir/<svc>/ 底下的狀態檔。
const (
	unitsFile   = "units"          // 這次部署的 systemd 服務名，一行一個，照停止順序（容器在前、pod 服務最後）
	secretsFile = "secrets.sha256" // podman secret 名與值的 sha256，用來判斷 secret 有沒有變
)

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

// ok 跑一條「查詢」指令：結束碼 0 回 true，其他結束碼回 false；指令根本跑不起來時回錯。
func (e Env) ok(ctx context.Context, name string, args ...string) (bool, error) {
	err := e.run(ctx, name, args...)
	if err == nil {
		return true, nil
	}
	if _, isExit := run.ExitCode(err); isExit {
		return false, nil
	}
	return false, err
}

// quiet 跑一條允許失敗的指令（例如刪除不存在的 secret）：非零結束碼吸收，跑不起來才回錯。
func (e Env) quiet(ctx context.Context, name string, args ...string) error {
	_, err := e.ok(ctx, name, args...)
	return err
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
	if yes, err := env.ok(ctx, "podman", "pod", "exists", svc+"-pod"); err != nil {
		return err
	} else if yes {
		legacy = append(legacy, svc+"-pod")
	}
	if yes, err := env.ok(ctx, "podman", "pod", "exists", svc); err != nil {
		return err
	} else if yes {
		active, err := env.ok(ctx, "systemctl", "is-active", "--quiet", svc+"-pod.service")
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

// writeIfChanged 在內容不同或檔案不存在時寫檔，回傳有沒有寫。
func writeIfChanged(p string, content []byte, perm fs.FileMode) (bool, error) {
	old, err := os.ReadFile(p)
	if err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(p, content, perm)
}

// Apply 寫單元、ConfigFiles 與 secret，daemon-reload，再啟動服務。
// 自己的檔或它用到的卷、ConfigMap、secret 有變的單元 restart，其餘 start（冪等）。
func Apply(ctx context.Context, env Env, b *quadlet.Bundle) error {
	if err := checkRoot(env, "apply"); err != nil {
		return err
	}
	svc := b.Service
	if err := checkLegacy(ctx, env, svc); err != nil {
		return err
	}
	demoNotice(ctx, env, b)

	// tokens 是「有變的依賴」在容器單元裡的寫法；單元內容含任一個就要 restart。
	var tokens []string
	changed := map[string]bool{}
	for _, u := range b.Units {
		p, err := env.unitPath(u)
		if err != nil {
			return err
		}
		ch, err := writeIfChanged(p, u.Content, 0o644)
		if err != nil {
			return err
		}
		if ch {
			changed[u.Name] = true
			if isVolume(u) {
				tokens = append(tokens, "Volume="+quadlet.Escape(u.Name)+":")
			}
		}
	}
	cmTokens, err := syncConfig(env, b)
	if err != nil {
		return err
	}
	tokens = append(tokens, cmTokens...)
	secTokens, err := syncSecrets(ctx, env, b)
	if err != nil {
		return err
	}
	tokens = append(tokens, secTokens...)

	if err := env.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	podFile := svc + "-pod.service"
	var restart, start, stopOrder []string
	for _, u := range b.Units {
		if isVolume(u) {
			continue // 卷服務由容器單元的依賴拉起來
		}
		name := serviceName(u.Name)
		need := changed[u.Name]
		if isContainer(u) {
			stopOrder = append(stopOrder, name)
			// pod 重建時，pod 裡的容器會被一起移除，所以也要 restart。
			need = need || changed[podFile] || containsAny(u.Content, tokens)
		}
		if need {
			restart = append(restart, name)
		} else {
			start = append(start, name)
		}
	}
	stopOrder = append(stopOrder, podFile)
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
	_, err = writeIfChanged(filepath.Join(env.svcState(svc), unitsFile), []byte(strings.Join(stopOrder, "\n")+"\n"), 0o644)
	return err
}

func containsAny(content []byte, tokens []string) bool {
	for _, t := range tokens {
		if bytes.Contains(content, []byte(t)) {
			return true
		}
	}
	return false
}

// syncConfig 讓 StateDir/<svc>/configmap/ 和 Bundle.ConfigFiles 一致，回傳有變的 ConfigMap 在單元裡的寫法。
// 只動有變的 ConfigMap 目錄：容器 bind mount 的是目錄，整個刪掉重建會讓沒 restart 的容器看到已刪除的目錄。
func syncConfig(env Env, b *quadlet.Bundle) ([]string, error) {
	base := filepath.Join(env.svcState(b.Service), "configmap")
	want := map[string]map[string][]byte{} // ConfigMap 名 → 鍵 → 內容
	for _, f := range b.ConfigFiles {
		p, err := env.configPath(f.Name)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(base, p)
		cm, key, ok := strings.Cut(filepath.ToSlash(rel), "/")
		if err != nil || !ok || cm == ".." || strings.Contains(key, "/") {
			return nil, fmt.Errorf("ConfigFile 路徑不是 %s/<ConfigMap>/<鍵>：%s", base, f.Name)
		}
		if want[cm] == nil {
			want[cm] = map[string][]byte{}
		}
		want[cm][key] = f.Content
	}
	have := map[string]map[string][]byte{}
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, d := range entries {
		files, err := os.ReadDir(filepath.Join(base, d.Name()))
		if err != nil {
			return nil, err
		}
		have[d.Name()] = map[string][]byte{}
		for _, f := range files {
			c, err := os.ReadFile(filepath.Join(base, d.Name(), f.Name()))
			if err != nil {
				return nil, err
			}
			have[d.Name()][f.Name()] = c
		}
	}
	var tokens []string
	for cm := range have {
		if _, ok := want[cm]; !ok {
			if err := os.RemoveAll(filepath.Join(base, cm)); err != nil {
				return nil, err
			}
		}
	}
	for _, cm := range sortedKeys(want) {
		if sameFiles(have[cm], want[cm]) {
			continue
		}
		dir := filepath.Join(base, cm)
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		for key, c := range want[cm] {
			if err := os.WriteFile(filepath.Join(dir, key), c, 0o644); err != nil {
				return nil, err
			}
		}
		host := quadlet.Escape(quadlet.ConfigDir(b.Service, cm))
		tokens = append(tokens, "Volume="+host+":", "Volume="+host+"/")
	}
	return tokens, nil
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

// readSecretHashes 讀 secrets.sha256，回傳 podman secret 名 → sha256。檔案不在時回空 map。
func readSecretHashes(p string) (map[string]string, error) {
	out := map[string]string{}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if name, sum, ok := strings.Cut(line, " "); ok {
			out[name] = sum
		}
	}
	return out, nil
}

// syncSecrets 每次都先 rm 再 create（podman 4.9.3 的 create --replace 在 secret 不存在時會失敗），
// 值走 stdin。值有沒有變，比對 secrets.sha256，不從 podman 讀回機密。回傳有變的 secret 在單元裡的寫法。
func syncSecrets(ctx context.Context, env Env, b *quadlet.Bundle) ([]string, error) {
	hashPath := filepath.Join(env.svcState(b.Service), secretsFile)
	old, err := readSecretHashes(hashPath)
	if err != nil {
		return nil, err
	}
	now := map[string]string{}
	var tokens []string
	for _, s := range b.Secrets {
		for _, key := range sortedKeys(s.Data) {
			name := quadlet.PodmanSecretName(b.Service, s.Name, key)
			if err := env.quiet(ctx, "podman", "secret", "rm", name); err != nil {
				return nil, err
			}
			if _, err := env.Runner.Run(ctx, "podman", []string{"secret", "create", name, "-"}, s.Data[key]); err != nil {
				return nil, err
			}
			sum := sha256.Sum256(s.Data[key])
			now[name] = hex.EncodeToString(sum[:])
			if old[name] != now[name] {
				tokens = append(tokens, "Secret="+quadlet.Escape(name)+",")
			}
		}
	}
	for _, name := range sortedKeys(old) {
		if _, ok := now[name]; !ok {
			if err := env.quiet(ctx, "podman", "secret", "rm", name); err != nil {
				return nil, err
			}
		}
	}
	var buf strings.Builder
	for _, name := range sortedKeys(now) {
		fmt.Fprintf(&buf, "%s %s\n", name, now[name])
	}
	if err := os.MkdirAll(filepath.Dir(hashPath), 0o755); err != nil {
		return nil, err
	}
	return tokens, os.WriteFile(hashPath, []byte(buf.String()), 0o600)
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

// readUnits 讀上次 apply 記下的服務名（停止順序）。沒有紀錄時回 nil。
func readUnits(env Env, svc string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(env.svcState(svc), unitsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}

// Stop 停掉容器單元與 pod 服務，不刪卷。沒有部署紀錄時只停 pod 服務：
// 容器單元 Requires= pod 服務，systemd 會一起停。
func Stop(ctx context.Context, env Env, svc string) error {
	if err := checkRoot(env, "stop"); err != nil {
		return err
	}
	units, err := readUnits(env, svc)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		units = []string{svc + "-pod.service"}
	}
	return env.run(ctx, "systemctl", append([]string{"stop"}, units...)...)
}

// Delete 確認後停單元，刪單元、ConfigFiles、secret 與狀態檔，daemon-reload，再刪卷並確認卷已不在。
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

	// 停止順序：容器在前、pod 服務最後；再補上次部署紀錄裡有、這次 Bundle 沒有的單元。
	var units []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			units = append(units, n)
		}
	}
	for _, u := range b.Units {
		if isContainer(u) {
			add(serviceName(u.Name))
		}
	}
	add(svc + "-pod.service")
	recorded, err := readUnits(env, svc)
	if err != nil {
		return err
	}
	for _, n := range recorded {
		add(n)
	}
	// 單元沒載入（從沒部署過）時 systemctl stop 會失敗，不擋刪除。
	if err := env.run(ctx, "systemctl", append([]string{"stop"}, units...)...); err != nil {
		if _, isExit := run.ExitCode(err); !isExit {
			return err
		}
		fmt.Fprintln(env.Out, "Info: 停止單元時有錯，繼續刪除：", err)
	}

	for _, u := range b.Units {
		p, err := env.unitPath(u)
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	hashes, err := readSecretHashes(filepath.Join(env.svcState(svc), secretsFile))
	if err != nil {
		return err
	}
	secrets := map[string]bool{}
	for name := range hashes {
		secrets[name] = true
	}
	for _, s := range b.Secrets {
		for key := range s.Data {
			secrets[quadlet.PodmanSecretName(svc, s.Name, key)] = true
		}
	}
	for _, name := range sortedKeys(secrets) {
		if err := env.quiet(ctx, "podman", "secret", "rm", name); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(env.svcState(svc)); err != nil {
		return err
	}
	if err := env.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}

	for _, v := range b.Volumes {
		if err := env.quiet(ctx, "podman", "volume", "rm", v); err != nil {
			return err
		}
	}
	var left []string
	for _, v := range b.Volumes {
		yes, err := env.ok(ctx, "podman", "volume", "exists", v)
		if err != nil {
			return err
		}
		if yes {
			left = append(left, v)
		}
	}
	if len(left) > 0 {
		var cmds []string
		for _, v := range left {
			cmds = append(cmds, "sudo podman volume rm "+v)
		}
		return fmt.Errorf("這些卷還在：%s。手動刪除：\n  %s", strings.Join(left, "、"), strings.Join(cmds, "\n  "))
	}
	return nil
}
