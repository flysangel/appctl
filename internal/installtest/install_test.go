// Package installtest 以本機假的下載伺服器，測試根目錄的 install.sh。
package installtest

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const fakeBinary = "#!/bin/sh\necho fake-appctl\n"

type fixture struct {
	srv  *httptest.Server
	dest string
	// sumName 是 .sha256 內寫的檔名，預設 appctl-linux-amd64
	sumName string

	mu   sync.Mutex
	hits []string
}

// newFixture 起假伺服器：sum 為空時回正確的 sha256，否則回指定值。
func newFixture(t *testing.T, sum string) *fixture {
	t.Helper()
	if sum == "" {
		h := sha256.Sum256([]byte(fakeBinary))
		sum = hex.EncodeToString(h[:])
	}
	f := &fixture{dest: t.TempDir(), sumName: "appctl-linux-amd64"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.URL.Path)
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/appctl-linux-amd64"):
			_, _ = w.Write([]byte(fakeBinary))
		case strings.HasSuffix(r.URL.Path, "/appctl-linux-amd64.sha256"):
			_, _ = w.Write([]byte(sum + "  " + f.sumName + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(),
		"APPCTL_DOWNLOAD_BASE="+f.srv.URL,
		"APPCTL_INSTALL_DIR="+f.dest,
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallLatest(t *testing.T) {
	f := newFixture(t, "")
	out, err := f.run(t)
	if err != nil {
		t.Fatalf("install 失敗：%v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(f.dest, "appctl"))
	if err != nil || string(got) != fakeBinary {
		t.Fatalf("目的檔內容不對：%v %q", err, got)
	}
	st, _ := os.Stat(filepath.Join(f.dest, "appctl"))
	if st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("目的檔沒有執行權限：%v", st.Mode())
	}
	if !strings.Contains(strings.Join(f.hits, " "), "/latest/download/appctl-linux-amd64") {
		t.Fatalf("沒走 latest 路徑：%v", f.hits)
	}
}

func TestInstallVersion(t *testing.T) {
	f := newFixture(t, "")
	for _, v := range []string{"v1.2.3", "v0.1.0-rc.1"} {
		f.hits = nil
		if out, err := f.run(t, v); err != nil {
			t.Fatalf("%s 失敗：%v\n%s", v, err, out)
		}
		want := "/download/" + v + "/appctl-linux-amd64"
		if !strings.Contains(strings.Join(f.hits, " "), want) {
			t.Fatalf("%s 沒走版本路徑：%v", v, f.hits)
		}
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	f := newFixture(t, strings.Repeat("0", 64))
	dest := filepath.Join(f.dest, "appctl")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := f.run(t)
	if err == nil {
		t.Fatalf("sha256 不對卻成功：\n%s", out)
	}
	if !strings.Contains(out, "Error:") {
		t.Fatalf("缺 Error: 訊息：%s", out)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "old" {
		t.Fatalf("目的檔被改動：%q", got)
	}
}

func TestInstallChecksumMismatchNoDest(t *testing.T) {
	f := newFixture(t, strings.Repeat("0", 64))
	if _, err := f.run(t); err == nil {
		t.Fatal("sha256 不對卻成功")
	}
	if _, err := os.Stat(filepath.Join(f.dest, "appctl")); err == nil {
		t.Fatal("不該寫入目的檔")
	}
}

func TestInstallBadVersion(t *testing.T) {
	f := newFixture(t, "")
	for _, v := range []string{"1.2.3", "v1.2", "latest", "v1.2.3/../x", "v1.2.3;id"} {
		out, err := f.run(t, v)
		if err == nil || !strings.Contains(out, "Error:") {
			t.Fatalf("版本 %q 應被拒：%v\n%s", v, err, out)
		}
	}
	if len(f.hits) != 0 {
		t.Fatalf("壞版本不該發出請求：%v", f.hits)
	}
}

func TestInstallDownloadFails(t *testing.T) {
	f := newFixture(t, "")
	f.srv.Close()
	if _, err := f.run(t); err == nil {
		t.Fatal("伺服器不通卻成功")
	}
	if _, err := os.Stat(filepath.Join(f.dest, "appctl")); err == nil {
		t.Fatal("不該寫入目的檔")
	}
}

func TestInstallSumNamesOtherFile(t *testing.T) {
	f := newFixture(t, "")
	f.sumName = "other-file"
	out, err := f.run(t)
	if err == nil || !strings.Contains(out, "Error:") {
		t.Fatalf(".sha256 指向別的檔卻成功：%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(f.dest, "appctl")); err == nil {
		t.Fatal("不該寫入目的檔")
	}
}
