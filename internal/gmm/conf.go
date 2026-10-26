// Package gmm 透過 guacamole 的 REST API，讓 user、group、connection 與 mapping.env 一致
// （冪等，含清理與目標埠的存活探測）。設定讀 services/gmm 的 sync.conf 與 sync.secret.env。
// guacadmin 密碼與產生的使用者密碼只在 HTTP body 與記憶體裡，不進 argv、輸出或錯誤訊息。
package gmm

import (
	"bufio"
	"os"
	"strings"
)

// ReadConf 讀 bash 可 source 的 KEY=VALUE 檔：值可加引號；
// 不加引號時到第一個空白為止，之後的 # 是註解。沒有 = 的行略過。
func ReadConf(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, rest, _ := strings.Cut(line, "=")
		var value string
		if rest != "" && (rest[0] == '\'' || rest[0] == '"') && strings.IndexByte(rest[1:], rest[0]) >= 0 {
			value = rest[1 : 1+strings.IndexByte(rest[1:], rest[0])]
		} else if fields := strings.Fields(rest); len(fields) > 0 {
			value = fields[0]
		}
		values[strings.TrimSpace(key)] = value
	}
	return values, sc.Err()
}
