package ncb

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flysangel/appctl/internal/podman"
	"github.com/flysangel/appctl/internal/run"
)

func setup(t *testing.T, serverName string) (root string, f *run.Fake, env podman.Env, out *bytes.Buffer) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "services", "ncb", "podman")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "# server_name commented.example;\nserver {\n    listen 80;\n    server_name " + serverName + ";\n}\nserver {\n    server_name other.example;\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "default.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	f = &run.Fake{}
	out = &bytes.Buffer{}
	env = podman.Env{Runner: f, IsRoot: func() bool { return true }, Out: out, Err: out}
	return
}

func TestCertReadsServerName(t *testing.T) {
	root, _, _, _ := setup(t, "ncb.local")
	got, err := ServerName(filepath.Join(root, "services", "ncb", "podman", "default.conf"))
	if err != nil || got != "ncb.local" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestACMERejectsLocal(t *testing.T) {
	root, f, env, _ := setup(t, "ncb.local")
	err := Cert(context.Background(), env, root, Options{ACME: true, Email: "a@example.com"})
	if err == nil || !strings.Contains(err.Error(), ".local") {
		t.Fatalf("應拒絕 .local：%v", err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("不應呼叫任何指令：%v", f.Lines())
	}
}

func TestACMEDefaultsToStaging(t *testing.T) {
	root, f, env, _ := setup(t, "ncb.example.com")
	f.On("podman volume inspect", t.TempDir(), 0)
	f.On("podman inspect", "true\n", 0)
	if err := Cert(context.Background(), env, root, Options{ACME: true, Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	want := "podman exec ncb-certbot certbot certonly --webroot -w /etc/letsencrypt/www -d ncb.example.com --cert-name ncb.example.com --email a@example.com --agree-tos --staging"
	if !f.Called(want) {
		t.Fatalf("缺 %q，實際：%v", want, f.Lines())
	}
	if !f.Called("podman exec ncb-nginx nginx -s reload") {
		t.Fatalf("缺 reload：%v", f.Lines())
	}
}

func TestACMEProductionAndEmail(t *testing.T) {
	root, f, env, _ := setup(t, "ncb.example.com")
	f.On("podman volume inspect", t.TempDir(), 0)
	if err := Cert(context.Background(), env, root, Options{ACME: true, Email: "a@example.com", Production: true}); err != nil {
		t.Fatal(err)
	}
	for _, l := range f.Lines() {
		if strings.Contains(l, "--staging") {
			t.Fatalf("不應有 --staging：%s", l)
		}
	}
	if err := Cert(context.Background(), env, root, Options{ACME: true}); err == nil {
		t.Fatal("缺 email 應回錯")
	}
}

func TestCertRequiresRoot(t *testing.T) {
	root, f, env, _ := setup(t, "ncb.local")
	env.IsRoot = func() bool { return false }
	err := Cert(context.Background(), env, root, Options{})
	if err == nil || !strings.Contains(err.Error(), "sudo appctl") {
		t.Fatalf("應要求 sudo：%v", err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("不應呼叫任何指令：%v", f.Lines())
	}
}

func TestSelfSignedWritesFiles(t *testing.T) {
	root, f, env, out := setup(t, "ncb.local")
	mount := t.TempDir()
	f.On("podman volume inspect ncb-certbot --format {{.Mountpoint}}", mount+"\n", 0)
	f.On("podman inspect", "true\n", 0)
	if err := Cert(context.Background(), env, root, Options{}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(mount, "live", "ncb.local")
	certPEM, err := os.ReadFile(filepath.Join(dir, "fullchain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "privkey.pem"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"fullchain.pem": 0o644, "privkey.pem": 0o600} {
		st, _ := os.Stat(filepath.Join(dir, name))
		if st.Mode().Perm() != want {
			t.Errorf("%s 權限 %v，要 %v", name, st.Mode().Perm(), want)
		}
	}
	blk, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "ncb.local" || len(cert.DNSNames) != 1 || cert.DNSNames[0] != "ncb.local" {
		t.Errorf("CN/SAN 不對：%v %v", cert.Subject, cert.DNSNames)
	}
	if days := cert.NotAfter.Sub(cert.NotBefore).Hours() / 24; days > 826 {
		t.Errorf("有效期 %.0f 天過長", days)
	}
	if !f.Called("podman exec ncb-nginx nginx -s reload") {
		t.Errorf("缺 reload：%v", f.Lines())
	}
	if bytes.Contains(out.Bytes(), []byte("PRIVATE KEY")) || strings.Contains(out.String(), strings.TrimSpace(string(keyPEM))[40:80]) {
		t.Error("輸出不應含私鑰")
	}
}

func TestSelfSignedRefusesOverwriteUnlessForce(t *testing.T) {
	root, f, env, _ := setup(t, "ncb.local")
	mount := t.TempDir()
	f.On("podman volume inspect", mount, 0)
	f.On("podman inspect", "true\n", 0)
	dir := filepath.Join(mount, "live", "ncb.local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "fullchain.pem")
	if err := os.WriteFile(existing, []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Cert(context.Background(), env, root, Options{}); err == nil {
		t.Fatal("已有檔案應回錯")
	}
	if b, _ := os.ReadFile(existing); string(b) != "real" {
		t.Fatal("不應覆蓋既有檔案")
	}
	if f.Called("podman exec") {
		t.Fatal("被拒絕時不應 reload")
	}
	if err := Cert(context.Background(), env, root, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(existing); string(b) == "real" {
		t.Fatal("--force 應覆蓋")
	}
}

func TestSelfSignedNginxNotRunningIsInfo(t *testing.T) {
	root, f, env, out := setup(t, "ncb.local")
	mount := t.TempDir()
	f.On("podman volume inspect", mount, 0)
	f.On("podman container exists ncb-nginx", "", 1)
	if err := Cert(context.Background(), env, root, Options{}); err != nil {
		t.Fatalf("nginx 沒起來應只提示：%v", err)
	}
	if !strings.Contains(out.String(), "Info:") || !strings.Contains(out.String(), "sudo appctl apply ncb --track podman") || !strings.Contains(out.String(), "sudo systemctl restart ncb-nginx.service") {
		t.Fatalf("缺提示：%s", out.String())
	}
	if f.Called("podman exec ncb-nginx") {
		t.Fatal("沒在跑不應 exec")
	}
	if _, err := os.Stat(filepath.Join(mount, "live", "ncb.local", "privkey.pem")); err != nil {
		t.Fatal(err)
	}
}

func TestNginxStoppedContainerIsInfo(t *testing.T) {
	root, f, env, out := setup(t, "ncb.local")
	f.On("podman volume inspect", t.TempDir(), 0)
	f.On("podman inspect", "false\n", 0)
	if err := Cert(context.Background(), env, root, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Info:") || f.Called("podman exec ncb-nginx") {
		t.Fatalf("停止中的容器應只提示：%s", out.String())
	}
}

func TestReloadFailureWhileRunningIsError(t *testing.T) {
	for _, acmeMode := range []bool{false, true} {
		root, f, env, _ := setup(t, "ncb.example.com")
		f.On("podman volume inspect", t.TempDir(), 0)
		f.On("podman inspect", "true\n", 0)
		f.OnError("podman exec ncb-nginx", &run.ExitError{Cmd: "podman exec", Code: 1, Stderr: "nginx: configuration file test failed"})
		err := Cert(context.Background(), env, root, Options{ACME: acmeMode, Email: "a@example.com"})
		if err == nil || !strings.Contains(err.Error(), "test failed") || !strings.Contains(err.Error(), "已放好") {
			t.Fatalf("acme=%v 在跑但 reload 失敗應回錯並帶 stderr：%v", acmeMode, err)
		}
	}
}

func TestExistsQueryErrorIsNotFalse(t *testing.T) {
	root, f, env, _ := setup(t, "ncb.local")
	f.On("podman volume inspect", t.TempDir(), 0)
	f.On("podman container exists ncb-nginx", "", 125)
	if err := Cert(context.Background(), env, root, Options{}); err == nil {
		t.Fatal("exists 回 125 應回錯")
	}
}

func TestServerNameParsing(t *testing.T) {
	cases := map[string]string{
		"server_name a.example b.example; # note": "a.example",
		"server_name   c.example;# x":             "c.example",
		"server_name d.example":                   "d.example",
	}
	for line, want := range cases {
		p := filepath.Join(t.TempDir(), "x.conf")
		if err := os.WriteFile(p, []byte("server {\n  "+line+"\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := ServerName(p); err != nil || got != want {
			t.Errorf("%q → %q, %v（要 %q）", line, got, err, want)
		}
	}
}

func TestForceReplacesAtomicallyWithoutTempLeftovers(t *testing.T) {
	root, f, env, _ := setup(t, "ncb.local")
	mount := t.TempDir()
	f.On("podman volume inspect", mount, 0)
	f.On("podman container exists ncb-nginx", "", 1)
	for i := 0; i < 2; i++ {
		if err := Cert(context.Background(), env, root, Options{Force: i == 1}); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(mount, "live", "ncb.local"))
	if len(ents) != 2 {
		t.Fatalf("目錄應只有兩個檔：%v", ents)
	}
}

// hookRunner 在 certbot 被呼叫的當下，呼叫 before 檢查卷裡的檔案狀態。
type hookRunner struct {
	*run.Fake
	before func()
}

func (h hookRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	if strings.HasPrefix(run.Line(name, args), "podman exec ncb-certbot") && h.before != nil {
		h.before()
	}
	return h.Fake.Run(ctx, name, args, stdin)
}

// acmeSetup 造一個卷，live/<domain>/ 放一份自簽憑證（renewal 不存在，不是 certbot 的 lineage）。
func acmeSetup(t *testing.T) (root, mount string, f *run.Fake, env podman.Env, out *bytes.Buffer) {
	t.Helper()
	root, f, env, out = setup(t, "ncb.example.com")
	mount = t.TempDir()
	f.On("podman volume inspect ncb-certbot --format {{.Mountpoint}}", mount+"\n", 0)
	f.On("podman inspect", "true\n", 0)
	live := filepath.Join(mount, "live", "ncb.example.com")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "fullchain.pem"), []byte("self-signed"), 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

func TestACMEMovesSelfSignedAsideBeforeCertbot(t *testing.T) {
	root, mount, f, env, out := acmeSetup(t)
	live := filepath.Join(mount, "live", "ncb.example.com")
	aside := live + ".selfsigned"
	// 舊的 .selfsigned 要被換掉
	if err := os.MkdirAll(aside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aside, "stale.pem"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	checked := false
	env.Runner = hookRunner{Fake: f, before: func() {
		checked = true
		if _, err := os.Stat(live); !os.IsNotExist(err) {
			t.Errorf("呼叫 certbot 時 %s 應已移開：%v", live, err)
		}
		if b, err := os.ReadFile(filepath.Join(aside, "fullchain.pem")); err != nil || string(b) != "self-signed" {
			t.Errorf("自簽憑證應移到 %s：%q %v", aside, b, err)
		}
		if _, err := os.Stat(filepath.Join(aside, "stale.pem")); !os.IsNotExist(err) {
			t.Errorf("舊的 .selfsigned 應被取代：%v", err)
		}
	}}
	if err := Cert(context.Background(), env, root, Options{ACME: true, Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatalf("沒有呼叫 certbot：%v", f.Lines())
	}
	if !has(f.Lines(), "--cert-name ncb.example.com") {
		t.Errorf("certbot 要帶 --cert-name：%v", f.Lines())
	}
	if !strings.Contains(out.String(), aside) {
		t.Errorf("應提示自簽憑證移到哪裡：%s", out.String())
	}
}

func TestACMEFailureRestoresSelfSigned(t *testing.T) {
	root, mount, f, env, _ := acmeSetup(t)
	f.OnError("podman exec ncb-certbot", &run.ExitError{Cmd: "certbot", Code: 1, Stderr: "challenge failed"})
	err := Cert(context.Background(), env, root, Options{ACME: true, Email: "a@example.com"})
	if err == nil || !strings.Contains(err.Error(), "challenge failed") {
		t.Fatalf("certbot 失敗應回錯：%v", err)
	}
	live := filepath.Join(mount, "live", "ncb.example.com")
	if b, err := os.ReadFile(filepath.Join(live, "fullchain.pem")); err != nil || string(b) != "self-signed" {
		t.Fatalf("certbot 失敗後自簽憑證應搬回 %s：%q %v", live, b, err)
	}
	if _, err := os.Stat(live + ".selfsigned"); !os.IsNotExist(err) {
		t.Errorf(".selfsigned 應已搬回：%v", err)
	}
}

func TestACMELeavesCertbotLineageAlone(t *testing.T) {
	root, mount, f, env, _ := acmeSetup(t)
	renewal := filepath.Join(mount, "renewal")
	if err := os.MkdirAll(renewal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(renewal, "ncb.example.com.conf"), []byte("# lineage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Cert(context.Background(), env, root, Options{ACME: true, Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(mount, "live", "ncb.example.com")
	if b, err := os.ReadFile(filepath.Join(live, "fullchain.pem")); err != nil || string(b) != "self-signed" {
		t.Errorf("certbot 的 lineage 不應移動：%q %v", b, err)
	}
	if _, err := os.Stat(live + ".selfsigned"); !os.IsNotExist(err) {
		t.Errorf("不應產生 .selfsigned：%v", err)
	}
	if !has(f.Lines(), "--cert-name ncb.example.com") {
		t.Errorf("certbot 要帶 --cert-name：%v", f.Lines())
	}
}

func has(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
