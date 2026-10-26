package confirm

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stdinFile(t *testing.T, s string) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func fakeTTY(t *testing.T) {
	t.Helper()
	old := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = old })
}

func TestConfirmRejectsWrongName(t *testing.T) {
	fakeTTY(t)
	var out bytes.Buffer
	err := Ask(stdinFile(t, "grafana\n"), &out, "filebrowser", "podman")
	if err == nil {
		t.Fatal("輸入錯的服務名應該回錯")
	}
	want := "刪除 filebrowser（podman），含資料卷、不可逆。輸入服務名確認："
	if !strings.Contains(out.String(), want) {
		t.Errorf("提示應含 %q，實際：%q", want, out.String())
	}
	if !strings.Contains(out.String(), "Info: 已取消（輸入的名稱與服務名不符）") {
		t.Errorf("應印取消訊息，實際：%q", out.String())
	}
}

func TestConfirmAcceptsRightName(t *testing.T) {
	fakeTTY(t)
	var out bytes.Buffer
	if err := Ask(stdinFile(t, "filebrowser\r\n"), &out, "filebrowser", "podman"); err != nil {
		t.Fatalf("輸入正確的服務名不應回錯：%v", err)
	}
}

func TestConfirmRejectsEmptyInput(t *testing.T) {
	fakeTTY(t)
	var out bytes.Buffer
	if err := Ask(stdinFile(t, ""), &out, "filebrowser", "podman"); err == nil {
		t.Fatal("沒有輸入應該回錯")
	}
}

func TestConfirmRequiresTTY(t *testing.T) {
	var out bytes.Buffer
	err := Ask(stdinFile(t, "filebrowser\n"), &out, "filebrowser", "podman")
	if err == nil || !strings.Contains(err.Error(), "終端機") {
		t.Fatalf("stdin 是一般檔案時應該回錯並提到終端機：%v", err)
	}
	if out.Len() != 0 {
		t.Errorf("不是終端機時不應印提示：%q", out.String())
	}
}

func TestPromptUsesGivenTextAndAnswer(t *testing.T) {
	fakeTTY(t)
	var out bytes.Buffer
	if err := Prompt(stdinFile(t, "gmm\n"), &out, "刪除多餘的項目。輸入服務名確認：", "gmm"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "刪除多餘的項目。輸入服務名確認：" {
		t.Errorf("應只印呼叫端給的提示：%q", out.String())
	}
}
