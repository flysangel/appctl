package gmm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake 是假的 guacamole REST API，行為照舊版 tests/test_gmm_sync.py 的 Fake。
type fake struct {
	mu           sync.Mutex
	users        map[string]map[string]any
	groups       map[string]map[string]any
	connections  map[string]map[string]any
	profiles     map[string]map[string]any
	requests     []request
	failUserPost bool
	nextID       int
}

type request struct {
	method, path, body string
	header             http.Header
}

func newFake() *fake {
	return &fake{
		users:       map[string]map[string]any{},
		groups:      map[string]map[string]any{},
		connections: map[string]map[string]any{},
		profiles:    map[string]map[string]any{},
		nextID:      100,
	}
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	f.requests = append(f.requests, request{r.Method, r.URL.RequestURI(), string(raw), r.Header.Clone()})
	reply := func(code int, body any) {
		if body == nil {
			body = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(body)
	}
	const base = "/guacamole/api/session/data/mysql/"
	if r.URL.Path == "/guacamole/api/tokens" {
		reply(200, map[string]string{"authToken": "tok"})
		return
	}
	if !strings.HasPrefix(r.URL.Path, base) {
		reply(404, nil)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, base), "/")
	var body map[string]any
	if len(raw) > 0 && r.Method != http.MethodPatch {
		json.Unmarshal(raw, &body)
	}
	f.nextID++
	nid := strconv.Itoa(f.nextID)
	if parts[0] == "users" {
		switch r.Method {
		case http.MethodGet:
			reply(200, f.users)
		case http.MethodPost:
			if f.failUserPost {
				reply(500, nil)
				return
			}
			name := body["username"].(string)
			f.users[name] = map[string]any{"username": name}
			reply(200, body)
		case http.MethodDelete:
			delete(f.users, parts[1])
			reply(204, nil)
		default:
			reply(204, nil)
		}
		return
	}
	store := map[string]map[string]map[string]any{
		"connectionGroups": f.groups, "connections": f.connections, "sharingProfiles": f.profiles,
	}[parts[0]]
	if store == nil {
		reply(404, nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := map[string]any{}
		for k, v := range store {
			m := map[string]any{"identifier": k}
			for kk, vv := range v {
				m[kk] = vv
			}
			out[k] = m
		}
		reply(200, out)
	case http.MethodPost:
		store[nid] = body
		reply(200, body)
	case http.MethodPut:
		store[parts[1]] = body
		reply(204, nil)
	case http.MethodDelete:
		delete(store, parts[1])
		reply(204, nil)
	default:
		reply(405, nil)
	}
}

func (f *fake) mutations() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []request
	for _, r := range f.requests {
		if r.method != http.MethodGet && !strings.HasSuffix(r.path, "/api/tokens") {
			out = append(out, r)
		}
	}
	return out
}

type setup struct {
	mapping   *string
	randomCSV *string
	fake      *fake
	confirm   func(*os.File, io.Writer) error
	stdin     *os.File
	dialErr   bool
	conf      string // 非空時取代預設的 sync.conf
	server    bool   // true 時經 Options.Server 指向假伺服器
}

type result struct {
	err       error
	out, err2 string
	fake      *fake
	dials     []string
	csv       string
	csvMode   os.FileMode
	csvFound  bool
}

func yes(*os.File, io.Writer) error { return nil }

func cancel(_ *os.File, out io.Writer) error {
	io.WriteString(out, "Info: 已取消（輸入的名稱與服務名不符）\n")
	return errors.New("已取消刪除")
}

func ptr(s string) *string { return &s }

// runGmm 在 tmp 目錄放一份 services/gmm 的設定檔，對假伺服器跑 Seed 或 Check。
func runGmm(t *testing.T, check bool, s setup) result {
	t.Helper()
	if s.fake == nil {
		s.fake = newFake()
	}
	if s.mapping == nil {
		s.mapping = ptr(row)
	}
	if s.confirm == nil && s.stdin == nil {
		s.confirm = yes
	}
	srv := httptest.NewServer(s.fake)
	defer srv.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "services", "gmm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := s.conf
	if conf == "" {
		conf = "GUACAMOLE_SERVER=" + srv.URL + "/guacamole   # 註解\nGUACAMOLE_ADMIN=guacadmin\n"
	}
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sync.conf", conf)
	write("sync.secret.env", "GUACAMOLE_PASS="+adminPass+"\n")
	write("mapping.env", *s.mapping)
	if s.randomCSV != nil {
		write("random.csv", *s.randomCSV)
	}
	var dials []string
	var out, errOut bytes.Buffer
	opts := Options{
		Root:    root,
		Stdin:   s.stdin,
		Out:     &out,
		Err:     &errOut,
		Confirm: s.confirm,
		Dial: func(network, addr string, timeout time.Duration) (net.Conn, error) {
			dials = append(dials, network+" "+addr)
			if s.dialErr {
				return nil, errors.New("connection refused")
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		},
	}
	if s.server {
		opts.Server = srv.URL + "/guacamole"
	}
	var err error
	if check {
		err = Check(context.Background(), opts)
	} else {
		err = Seed(context.Background(), opts)
	}
	res := result{err: err, out: out.String(), err2: errOut.String(), fake: s.fake, dials: dials}
	if st, e := os.Stat(filepath.Join(dir, "random.csv")); e == nil {
		b, _ := os.ReadFile(filepath.Join(dir, "random.csv"))
		res.csv, res.csvMode, res.csvFound = string(b), st.Mode().Perm(), true
	}
	return res
}

func TestTokenInHeaderNotURL(t *testing.T) {
	r := runGmm(t, false, setup{})
	if r.err != nil {
		t.Fatalf("%v\n%s%s", r.err, r.out, r.err2)
	}
	for _, q := range r.fake.requests {
		if strings.Contains(q.path, "token=") {
			t.Errorf("token 不該出現在 URL：%s", q.path)
		}
		if !strings.HasSuffix(q.path, "/api/tokens") && q.header.Get("Guacamole-Token") != "tok" {
			t.Errorf("%s %s 沒帶 Guacamole-Token 標頭", q.method, q.path)
		}
	}
}

func TestCancelMakesNoMutation(t *testing.T) {
	r := runGmm(t, false, setup{confirm: cancel})
	if r.err == nil {
		t.Fatal("取消時應回錯")
	}
	if !strings.Contains(r.out, "已取消") {
		t.Errorf("應印已取消：%q", r.out)
	}
	if m := r.fake.mutations(); len(m) != 0 {
		t.Errorf("取消時不該有任何寫入：%v", m)
	}
}

func TestNoTTYMakesNoMutation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stdin")
	os.WriteFile(p, []byte("gmm\n"), 0o600)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// confirm 留 nil，走真的 confirm.Prompt：stdin 是一般檔案，不是終端機
	r := runGmm(t, false, setup{stdin: f})
	if r.err == nil {
		t.Fatal("沒有 tty 時應回錯")
	}
	if m := r.fake.mutations(); len(m) != 0 {
		t.Errorf("沒有 tty 時不該有任何寫入：%v", m)
	}
}

func TestBadMappingAbortsBeforeCleanup(t *testing.T) {
	f := newFake()
	f.users["stale"] = map[string]any{"username": "stale"}
	r := runGmm(t, false, setup{mapping: ptr(row + "bad|line\n"), fake: f})
	if r.err == nil {
		t.Fatal("mapping.env 有壞行時應回錯")
	}
	if m := r.fake.mutations(); len(m) != 0 {
		t.Errorf("mapping.env 有壞行時不該有任何寫入：%v", m)
	}
}

func TestEmptyMappingAborts(t *testing.T) {
	f := newFake()
	f.users["stale"] = map[string]any{"username": "stale"}
	r := runGmm(t, false, setup{mapping: ptr("# 只有註解\n\n"), fake: f})
	if r.err == nil || !strings.Contains(r.err.Error(), "沒有任何一筆") {
		t.Fatalf("mapping.env 沒有任何一筆時應回錯：%v", r.err)
	}
	if m := r.fake.mutations(); len(m) != 0 {
		t.Errorf("不該有任何寫入：%v", m)
	}
}

func TestAPIFailureNonzero(t *testing.T) {
	f := newFake()
	f.failUserPost = true
	r := runGmm(t, false, setup{mapping: ptr("u1||g1|ssh|127.0.0.1|22|acct|pw|dom|\n"), fake: f})
	if r.err == nil {
		t.Fatal("API 失敗時應回錯")
	}
	if !strings.Contains(r.err2, "Error:") {
		t.Errorf("失敗的那一行應印 Error:：%q", r.err2)
	}
}

func TestRandomCSVAppends(t *testing.T) {
	r := runGmm(t, false, setup{
		mapping:   ptr("newuser||g1|ssh|127.0.0.1|22|acct|pw|dom|\n"),
		randomCSV: ptr("User,Password\nold,dummy-old\n"),
	})
	if r.err != nil {
		t.Fatalf("%v\n%s%s", r.err, r.out, r.err2)
	}
	lines := strings.Split(strings.TrimSuffix(r.csv, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "User,Password" || lines[1] != "old,dummy-old" || !strings.HasPrefix(lines[2], "newuser,") {
		t.Fatalf("random.csv 應附加一行 newuser：%q", lines)
	}
	if pw := strings.TrimPrefix(lines[2], "newuser,"); len(pw) != 12 {
		t.Errorf("產生的密碼應為 PASS_LENGTH 預設 12 位，得到 %d 位", len(pw))
	} else if strings.Contains(r.out, pw) || strings.Contains(r.err2, pw) {
		t.Error("產生的密碼不該印到終端")
	}
	if r.csvMode != 0o600 {
		t.Errorf("random.csv 權限應為 0600，得到 %o", r.csvMode)
	}
}

func TestRandomCSVCreatedWithHeader(t *testing.T) {
	r := runGmm(t, false, setup{mapping: ptr("newuser||g1|ssh|127.0.0.1|22|acct|pw|dom|\n")})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !r.csvFound || !strings.HasPrefix(r.csv, "User,Password\nnewuser,") || r.csvMode != 0o600 {
		t.Errorf("新建的 random.csv 應有標頭且權限 0600：%q %o", r.csv, r.csvMode)
	}
}

func TestSecretsNotInProbeAddress(t *testing.T) {
	r := runGmm(t, false, setup{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.dials) != 1 || r.dials[0] != "tcp 127.0.0.1:22" {
		t.Fatalf("應只探 tcp 127.0.0.1:22：%v", r.dials)
	}
	for _, s := range []string{r.out, r.err2} {
		if strings.Contains(s, adminPass) || strings.Contains(s, connPass) {
			t.Errorf("密碼不該出現在輸出：%q", s)
		}
	}
}

func TestCleanupDeletesEveryDuplicate(t *testing.T) {
	// 舊版逐筆刪 mapping 以外的項目；同名的 connection 或 group 有好幾筆時，每一筆都要刪
	f := newFake()
	f.connections["7"] = map[string]any{"name": "dup"}
	f.connections["8"] = map[string]any{"name": "dup"}
	f.groups["5"] = map[string]any{"name": "gdup"}
	f.groups["6"] = map[string]any{"name": "gdup"}
	r := runGmm(t, false, setup{fake: f})
	if r.err != nil {
		t.Fatalf("%v\n%s%s", r.err, r.out, r.err2)
	}
	deleted := map[string]bool{}
	for _, m := range r.fake.mutations() {
		if m.method == http.MethodDelete {
			deleted[m.path[strings.LastIndex(m.path, "/")+1:]] = true
		}
	}
	for _, id := range []string{"5", "6", "7", "8"} {
		if !deleted[id] {
			t.Errorf("%s 沒有被刪：%v", id, deleted)
		}
	}
}

func TestPlanPrintedBeforeConfirm(t *testing.T) {
	f := newFake()
	f.users["stale"] = map[string]any{"username": "stale"}
	r := runGmm(t, false, setup{confirm: cancel, fake: f})
	i, j := strings.Index(r.out, "stale"), strings.Index(r.out, "已取消")
	if i < 0 || j < 0 || i > j {
		t.Errorf("計畫應在確認之前印出：%q", r.out)
	}
}

func TestCheckModeChecksMapping(t *testing.T) {
	r := runGmm(t, true, setup{mapping: ptr("a|b|c\n")})
	if r.err == nil {
		t.Fatal("mapping.env 欄數不對時，check 應回錯")
	}
	if m := r.fake.mutations(); len(m) != 0 {
		t.Errorf("check 不該有任何寫入：%v", m)
	}
}

func TestCheckSucceedsWithoutWriting(t *testing.T) {
	r := runGmm(t, true, setup{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, "Success:") || !strings.Contains(r.out, "1 筆") {
		t.Errorf("check 應印 Success 與筆數：%q", r.out)
	}
	if m := r.fake.mutations(); len(m) != 0 {
		t.Errorf("check 不該有任何寫入：%v", m)
	}
}

func TestProbeClosedAddsX(t *testing.T) {
	r := runGmm(t, false, setup{dialErr: true})
	if r.err != nil {
		t.Fatal(r.err)
	}
	found := false
	for _, m := range r.fake.mutations() {
		if m.method == http.MethodPost && strings.HasSuffix(m.path, "/connections") {
			found = strings.Contains(m.body, `"【dom】acct@127.0.0.1.X"`)
		}
	}
	if !found {
		t.Error("探不到時，連線名應加 .X")
	}
}

func TestServerOptionOverridesConf(t *testing.T) {
	r := runGmm(t, false, setup{server: true, conf: "GUACAMOLE_SERVER=http://127.0.0.1:1/guacamole\nGUACAMOLE_ADMIN=guacadmin\n"})
	if r.err != nil {
		t.Fatalf("Options.Server 應取代 sync.conf 的 GUACAMOLE_SERVER：%v", r.err)
	}
}

func TestSeedSyncsSharingProfilesAndPermissions(t *testing.T) {
	r := runGmm(t, false, setup{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	var profiles []string
	patched := false
	for _, m := range r.fake.mutations() {
		if m.method == http.MethodPost && strings.HasSuffix(m.path, "/sharingProfiles") {
			var b map[string]any
			json.Unmarshal([]byte(m.body), &b)
			profiles = append(profiles, b["name"].(string))
		}
		if m.method == http.MethodPatch && strings.HasSuffix(m.path, "/users/u1/permissions") {
			patched = strings.Contains(m.body, `"op":"add"`) && strings.Contains(m.body, "connectionPermissions")
		}
	}
	if strings.Join(profiles, ",") != "ro,rw" {
		t.Errorf("每條連線應建 ro、rw 兩個 sharing profile：%v", profiles)
	}
	if !patched {
		t.Error("應以 add 加上 connection 權限")
	}
	// 第二次跑：連線已存在，走 PUT，profile 不重建
	f := r.fake
	f.requests = nil
	r2 := runGmm(t, false, setup{fake: f})
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	for _, m := range f.mutations() {
		if m.method == http.MethodPost && (strings.HasSuffix(m.path, "/connections") || strings.HasSuffix(m.path, "/sharingProfiles") || strings.HasSuffix(m.path, "/users")) {
			t.Errorf("第二次跑不該再建：%s %s", m.method, m.path)
		}
	}
}
