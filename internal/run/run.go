// Package run 是外部指令層：podman、systemctl、git 都經 Runner 呼叫，
// 測試用 Fake 換掉，不碰真的主機。
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner 執行一條外部指令。stdin 是 nil 時不接 stdin。
// 回傳 stdout；指令以非零結束時回 *ExitError。
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)
}

// ExitError 表示指令跑起來了，但以非零結束。Stderr 是指令的 stderr。
type ExitError struct {
	Cmd    string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s 結束碼 %d", e.Cmd, e.Code)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += "：" + s
	}
	return msg
}

// ExitCode 回傳 err 的結束碼。err 不是 *ExitError（例如指令不存在）時，ok 是 false。
func ExitCode(err error) (code int, ok bool) {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code, true
	}
	return 0, false
}

// Line 把指令與參數接成一行，供訊息與 Fake 比對用。
func Line(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// Exec 是真的 Runner，用 exec.CommandContext 執行。
type Exec struct{}

// Run 實作 Runner。
func (Exec) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var xe *exec.ExitError
	if errors.As(err, &xe) {
		return stdout.Bytes(), &ExitError{Cmd: Line(name, args), Code: xe.ExitCode(), Stderr: stderr.String()}
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s：%w", Line(name, args), err)
	}
	return stdout.Bytes(), nil
}
