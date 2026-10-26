package smoke

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flysangel/appctl/internal/podman"
	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
	"github.com/flysangel/appctl/internal/run"
	"github.com/flysangel/appctl/internal/setting"
)

const deployment = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %NAME%
spec:
  selector:
    matchLabels:
      app: %NAME%
  template:
    metadata:
      labels:
        app: %NAME%
    spec:
      containers:
      - name: c
        image: example.io/x:1
        ports:
        - containerPort: 80
`

type harness struct {
	root string
	fake *run.Fake
	out  *bytes.Buffer
	env  podman.Env
	opt  options
	srv  *httptest.Server
	hits atomic.Int32
	// ports 記下探測與登入用過的埠
	ports []int
	// handler 處理探測與登入的 HTTP 請求；測試可換掉
	handler http.HandlerFunc
}

// newHarness 造假 repo（每個服務一個 podman 或 k8s 目錄）、Fake、假 HTTP 伺服器。
func newHarness(t *testing.T, noPodman ...string) *harness {
	t.Helper()
	h := &harness{root: t.TempDir(), fake: &run.Fake{}, out: &bytes.Buffer{}}
	h.fake.On("podman pod exists", "", 1)
	h.fake.On("podman volume exists", "", 1)
	h.fake.On("podman secret exists", "", 1)
	h.fake.On("git", "", 128)
	state := t.TempDir()
	h.env = podman.Env{
		Runner:     h.fake,
		UnitDir:    filepath.Join(state, "quadlet"),
		SystemdDir: filepath.Join(state, "systemd"),
		StateDir:   filepath.Join(state, "state"),
		IsRoot:     func() bool { return true },
		Out:        h.out,
		Err:        h.out,
		Confirm:    func(*os.File, io.Writer, string, string) error { return nil },
	}
	h.handler = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		h.handler(w, r)
	}))
	t.Cleanup(h.srv.Close)
	h.opt = options{
		client: &http.Client{
			Timeout:       2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		url: func(port int, path string) string {
			h.ports = append(h.ports, port)
			return h.srv.URL + path
		},
		pollEvery: time.Millisecond,
		interval:  time.Millisecond,
		timeout:   func(string) time.Duration { return 100 * time.Millisecond },
		translate: func(_, svc, _ string) (*quadlet.Bundle, error) {
			d, err := render.Docs([]byte(strings.ReplaceAll(deployment, "%NAME%", svc)))
			if err != nil {
				return nil, err
			}
			s, err := setting.Parse(strings.NewReader("[ports]\nc = 18080:80\n"))
			if err != nil {
				return nil, err
			}
			return quadlet.Translate(svc, d, s)
		},
	}
	return h
}

// addService 建服務的軌目錄。repo 裡同名服務同軌有 setting.conf 時，一起複製過來：
// 探測與登入的主機埠從 setting.conf 查，測試斷言的埠就是 repo 現行的值。
func (h *harness) addService(t *testing.T, svc, track string) {
	t.Helper()
	dir := filepath.Join(h.root, "services", svc, track)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := repo.Root(".")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, "services", svc, track, "setting.conf"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	h.writeSetting(t, svc, track, string(b))
}

func (h *harness) writeSetting(t *testing.T, svc, track, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.root, "services", svc, track, "setting.conf"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) run(svcs []string, allowGuarded bool) error {
	return runWith(context.Background(), h.env, h.root, svcs, allowGuarded, h.opt)
}

func has(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func TestNoArgsFails(t *testing.T) {
	h := newHarness(t)
	err := h.run(nil, false)
	if err == nil || !strings.Contains(err.Error(), "用法") {
		t.Fatalf("沒有服務名應回用法錯誤：%v", err)
	}
	if len(h.fake.Calls) != 0 {
		t.Errorf("不應呼叫外部指令：%v", h.fake.Lines())
	}
}

func TestSmokeRequiresRoot(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	h.env.IsRoot = func() bool { return false }
	err := h.run([]string{"a"}, false)
	if err == nil || !strings.Contains(err.Error(), "sudo appctl") {
		t.Fatalf("非 root 應回錯並提到 sudo appctl：%v", err)
	}
	if len(h.fake.Calls) != 0 {
		t.Errorf("不應呼叫外部指令：%v", h.fake.Lines())
	}
}

func TestPrivilegedRefusedWithoutFlag(t *testing.T) {
	for _, svc := range guarded {
		h := newHarness(t)
		h.addService(t, svc, "podman")
		err := h.run([]string{svc}, false)
		if err == nil || !strings.Contains(h.out.String(), "--allow-guarded") {
			t.Errorf("%s 沒加旗標應失敗並提示 --allow-guarded：%v\n%s", svc, err, h.out)
		}
		if len(h.fake.Calls) != 0 {
			t.Errorf("%s 不應動主機：%v", svc, h.fake.Lines())
		}
	}
}

func TestPrivilegedRunsWithFlag(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "kadm", "podman")
	if err := h.run([]string{"kadm"}, true); err != nil {
		t.Fatalf("加旗標應跑：%v\n%s", err, h.out)
	}
	if !h.fake.Called("systemctl restart kadm-pod.service") {
		t.Errorf("應有 apply：%v", h.fake.Lines())
	}
}

func TestNoPodmanTrackIsSkippedWithReason(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "kadm", "k8s")
	h.addService(t, "a", "podman")
	if err := h.run([]string{"kadm", "a"}, false); err != nil {
		t.Fatalf("跳過不算失敗：%v\n%s", err, h.out)
	}
	if !strings.Contains(h.out.String(), "沒有 podman 軌") {
		t.Errorf("應說明跳過原因：%s", h.out)
	}
	for _, l := range h.fake.Lines() {
		if strings.Contains(l, "kadm") {
			t.Errorf("不應碰 kadm：%s", l)
		}
	}
}

func TestApplyFailureCleansAndContinues(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	h.addService(t, "b", "podman")
	h.fake.On("systemctl restart a-pod.service", "", 1)
	err := h.run([]string{"a", "b"}, false)
	if err == nil || !strings.Contains(err.Error(), "煙測未通過：a") {
		t.Fatalf("a 失敗應回錯並列出 a：%v", err)
	}
	lines := h.fake.Lines()
	stopA, startB := -1, -1
	for i, l := range lines {
		if stopA < 0 && strings.HasPrefix(l, "systemctl stop") && strings.Contains(l, "a-pod.service") {
			stopA = i
		}
		if startB < 0 && strings.HasPrefix(l, "systemctl restart b-pod.service") {
			startB = i
		}
	}
	if stopA < 0 || startB < 0 || stopA > startB {
		t.Errorf("a 失敗應先停掉 a，再跑 b（stop a=%d，restart b=%d）：%v", stopA, startB, lines)
	}
	if !h.fake.Called("podman pod exists b") {
		t.Errorf("a 失敗後應繼續跑 b：%v", lines)
	}
}

func TestNotRunningCleansPod(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	h.fake.On("systemctl is-active", "activating", 3)
	err := h.run([]string{"a"}, false)
	if err == nil {
		t.Fatalf("單元一直不是 active 應失敗\n%s", h.out)
	}
	if !strings.Contains(h.out.String(), "不是 active") {
		t.Errorf("應說明單元不是 active：%s", h.out)
	}
	if !h.fake.Called("systemctl stop") {
		t.Errorf("應停掉服務：%v", h.fake.Lines())
	}
}

func TestWaitsForEveryUnit(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	// pod 服務 active，容器單元不是：只看 pod 不夠
	h.fake.On("systemctl is-active --quiet a-pod.service", "", 0)
	h.fake.On("systemctl is-active --quiet a-c.service", "inactive", 3)
	if err := h.run([]string{"a"}, false); err == nil {
		t.Fatalf("容器單元不是 active 應失敗\n%s", h.out)
	}
	if !strings.Contains(h.out.String(), "a-c.service") {
		t.Errorf("應點名不是 active 的單元：%s", h.out)
	}
}

func TestProbeRetriesUntilReady(t *testing.T) {
	// gmm 的 HTTP 探測前兩次回 503，第三次回 200；不重試就會誤判失敗
	h := newHarness(t)
	h.addService(t, "gmm", "podman")
	h.handler = func(w http.ResponseWriter, r *http.Request) {
		if h.hits.Load() <= 2 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}
	if err := h.run([]string{"gmm"}, false); err != nil {
		t.Fatalf("重試後應通過：%v\n%s", err, h.out)
	}
	if h.hits.Load() < 3 {
		t.Errorf("探測應至少 3 次：%d", h.hits.Load())
	}
}

func TestAllPassRC0(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	h.addService(t, "b", "podman")
	if err := h.run([]string{"a", "b"}, false); err != nil {
		t.Fatalf("全部通過應回 nil：%v\n%s", err, h.out)
	}
	lines := h.fake.Lines()
	// 每個服務：兩次 apply（daemon-reload 兩次加 delete 一次），每次 apply 後查每個單元，最後 pod exists
	var a []string
	for _, l := range lines {
		if strings.Contains(l, " a-") || strings.HasPrefix(l, "podman pod exists a") {
			a = append(a, l)
		}
	}
	n := 0
	for _, l := range a {
		if l == "systemctl is-active --quiet a-pod.service" {
			n++
		}
	}
	if n < 2 {
		t.Errorf("兩次 apply 後都應查 a-pod.service：%v", a)
	}
	if got := a[len(a)-1]; got != "podman pod exists a" {
		t.Errorf("a 的最後一步應是 pod exists：%q", got)
	}
	if !has(a, "systemctl stop") {
		t.Errorf("delete 應停掉單元：%v", a)
	}
	if !strings.Contains(h.out.String(), "Success: 全部煙測通過") {
		t.Errorf("應印全部通過：%s", h.out)
	}
}

func TestSmokeStopsServiceOnFailure(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "grafana", "podman")
	h.handler = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"database":"failing"}`)) }
	err := h.run([]string{"grafana"}, false)
	if err == nil {
		t.Fatalf("探測失敗應回錯\n%s", h.out)
	}
	lines := h.fake.Lines()
	if !has(lines, "systemctl stop") {
		t.Errorf("探測失敗應 systemctl stop：%v", lines)
	}
	if has(lines, "podman volume rm") {
		t.Errorf("失敗時要留卷，不能 podman volume rm：%v", lines)
	}
}

func TestProbeTargets(t *testing.T) {
	cases := []struct {
		svc, path string
		port      int
		reply     func(w http.ResponseWriter)
		pass      bool
	}{
		{"gmm", "/guacamole/", 8080, func(w http.ResponseWriter) { w.WriteHeader(302) }, true},
		{"gmm", "/guacamole/", 8080, func(w http.ResponseWriter) { w.WriteHeader(500) }, false},
		{"grafana", "/api/health", 3000, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"database":"ok"}`)) }, true},
		{"grafana", "/api/health", 3000, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"database":"x"}`)) }, false},
		{"grafana", "/api/health", 3000, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`not json`)) }, false},
		{"quay", "/health/instance", 8080, func(w http.ResponseWriter) { w.WriteHeader(200) }, true},
		{"quay", "/health/instance", 8080, func(w http.ResponseWriter) { w.WriteHeader(204) }, false},
		{"ncb", "/", 80, func(w http.ResponseWriter) { w.WriteHeader(301) }, true},
		{"ncb", "/", 80, func(w http.ResponseWriter) { w.WriteHeader(200) }, false},
	}
	for _, c := range cases {
		h := newHarness(t)
		h.addService(t, c.svc, "podman")
		var gotPath atomic.Value
		h.handler = func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				gotPath.Store(r.URL.Path)
			}
			c.reply(w)
		}
		// 探測要通過；gmm 沒有 sync.secret.env 時略過登入
		err := h.run([]string{c.svc}, true)
		if (err == nil) != c.pass {
			t.Errorf("%s %v：通過=%v，期望 %v\n%s", c.svc, c.path, err == nil, c.pass, h.out)
		}
		for _, p := range h.ports {
			if p != c.port {
				t.Errorf("%s 探測埠 = %d，期望 %d", c.svc, p, c.port)
			}
		}
		if p, _ := gotPath.Load().(string); p != c.path {
			t.Errorf("%s 探測路徑 = %q，期望 %q", c.svc, p, c.path)
		}
	}
}

func TestNoProbeServiceOnlyChecksUnits(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	if err := h.run([]string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	if h.hits.Load() != 0 {
		t.Errorf("沒有探測的服務不應打 HTTP：%d", h.hits.Load())
	}
}

const fakePass = "fake-pass-value-example"

func writeGmmFiles(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "services", "gmm")
	conf := "# 註解\nGUACAMOLE_SERVER=http://x   # 不是這個\nGUACAMOLE_ADMIN=guacadmin                # 內建帳號\n"
	if err := os.WriteFile(filepath.Join(dir, "sync.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sync.secret.env"), []byte("GUACAMOLE_PASS="+fakePass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tokenHandler(t *testing.T, token string, seen *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(200)
			return
		}
		seen.Add(1)
		if r.URL.Path != "/guacamole/api/tokens" {
			t.Errorf("登入路徑 = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.PostForm.Get("username") != "guacadmin" || r.PostForm.Get("password") != fakePass {
			t.Errorf("登入表單不對（username=%q）", r.PostForm.Get("username"))
		}
		_, _ = w.Write([]byte(`{"authToken":"` + token + `"}`))
	}
}

func TestLoginGmm(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "gmm", "podman")
	writeGmmFiles(t, h.root)
	var seen atomic.Int32
	h.handler = tokenHandler(t, "tok-example", &seen)
	if err := h.run([]string{"gmm"}, false); err != nil {
		t.Fatalf("登入應成功：%v\n%s", err, h.out)
	}
	if len(h.ports) == 0 {
		t.Error("沒有記到任何埠")
	}
	for _, p := range h.ports {
		if p != 8080 {
			t.Errorf("gmm 探測與登入應用 8080：%d", p)
		}
	}
	if seen.Load() != 2 {
		t.Errorf("兩次 apply 後各登入一次：%d", seen.Load())
	}
	if !strings.Contains(h.out.String(), "gmm 登入成功") {
		t.Errorf("應印登入成功：%s", h.out)
	}
	if strings.Contains(h.out.String(), fakePass) || strings.Contains(strings.Join(h.fake.Lines(), "\n"), fakePass) {
		t.Errorf("密碼不能出現在輸出或指令列")
	}
}

func TestLoginGmmWithoutTokenFails(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "gmm", "podman")
	writeGmmFiles(t, h.root)
	h.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"message":"denied"}`))
			return
		}
		w.WriteHeader(200)
	}
	err := h.run([]string{"gmm"}, false)
	if err == nil || !strings.Contains(h.out.String(), "authToken") {
		t.Fatalf("取不到 authToken 應失敗：%v\n%s", err, h.out)
	}
	if strings.Contains(h.out.String(), fakePass) {
		t.Errorf("密碼不能出現在輸出")
	}
	if !h.fake.Called("systemctl stop") {
		t.Errorf("登入失敗也要停服務：%v", h.fake.Lines())
	}
}

func TestLoginGmmSkippedWithoutSecretFile(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "gmm", "podman")
	if err := h.run([]string{"gmm"}, false); err != nil {
		t.Fatalf("缺 sync.secret.env 應略過登入：%v\n%s", err, h.out)
	}
	if !strings.Contains(h.out.String(), "略過 gmm 登入") {
		t.Errorf("應說明略過：%s", h.out)
	}
}

func TestDeleteWithoutTTYRecordsFailure(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	h.addService(t, "b", "podman")
	h.env.Confirm = func(*os.File, io.Writer, string, string) error { return errors.New("需要 tty") }
	err := h.run([]string{"a", "b"}, false)
	if err == nil || !strings.Contains(err.Error(), "a b") {
		t.Fatalf("delete 確認失敗應記為失敗且繼續下一個：%v", err)
	}
}

func TestSmokeStopsServiceWhenDeleteFails(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	h.env.Confirm = func(*os.File, io.Writer, string, string) error { return errors.New("需要 tty") }
	if err := h.run([]string{"a"}, false); err == nil {
		t.Fatal("delete 失敗應回錯")
	}
	lines := h.fake.Lines()
	if !has(lines, "systemctl stop") {
		t.Errorf("delete 失敗後應停掉服務：%v", lines)
	}
	if has(lines, "podman volume rm") {
		t.Errorf("要留卷：%v", lines)
	}
}

func TestPodStillExistsAfterDeleteFails(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "a", "podman")
	h.fake.On("podman pod exists", "", 0)
	h.fake.On("podman pod ps", "", 0)
	err := h.run([]string{"a"}, false)
	if err == nil {
		t.Fatalf("delete 後 pod 還在應失敗\n%s", h.out)
	}
}

func TestDefaultOptionsEnv(t *testing.T) {
	t.Setenv("SMOKE_TIMEOUT", "7")
	t.Setenv("SMOKE_PROBE_INTERVAL", "0")
	o := defaultOptions()
	if o.timeout("a") != 7*time.Second || o.timeout("quay") != 7*time.Second {
		t.Errorf("SMOKE_TIMEOUT 應覆寫全部：%v %v", o.timeout("a"), o.timeout("quay"))
	}
	if o.interval != 0 {
		t.Errorf("SMOKE_PROBE_INTERVAL=0 應為 0：%v", o.interval)
	}
	t.Setenv("SMOKE_TIMEOUT", "")
	t.Setenv("SMOKE_PROBE_INTERVAL", "")
	o = defaultOptions()
	if o.timeout("a") != 120*time.Second || o.timeout("quay") != 600*time.Second || o.interval != 5*time.Second {
		t.Errorf("預設值不對：%v %v %v", o.timeout("a"), o.timeout("quay"), o.interval)
	}
}

func TestProbeUsesSettingHostPort(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "grafana", "podman")
	h.writeSetting(t, "grafana", "podman", "[ports]\ngrafana = 13000:3000\n")
	h.handler = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"database":"ok"}`)) }
	if err := h.run([]string{"grafana"}, false); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if len(h.ports) == 0 {
		t.Fatal("沒有記到任何埠")
	}
	for _, p := range h.ports {
		if p != 13000 {
			t.Errorf("探測埠 = %d，應從 setting.conf 查到 13000", p)
		}
	}
}

func TestProbeMissingPortMappingFails(t *testing.T) {
	h := newHarness(t)
	h.addService(t, "quay", "podman")
	h.writeSetting(t, "quay", "podman", "[ports]\nother = 18080:80\n")
	err := h.run([]string{"quay"}, false)
	if err == nil {
		t.Fatal("setting.conf 缺 quay-app:8080 應失敗")
	}
	if !strings.Contains(h.out.String(), "quay-app") {
		t.Errorf("錯誤訊息應指出缺哪個容器：%s", h.out)
	}
	if h.hits.Load() != 0 {
		t.Errorf("查不到埠時不應打 HTTP：%d", h.hits.Load())
	}
}
