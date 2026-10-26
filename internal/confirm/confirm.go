// Package confirm 在破壞性動作前要使用者輸入服務名確認。
package confirm

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// isTerminal 判斷 f 是不是終端機。測試換掉它，模擬有 tty。
var isTerminal = func(f *os.File) bool { return f != nil && term.IsTerminal(int(f.Fd())) }

// Ask 印出確認提示，讀一行輸入，等於 svc 才回 nil。
// in 不是終端機時直接回錯，不印提示：腳本或管線裡不能替使用者按確認。
func Ask(in *os.File, out io.Writer, svc, track string) error {
	if !isTerminal(in) {
		return errors.New("stdin 不是終端機，無法確認刪除；請在終端機直接執行")
	}
	fmt.Fprintf(out, "刪除 %s（%s），含資料卷、不可逆。輸入服務名確認：", svc, track)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("讀不到輸入：%w", err)
	}
	if strings.TrimSpace(line) != svc {
		fmt.Fprintln(out, "Info: 已取消（輸入的名稱與服務名不符）")
		return errors.New("已取消刪除")
	}
	return nil
}
