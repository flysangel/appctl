package quadlet

import (
	"fmt"
	"strings"
)

// Escape 把 systemd 會改寫的字元跳脫：% 改 %%（specifier），$ 改 $$（環境變數展開）。
// 凡是最後會進到產生出來的 ExecStart= 的值（Exec=、PodmanArgs=、Environment=、Volume=…）
// 都要先過這裡；沒跳脫時 systemd 會改寫指令而且不報錯（例如 %T 變成 /tmp）。
func Escape(s string) string {
	return strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
}

// quote 把一個參數寫成 systemd 命令列的一個字（Quadlet 的 Exec=、PodmanArgs=、
// Environment= 用同一套切字規則）。不需要時原樣回傳；需要時包雙引號，
// 並把 \、"、換行與其他控制字元寫成 C 風格跳脫，讓含空白或換行的參數仍是一個字。
// 呼叫端要先 Escape 再 quote。
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r\"'\\;") && !hasControl(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// words 把每個參數 quote 後用空白接起來，寫成一行 systemd 命令列。
func words(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = quote(Escape(a))
	}
	return strings.Join(q, " ")
}
