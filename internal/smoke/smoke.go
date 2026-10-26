// Package smoke 是 podman 軌的煙測：apply、等全部單元 active、HTTP 探測、再 apply、再探測、delete。
// 逐個服務跑；任一步失敗就 podman.Stop 該服務（留卷）並記失敗，繼續下一個。
package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flysangel/appctl/internal/podman"
	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
	"github.com/flysangel/appctl/internal/run"
	"github.com/flysangel/appctl/internal/setting"
)

// guarded 的 delete 會刪掉重要資料：ncb 的卷是憑證，kadm 與 xadm-* 部署前要先確認網段隔離。
var guarded = []string{"ncb", "kadm", "xadm-lxqt", "xadm-xfce"}

// probeSpec 是一個服務的 HTTP 探測。
type probeSpec struct {
	port int
	path string
	// ok 判斷一次回應算不算通過。body 只有 wantBody 為 true 時才讀。
	ok       func(code int, body []byte) bool
	wantBody bool
}

var probes = map[string]probeSpec{
	"gmm": {port: 8080, path: "/guacamole/", ok: func(c int, _ []byte) bool { return c >= 200 && c < 400 }},
	"grafana": {port: 3000, path: "/api/health", wantBody: true, ok: func(_ int, b []byte) bool {
		var h struct {
			Database string `json:"database"`
		}
		return json.Unmarshal(b, &h) == nil && h.Database == "ok"
	}},
	"quay": {port: 8080, path: "/health/instance", ok: func(c int, _ []byte) bool { return c == 200 }},
	"ncb":  {port: 80, path: "/", ok: func(c int, _ []byte) bool { return c == 301 }},
}

// options 是可以在測試換掉的部分。
type options struct {
	client    *http.Client
	url       func(port int, path string) string // 正式環境是 http://127.0.0.1:<port><path>
	pollEvery time.Duration                      // 輪詢單元狀態的間隔
	interval  time.Duration                      // HTTP 探測的間隔
	timeout   func(svc string) time.Duration
	translate func(root, svc, track string) (*quadlet.Bundle, error)
}

func defaultOptions() options {
	return options{
		client: &http.Client{
			Timeout: 10 * time.Second,
			// 不跟轉址：ncb 的探測要看到 301 本身
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		url:       func(port int, path string) string { return "http://127.0.0.1:" + strconv.Itoa(port) + path },
		pollEvery: 2 * time.Second,
		interval:  envSeconds("SMOKE_PROBE_INTERVAL", 5*time.Second),
		timeout: func(svc string) time.Duration {
			// quay 第一次啟動要跑 migration，等比較久
			d := 120 * time.Second
			if svc == "quay" {
				d = 600 * time.Second
			}
			return envSeconds("SMOKE_TIMEOUT", d)
		},
		translate: translate,
	}
}

func envSeconds(name string, def time.Duration) time.Duration {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil || n < 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

func translate(root, svc, track string) (*quadlet.Bundle, error) {
	out, err := render.Render(root, svc, track)
	if err != nil {
		return nil, err
	}
	docs, err := render.Docs(out)
	if err != nil {
		return nil, err
	}
	s, err := setting.Load(root, svc, track)
	if err != nil {
		return nil, err
	}
	return quadlet.Translate(svc, docs, s)
}

// Run 對每個服務跑煙測。要 root。全部通過回 nil；有服務失敗時，跑完全部才回錯。
func Run(ctx context.Context, env podman.Env, root string, svcs []string, allowGuarded bool) error {
	return runWith(ctx, env, root, svcs, allowGuarded, defaultOptions())
}

type smoker struct {
	env  podman.Env
	root string
	opt  options
}

func runWith(ctx context.Context, env podman.Env, root string, svcs []string, allowGuarded bool, opt options) error {
	if len(svcs) == 0 {
		return fmt.Errorf("用法：appctl smoke [--allow-guarded] <svc>...（%s 要加 --allow-guarded 才跑）", strings.Join(guarded, " "))
	}
	if env.IsRoot == nil || !env.IsRoot() {
		return fmt.Errorf("smoke 要 root 權限，請改用 sudo appctl smoke")
	}
	s := &smoker{env: env, root: root, opt: opt}
	var failed []string
	for _, svc := range svcs {
		tracks, err := repo.Tracks(root, svc)
		if err != nil {
			fmt.Fprintf(env.Err, "Error: %v\n", err)
			failed = append(failed, svc)
			continue
		}
		track := podmanTrack(tracks)
		if track == "" {
			fmt.Fprintf(env.Out, "Info: %s 沒有 podman 軌，跳過\n", svc)
			continue
		}
		if slices.Contains(guarded, svc) && !allowGuarded {
			fmt.Fprintf(env.Err, "Error: %s 預設不跑：delete 會刪掉它的卷，要跑就加 --allow-guarded\n", svc)
			failed = append(failed, svc)
			continue
		}
		if err := s.one(ctx, svc, track); err != nil {
			fmt.Fprintf(env.Err, "Error: %s 煙測失敗：%v\n", svc, err)
			failed = append(failed, svc)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("煙測未通過：%s", strings.Join(failed, " "))
	}
	fmt.Fprintln(env.Out, "Success: 全部煙測通過")
	return nil
}

func podmanTrack(tracks []string) string {
	if slices.Contains(tracks, "podman") {
		return "podman"
	}
	for _, t := range tracks {
		if strings.HasPrefix(t, "podman") {
			return t
		}
	}
	return ""
}

func (s *smoker) one(ctx context.Context, svc, track string) error {
	b, err := s.opt.translate(s.root, svc, track)
	if err != nil {
		return err
	}
	env := s.env
	env.Track = track
	timeout := s.opt.timeout(svc)
	for round := 1; round <= 2; round++ {
		fmt.Fprintf(env.Out, "Info: %s 第 %d 次 apply\n", svc, round)
		err := podman.Apply(ctx, env, b)
		if err == nil {
			err = s.check(ctx, svc, b, timeout)
		}
		if err != nil {
			// 清掉服務，免得占著 hostPort 讓下一個服務連鎖失敗；卷留著
			fmt.Fprintf(env.Err, "Error: %s 第 %d 次檢查失敗，停掉服務（卷留著）\n", svc, round)
			if serr := podman.Stop(ctx, env, svc); serr != nil {
				fmt.Fprintf(env.Err, "Error: 停止 %s 也失敗：%v\n", svc, serr)
			}
			return err
		}
	}
	if err := podman.Delete(ctx, env, b); err != nil {
		return err
	}
	// pod 在 delete 後不該還在
	_, err = env.Runner.Run(ctx, "podman", []string{"pod", "exists", svc}, nil)
	if err == nil {
		return fmt.Errorf("pod %s 刪除後還在", svc)
	}
	if code, ok := run.ExitCode(err); !ok || code != 1 {
		return err
	}
	fmt.Fprintf(env.Out, "Success: %s 煙測通過\n", svc)
	return nil
}

func (s *smoker) check(ctx context.Context, svc string, b *quadlet.Bundle, timeout time.Duration) error {
	if err := s.waitActive(ctx, svc, b, timeout); err != nil {
		return err
	}
	if err := s.probe(ctx, svc, timeout); err != nil {
		return err
	}
	return s.login(ctx, svc)
}

// activeUnits 回傳 Bundle 的 pod 服務與每個容器單元對應的 systemd 服務名。.volume 不算：它是一次性的。
func activeUnits(b *quadlet.Bundle) []string {
	var out []string
	for _, u := range b.Units {
		switch {
		case strings.HasSuffix(u.Name, ".container"):
			out = append(out, strings.TrimSuffix(u.Name, ".container")+".service")
		case strings.HasSuffix(u.Name, ".service"):
			out = append(out, u.Name)
		}
	}
	return out
}

// waitActive 每 pollEvery 查一次，直到全部單元 active。
func (s *smoker) waitActive(ctx context.Context, svc string, b *quadlet.Bundle, timeout time.Duration) error {
	units := activeUnits(b)
	deadline := time.Now().Add(timeout)
	var inactive []string
	for {
		inactive = inactive[:0]
		for _, u := range units {
			_, err := s.env.Runner.Run(ctx, "systemctl", []string{"is-active", "--quiet", u}, nil)
			if err == nil {
				continue
			}
			if _, isExit := run.ExitCode(err); !isExit {
				return err
			}
			inactive = append(inactive, u)
		}
		if len(inactive) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s 等了 %s 還有單元不是 active：%s", svc, timeout, strings.Join(inactive, "、"))
		}
		if err := sleep(ctx, s.opt.pollEvery); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// probeOnce 探一次。連不上或不符合都回 false。
func (s *smoker) probeOnce(ctx context.Context, p probeSpec) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opt.url(p.port, p.path), nil)
	if err != nil {
		return false
	}
	resp, err := s.opt.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body []byte
	if p.wantBody {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	return p.ok(resp.StatusCode, body)
}

// probe 重試到通過或逾時：單元剛 active 時服務多半還沒聽埠。
func (s *smoker) probe(ctx context.Context, svc string, timeout time.Duration) error {
	p, has := probes[svc]
	if !has {
		fmt.Fprintf(s.env.Out, "Info: %s 沒有 HTTP 探測，只確認單元都是 active\n", svc)
		return nil
	}
	deadline := time.Now().Add(timeout)
	for !s.probeOnce(ctx, p) {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s 的 HTTP 探測等了 %s 還沒通過（GET :%d%s）", svc, timeout, p.port, p.path)
		}
		if err := sleep(ctx, s.opt.interval); err != nil {
			return err
		}
	}
	fmt.Fprintf(s.env.Out, "Info: %s HTTP 探測通過\n", svc)
	return nil
}

var adminLine = regexp.MustCompile(`^GUACAMOLE_ADMIN=(\S*)`)

// login 只有 gmm 要：用 sync.conf 的管理帳號與 sync.secret.env 的密碼取 authToken。密碼只放在 POST body。
func (s *smoker) login(ctx context.Context, svc string) error {
	if svc != "gmm" {
		return nil
	}
	dir := filepath.Join(s.root, "services", "gmm")
	secretPath := filepath.Join(dir, "sync.secret.env")
	secret, err := os.ReadFile(secretPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(s.env.Out, "Info: 缺 %s，略過 gmm 登入\n", secretPath)
		return nil
	}
	if err != nil {
		return err
	}
	conf, err := os.ReadFile(filepath.Join(dir, "sync.conf"))
	if err != nil {
		return err
	}
	var admin, pass string
	for _, l := range strings.Split(string(conf), "\n") {
		if m := adminLine.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			admin = m[1]
		}
	}
	for _, l := range strings.Split(string(secret), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(l, "\r"), "GUACAMOLE_PASS="); ok {
			pass = v
		}
	}
	form := url.Values{"username": {admin}, "password": {pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.opt.url(8080, "/guacamole/api/tokens"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.opt.client.Do(req)
	if err != nil {
		return fmt.Errorf("gmm 登入連不上：%w", scrub(err, pass))
	}
	defer resp.Body.Close()
	var tok struct {
		AuthToken string `json:"authToken"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(body, &tok) != nil || tok.AuthToken == "" {
		return fmt.Errorf("gmm 登入取不到 authToken（HTTP %d）", resp.StatusCode)
	}
	fmt.Fprintln(s.env.Out, "Info: gmm 登入成功")
	return nil
}

// scrub 確保錯誤文字不含密碼（url 錯誤只帶網址，這裡是保險）。
func scrub(err error, pass string) error {
	if pass != "" && strings.Contains(err.Error(), pass) {
		return errors.New("連線錯誤（細節含機密，已隱藏）")
	}
	return err
}
