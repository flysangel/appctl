package quay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "services", "quay", "secret")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "SERVER_HOSTNAME: quay.local\nSUPER_USERS:\n    - boss\n    - second\n"
	env := "DB_PASSWORD=fake-db\nQUAY_SUPERUSER_PASSWORD=fake-pass-value\nQUAY_SUPERUSER_EMAIL=boss@example.com\n"
	for name, body := range map[string]string{"config.yaml": cfg, "secret.env": env} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func opts(t *testing.T, url string, out *bytes.Buffer) Options {
	return Options{Root: fixtureRoot(t), BaseURL: url, Timeout: 2 * time.Second, Interval: time.Millisecond, Out: out}
}

func TestInitWaitsForHealth(t *testing.T) {
	var health atomic.Int32
	var posted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/instance":
			if health.Add(1) < 3 {
				w.WriteHeader(503)
				return
			}
		case "/api/v1/user/initialize":
			if health.Load() < 3 {
				t.Error("health 還沒過就送了 initialize")
			}
			posted.Store(true)
			w.WriteHeader(200)
			io.WriteString(w, `{"access_token":"tok"}`)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := Init(context.Background(), opts(t, srv.URL, &out)); err != nil {
		t.Fatal(err)
	}
	if health.Load() < 3 || !posted.Load() {
		t.Fatalf("health=%d posted=%v", health.Load(), posted.Load())
	}
}

func TestInitPostsSuperuser(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user/initialize" {
			if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("method=%s ct=%s", r.Method, r.Header.Get("Content-Type"))
			}
			json.NewDecoder(r.Body).Decode(&got)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := Init(context.Background(), opts(t, srv.URL, &out)); err != nil {
		t.Fatal(err)
	}
	if got["username"] != "boss" || got["password"] != "fake-pass-value" || got["email"] != "boss@example.com" {
		t.Fatalf("body 不對：username=%q email=%q", got["username"], got["email"])
	}
	if strings.Contains(out.String(), "fake-pass-value") {
		t.Fatal("輸出含密碼")
	}
}

func TestInitAlreadyInitialized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user/initialize" {
			w.WriteHeader(400)
			io.WriteString(w, `{"message":"Cannot initialize user in a non-empty database"}`)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := Init(context.Background(), opts(t, srv.URL, &out)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Info: quay 已經初始化") {
		t.Fatalf("輸出：%q", out.String())
	}
}

func TestInitOtherErrorHidesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user/initialize" {
			w.WriteHeader(400)
			io.WriteString(w, `{"message":"bad password fake-pass-value"}`)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := Init(context.Background(), opts(t, srv.URL, &out))
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "/api/v1/user/initialize") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "fake-pass-value") {
		t.Fatal("錯誤含密碼")
	}
}

func TestInitTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	o := opts(t, srv.URL, &bytes.Buffer{})
	o.Timeout = 50 * time.Millisecond
	err := Init(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "health") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitMissingPassword(t *testing.T) {
	o := opts(t, "http://127.0.0.1:1", &bytes.Buffer{})
	p := filepath.Join(o.Root, "services", "quay", "secret", "secret.env")
	os.WriteFile(p, []byte("DB_PASSWORD=fake-db\n"), 0o600)
	err := Init(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "QUAY_SUPERUSER_PASSWORD") {
		t.Fatalf("err=%v", err)
	}
}
