package run

import (
	"context"
	"io"
	"strings"
	"sync"
)

// Call 是 Fake 記下的一次呼叫。
type Call struct {
	Name  string
	Args  []string
	Stdin []byte
}

// Line 回傳這次呼叫的指令行。
func (c Call) Line() string { return Line(c.Name, c.Args) }

// Response 是 Fake 對某個指令行前綴的預設回應。Code 非零時回 *ExitError。
type Response struct {
	Out  string
	Code int
	Err  error // 非 nil 時原樣回傳這個錯誤，優先於 Code
}

// Fake 是測試用的 Runner：記下每次呼叫，依指令行前綴回預設輸出。
// 多個前綴都符合時，取最長的；沒有符合的前綴時，回空輸出、結束碼 0。
type Fake struct {
	Calls     []Call
	Responses map[string]Response
}

// On 設定指令行以 prefix 開頭時的回應。
func (f *Fake) On(prefix, out string, code int) {
	if f.Responses == nil {
		f.Responses = map[string]Response{}
	}
	f.Responses[prefix] = Response{Out: out, Code: code}
}

// OnError 設定指令行以 prefix 開頭時回傳指定的錯誤，例如 *ExitError 帶 stderr，或指令不存在。
func (f *Fake) OnError(prefix string, err error) {
	if f.Responses == nil {
		f.Responses = map[string]Response{}
	}
	f.Responses[prefix] = Response{Err: err}
}

// Run 實作 Runner。
func (f *Fake) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	c := Call{Name: name, Args: append([]string(nil), args...), Stdin: append([]byte(nil), stdin...)}
	f.Calls = append(f.Calls, c)
	line := c.Line()
	best, found := "", false
	for p := range f.Responses {
		if strings.HasPrefix(line, p) && (!found || len(p) > len(best)) {
			best, found = p, true
		}
	}
	if !found {
		return nil, nil
	}
	r := f.Responses[best]
	if r.Err != nil {
		return []byte(r.Out), r.Err
	}
	if r.Code != 0 {
		return []byte(r.Out), &ExitError{Cmd: line, Code: r.Code}
	}
	return []byte(r.Out), nil
}

// Lines 回傳全部呼叫的指令行。
func (f *Fake) Lines() []string {
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = c.Line()
	}
	return out
}

// Called 回傳有沒有任何呼叫的指令行以 prefix 開頭。
func (f *Fake) Called(prefix string) bool {
	for _, c := range f.Calls {
		if strings.HasPrefix(c.Line(), prefix) {
			return true
		}
	}
	return false
}

// FakeStarter 是測試用的 Starter：記下每次啟動，每個 Process 的 stdout 是 Out，Wait 回 Exit。
// StartErr 非 nil 時，Start 直接回這個錯誤。
type FakeStarter struct {
	Calls    []Call
	Procs    []*FakeProcess
	Out      string
	Exit     error
	StartErr error
}

// Start 實作 Starter。
func (f *FakeStarter) Start(_ context.Context, name string, args []string) (Process, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	if f.StartErr != nil {
		return nil, f.StartErr
	}
	p := &FakeProcess{out: strings.NewReader(f.Out), exit: f.Exit}
	f.Procs = append(f.Procs, p)
	return p, nil
}

// FakeProcess 是 FakeStarter 啟動的 Process。
type FakeProcess struct {
	mu      sync.Mutex
	out     io.Reader
	exit    error
	stopped bool
}

// Stdout 實作 Process。
func (p *FakeProcess) Stdout() io.Reader { return p.out }

// Wait 實作 Process。
func (p *FakeProcess) Wait() error { return p.exit }

// Stop 實作 Process。
func (p *FakeProcess) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
}

// Stopped 回傳 Stop 有沒有被呼叫過。
func (p *FakeProcess) Stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}
