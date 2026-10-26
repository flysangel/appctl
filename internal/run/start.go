package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Starter 啟動一條會一直跑的外部指令，例如 kubectl port-forward。
// Runner 要等指令結束才回傳，所以長時間的指令另走這個介面。
type Starter interface {
	Start(ctx context.Context, name string, args []string) (Process, error)
}

// Process 是 Starter 啟動的指令。
type Process interface {
	// Stdout 是指令的 stdout。呼叫端要一直讀到 EOF，否則指令寫滿管線後會卡住。
	Stdout() io.Reader
	// Wait 等指令結束。以非零結束時回 *ExitError，Stderr 帶指令的 stderr。
	Wait() error
	// Stop 終止指令並等它結束。可以重複呼叫。
	Stop()
}

// Start 實作 Starter。ctx 取消時指令也會被終止。
func (Exec) Start(ctx context.Context, name string, args []string) (Process, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, name, args...)
	pr, pw := io.Pipe()
	p := &execProcess{stdout: pr, cancel: cancel, done: make(chan struct{})}
	cmd.Stdout = pw
	cmd.Stderr = &p.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%s：%w", Line(name, args), err)
	}
	go func() {
		err := cmd.Wait()
		pw.Close()
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			err = &ExitError{Cmd: Line(name, args), Code: xe.ExitCode(), Stderr: p.stderr.String()}
		} else if err != nil {
			err = fmt.Errorf("%s：%w", Line(name, args), err)
		}
		p.err = err
		close(p.done)
	}()
	return p, nil
}

type execProcess struct {
	stdout *io.PipeReader
	stderr bytes.Buffer
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	once   sync.Once
}

func (p *execProcess) Stdout() io.Reader { return p.stdout }

func (p *execProcess) Wait() error {
	<-p.done
	return p.err
}

// Stop 先終止指令，再關掉讀端：沒人讀 stdout 時，exec 的複製 goroutine 才不會卡住 Wait。
func (p *execProcess) Stop() {
	p.once.Do(func() {
		p.cancel()
		p.stdout.Close()
		<-p.done
	})
}
