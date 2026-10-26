package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionInjected(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "appctl")
	build := exec.Command("go", "build", "-ldflags", "-X main.version=v1.2.3", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build 失敗：%v\n%s", err, out)
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "v1.2.3" {
		t.Fatalf("version 輸出不對：%v %q", err, out)
	}
	dev := filepath.Join(t.TempDir(), "appctl")
	if o, err := exec.Command("go", "build", "-o", dev, ".").CombinedOutput(); err != nil {
		t.Fatalf("build 失敗：%v\n%s", err, o)
	}
	out, _ = exec.Command(dev, "version").Output()
	if strings.TrimSpace(string(out)) != "dev" {
		t.Fatalf("預設應為 dev：%q", out)
	}
}
