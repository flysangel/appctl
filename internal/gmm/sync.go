package gmm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/flysangel/appctl/internal/confirm"
)

// probeTimeout 是探一個目標埠的上限。
const probeTimeout = 3 * time.Second

// confirmPrompt 是 Seed 動資料前的確認提示。
const confirmPrompt = "刪除 mapping.env 以外的 user、group、connection，不可逆。輸入服務名確認："

// Options 是 Seed 與 Check 的輸入。
type Options struct {
	Root   string    // repo 根目錄；設定檔在 services/gmm
	Server string    // guacamole 網址，含 /guacamole；空字串時讀 sync.conf 的 GUACAMOLE_SERVER
	Stdin  *os.File  // 確認用
	Out    io.Writer // Info:、Success: 與計畫
	Err    io.Writer // 單行失敗的 Error:；nil 時用 os.Stderr
	// Dial 探目標埠；nil 時用 net.DialTimeout。測試換掉它。
	Dial func(network, address string, timeout time.Duration) (net.Conn, error)
	// Confirm 是動資料前的確認；nil 時用 confirm.Prompt，要輸入 gmm。測試換掉它。
	Confirm func(in *os.File, out io.Writer) error
}

type config struct {
	server, admin, pass, dataSource string
	passLength                      int
}

func gmmDir(root string) string { return filepath.Join(root, "services", "gmm") }

// loadConfig 讀 sync.conf 與 sync.secret.env。Options.Server 非空時取代 GUACAMOLE_SERVER。
func loadConfig(opts Options) (config, error) {
	dir := gmmDir(opts.Root)
	conf := map[string]string{}
	for _, name := range []string{"sync.conf", "sync.secret.env"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			return config{}, fmt.Errorf("%s 不存在（這個檔在版控裡，用 git restore 還原）", p)
		}
		v, err := ReadConf(p)
		if err != nil {
			return config{}, fmt.Errorf("讀不到 %s：%w", p, err)
		}
		for k, val := range v {
			conf[k] = val
		}
	}
	c := config{server: opts.Server, admin: conf["GUACAMOLE_ADMIN"], pass: conf["GUACAMOLE_PASS"], dataSource: conf["DATA_SOURCE"], passLength: 12}
	if c.server == "" {
		c.server = conf["GUACAMOLE_SERVER"]
	}
	for _, k := range []struct{ key, val string }{{"GUACAMOLE_SERVER", c.server}, {"GUACAMOLE_ADMIN", c.admin}, {"GUACAMOLE_PASS", c.pass}} {
		if k.val == "" {
			return config{}, fmt.Errorf("sync.conf 或 sync.secret.env 缺 %s", k.key)
		}
	}
	if c.dataSource == "" {
		c.dataSource = "mysql"
	}
	if s := conf["PASS_LENGTH"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return config{}, fmt.Errorf("sync.conf 的 PASS_LENGTH 要是正整數，現在是 %q", s)
		}
		c.passLength = n
	}
	return c, nil
}

func loadMapping(root string) ([]Row, error) {
	p := filepath.Join(gmmDir(root), "mapping.env")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("%s 不存在（這個檔在版控裡，用 git restore 還原）", p)
	}
	rows, err := ParseMapping(string(b))
	if err != nil {
		return nil, fmt.Errorf("%v，整批中止，什麼都不改", err)
	}
	return rows, nil
}

// prepare 讀設定與 mapping，再取 token。三步都在任何寫入之前。
func prepare(ctx context.Context, opts Options) (config, []Row, *client, error) {
	cfg, err := loadConfig(opts)
	if err != nil {
		return config{}, nil, nil, err
	}
	rows, err := loadMapping(opts.Root)
	if err != nil {
		return config{}, nil, nil, err
	}
	c := newClient(cfg.server, cfg.dataSource)
	if err := c.login(ctx, cfg.admin, cfg.pass); err != nil {
		return config{}, nil, nil, fmt.Errorf("取 token 失敗（檢查 sync.conf 的 SERVER、ADMIN 與 sync.secret.env 的 PASS）：%w", err)
	}
	return cfg, rows, c, nil
}

// Check 唯讀檢查：設定檔齊全、mapping.env 每行欄數正確、取得到 token。
func Check(ctx context.Context, opts Options) error {
	_, rows, _, err := prepare(ctx, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Out, "Success: token ok，mapping.env 有 %d 筆\n", len(rows))
	return nil
}

// Seed 讓 guacamole 與 mapping.env 一致：先印計畫，確認後才寫入。
// 單行失敗印 Error: 並繼續其餘行，最後回錯。
func Seed(ctx context.Context, opts Options) error {
	errOut := opts.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	cfg, rows, c, err := prepare(ctx, opts)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("mapping.env 沒有任何一筆連線，照這份清單同步會刪光所有資料，中止")
	}
	alive := make([]bool, len(rows))
	for i, r := range rows {
		alive[i] = probe(opts.Dial, r)
	}
	state, err := readState(ctx, c)
	if err != nil {
		return fmt.Errorf("讀取現況失敗：%w", err)
	}
	plan := BuildPlan(rows, alive, state, cfg.admin)
	plan.Print(opts.Out)
	ask := opts.Confirm
	if ask == nil {
		ask = func(in *os.File, out io.Writer) error { return confirm.Prompt(in, out, confirmPrompt, "gmm") }
	}
	if err := ask(opts.Stdin, opts.Out); err != nil {
		return err
	}
	failed := 0
	for i, r := range rows {
		if err := syncRow(ctx, c, opts, r, ConnName(r, alive[i]), &state, cfg.passLength); err != nil {
			fmt.Fprintf(errOut, "Error: %s 這一行同步失敗：%v\n", r.UserAccount, err)
			failed++
		}
	}
	failed += cleanup(ctx, c, opts.Out, errOut, plan)
	if failed > 0 {
		return fmt.Errorf("sync 有 %d 處失敗，見上方 Error:", failed)
	}
	fmt.Fprintln(opts.Out, "Success: sync 完成")
	return nil
}

// probe 只拿 IP 與埠去連，連得上就算 open。
func probe(dial func(string, string, time.Duration) (net.Conn, error), r Row) bool {
	if dial == nil {
		dial = net.DialTimeout
	}
	conn, err := dial("tcp", net.JoinHostPort(r.ConnIP, r.ConnPort), probeTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func readState(ctx context.Context, c *client) (State, error) {
	users, err := c.list(ctx, "users")
	if err != nil {
		return State{}, err
	}
	groups, err := c.list(ctx, "connectionGroups")
	if err != nil {
		return State{}, err
	}
	conns, err := c.list(ctx, "connections")
	if err != nil {
		return State{}, err
	}
	s := State{Users: map[string]bool{}, Groups: byName(groups), Connections: byName(conns)}
	for u := range users {
		s.Users[u] = true
	}
	return s, nil
}

// params 是連線參數。協定參數寫死：rdp 帶 security nla 與 ignore-cert；ssh 帶配色與字級；其他協定只送四欄。
func params(r Row) map[string]string {
	p := map[string]string{"hostname": r.ConnIP, "port": r.ConnPort, "username": r.ConnAccount}
	p["password"] = r.ConnPassword
	switch r.ConnProtocol {
	case "rdp":
		p["domain"] = r.ConnDomain
		p["security"] = "nla"
		p["ignore-cert"] = "true"
	case "ssh":
		p["color-scheme"] = "black-white"
		p["font-size"] = "14"
	}
	return p
}

const passAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func generatePassword(n int) (string, error) {
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(passAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = passAlphabet[k.Int64()]
	}
	return string(b), nil
}

// recordPassword 把產生的密碼附加到 random.csv（0600）。這是唯一留存處，終端不印。
func recordPassword(root, user, pass string) error {
	p := filepath.Join(gmmDir(root), "random.csv")
	_, statErr := os.Stat(p)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if os.IsNotExist(statErr) {
		if _, err := io.WriteString(f, "User,Password\n"); err != nil {
			return err
		}
	}
	_, err = io.WriteString(f, user+","+pass+"\n")
	return err
}

// syncRow 同步一行：user（已存在不改密碼）、group、connection（已存在就整份 PUT）、
// ro 與 rw 兩個 sharing profile（已存在不動）、READ 權限（只加不減）。
func syncRow(ctx context.Context, c *client, opts Options, r Row, name string, state *State, passLength int) error {
	if !state.Users[r.UserAccount] {
		pass := r.UserPassword
		if pass == "" {
			var err error
			if pass, err = generatePassword(passLength); err != nil {
				return fmt.Errorf("產生密碼失敗：%w", err)
			}
			if err := recordPassword(opts.Root, r.UserAccount, pass); err != nil {
				return fmt.Errorf("寫 random.csv 失敗：%w", err)
			}
		}
		user := map[string]any{"username": r.UserAccount, "attributes": map[string]any{}}
		user["password"] = pass
		if err := c.call(ctx, http.MethodPost, "users", user, nil); err != nil {
			return err
		}
		state.Users[r.UserAccount] = true
		fmt.Fprintf(opts.Out, "Info: create user %s\n", r.UserAccount)
	}
	gid, ok := state.Groups[r.ConnGroup]
	if !ok {
		group := map[string]any{"parentIdentifier": "ROOT", "name": r.ConnGroup, "type": "ORGANIZATIONAL", "attributes": map[string]any{}}
		if err := c.call(ctx, http.MethodPost, "connectionGroups", group, nil); err != nil {
			return err
		}
		groups, err := c.list(ctx, "connectionGroups")
		if err != nil {
			return err
		}
		state.Groups = byName(groups)
		fmt.Fprintf(opts.Out, "Info: create group %s\n", r.ConnGroup)
		if gid, ok = state.Groups[r.ConnGroup]; !ok {
			return fmt.Errorf("group %s 取不到 id", r.ConnGroup)
		}
	}
	payload := map[string]any{
		"parentIdentifier": gid, "name": name, "protocol": r.ConnProtocol, "parameters": params(r),
		"attributes": map[string]string{"max-connections": "", "max-connections-per-user": ""},
	}
	cid, ok := state.Connections[name]
	if ok {
		if err := c.call(ctx, http.MethodPut, "connections/"+url.PathEscape(cid), payload, nil); err != nil {
			return err
		}
	} else {
		if err := c.call(ctx, http.MethodPost, "connections", payload, nil); err != nil {
			return err
		}
		conns, err := c.list(ctx, "connections")
		if err != nil {
			return err
		}
		state.Connections = byName(conns)
		cid, ok = state.Connections[name]
	}
	fmt.Fprintf(opts.Out, "Info: sync connection %s\n", name)
	if !ok {
		return fmt.Errorf("connection %s 取不到 id", name)
	}
	profiles, err := c.list(ctx, "sharingProfiles")
	if err != nil {
		return err
	}
	for _, sp := range []struct{ name, readOnly string }{{"ro", "true"}, {"rw", ""}} {
		exists := false
		for _, p := range profiles {
			if p["name"] == sp.name && p["primaryConnectionIdentifier"] == cid {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		body := map[string]any{"primaryConnectionIdentifier": cid, "name": sp.name,
			"parameters": map[string]string{"read-only": sp.readOnly}, "attributes": map[string]any{}}
		if err := c.call(ctx, http.MethodPost, "sharingProfiles", body, nil); err != nil {
			return err
		}
	}
	patch := []map[string]string{
		{"op": "add", "path": "/connectionGroupPermissions/" + gid, "value": "READ"},
		{"op": "add", "path": "/connectionPermissions/" + cid, "value": "READ"},
	}
	return c.call(ctx, http.MethodPatch, "users/"+url.PathEscape(r.UserAccount)+"/permissions", patch, nil)
}

// cleanup 刪掉計畫裡的項目，回傳失敗數。group 與 connection 重新列一次，
// 同名的每一筆都刪，照舊版逐筆刪的語意。
func cleanup(ctx context.Context, c *client, out, errOut io.Writer, plan Plan) int {
	type target struct{ kind, name, id string }
	var targets []target
	for _, u := range plan.DeleteUsers {
		targets = append(targets, target{"users", u, u})
	}
	for _, k := range []struct {
		kind  string
		names []string
	}{{"connectionGroups", plan.DeleteGroups}, {"connections", plan.DeleteConnections}} {
		if len(k.names) == 0 {
			continue
		}
		want := map[string]bool{}
		for _, n := range k.names {
			want[n] = true
		}
		listing, err := c.list(ctx, k.kind)
		if err != nil {
			fmt.Fprintf(errOut, "Error: 清理前重新列出項目失敗：%v\n", err)
			return 1
		}
		ids := make([]string, 0, len(listing))
		for id := range listing {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if n, _ := listing[id]["name"].(string); want[n] {
				targets = append(targets, target{k.kind, n, id})
			}
		}
	}
	failed := 0
	for _, t := range targets {
		if err := c.call(ctx, http.MethodDelete, t.kind+"/"+url.PathEscape(t.id), nil, nil); err != nil {
			fmt.Fprintf(errOut, "Error: delete %s %s 失敗：%v\n", t.kind, t.name, err)
			failed++
			continue
		}
		fmt.Fprintf(out, "Info: delete %s %s\n", t.kind, t.name)
	}
	return failed
}
