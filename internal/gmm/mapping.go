package gmm

import (
	"fmt"
	"strings"
)

// fieldCount 是 mapping.env 每行的欄數。
const fieldCount = 10

// Row 是 mapping.env 的一行：user|userpass|group|proto|ip|port|acct|pass|domain|alias。
type Row struct {
	UserAccount  string
	UserPassword string
	ConnGroup    string
	ConnProtocol string
	ConnIP       string
	ConnPort     string
	ConnAccount  string
	ConnPassword string
	ConnDomain   string
	ConnAlias    string
}

// MappingError 表示 mapping.env 某一行欄數不對。Line 從 1 起算。
type MappingError struct {
	Line   int
	Fields int
}

func (e *MappingError) Error() string {
	return fmt.Sprintf("mapping.env 第 %d 行有 %d 欄，要恰好 %d 欄", e.Line, e.Fields, fieldCount)
}

// ParseMapping 解析 mapping.env。任一行欄數不對就回 *MappingError，一筆都不回。
// 空行與 # 開頭的行略過；user 欄空的行整行略過，照舊版行為。
func ParseMapping(text string) ([]Row, error) {
	var rows []Row
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "|")
		if len(p) != fieldCount {
			return nil, &MappingError{Line: i + 1, Fields: len(p)}
		}
		r := Row{p[0], p[1], p[2], p[3], p[4], p[5], p[6], p[7], p[8], p[9]}
		if r.UserAccount != "" {
			rows = append(rows, r)
		}
	}
	return rows, nil
}

// ConnName 組連線名：【domain】acct@ip，alias 非空時加【alias】，目標埠探不到時加 .X。
func ConnName(r Row, alive bool) string {
	name := fmt.Sprintf("【%s】%s@%s", r.ConnDomain, r.ConnAccount, r.ConnIP)
	if r.ConnAlias != "" {
		name += "【" + r.ConnAlias + "】"
	}
	if !alive {
		name += ".X"
	}
	return name
}
