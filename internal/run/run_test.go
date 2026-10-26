package run

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestExecMissingCommandIsErrNotFound(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), "appctl-no-such-command-xyz", nil, nil)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("要能用 errors.Is 認出 exec.ErrNotFound，得到：%v", err)
	}
}

func TestExecStartReadsStdoutAndStops(t *testing.T) {
	p, err := Exec{}.Start(context.Background(), "sh", []string{"-c", "echo ready; exec sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(p.Stdout()).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("應讀到 ready：%q %v", line, err)
	}
	done := make(chan struct{})
	go func() { p.Stop(); p.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop 應在指令被終止後返回")
	}
}

func TestExecStartWaitReportsStderr(t *testing.T) {
	p, err := Exec{}.Start(context.Background(), "sh", []string{"-c", "echo boom >&2; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, p.Stdout())
	err = p.Wait()
	if code, ok := ExitCode(err); !ok || code != 3 || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Wait 應回結束碼 3 並帶 stderr：%v", err)
	}
}
