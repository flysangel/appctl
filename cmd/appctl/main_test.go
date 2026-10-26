package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuayPodmanURLReadsSetting(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "services", "quay", "podman")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setting.conf"), []byte("[ports]\nquay-app = 18443:8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := quayPodmanURL(root, "podman")
	if err != nil || got != "http://127.0.0.1:18443" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setting.conf"), []byte("[ports]\nother = 1:2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := quayPodmanURL(root, "podman"); err == nil {
		t.Fatal("缺 quay-app 的 8080 對應應回錯")
	}
}
