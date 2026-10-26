package gmm

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	adminPass = "fake-admin-pass"
	connPass  = "fake-conn-pass"
	row       = "u1|pw1|g1|ssh|127.0.0.1|22|acct|" + connPass + "|dom|\n"
)

func TestParseMappingRejectsBadLine(t *testing.T) {
	_, err := ParseMapping(row + "a|b|c|d|e|f|g|h|i\n")
	var me *MappingError
	if !errors.As(err, &me) {
		t.Fatalf("9 欄的行沒有被擋下：%v", err)
	}
	if me.Line != 2 {
		t.Errorf("應指出第 2 行，得到 %d", me.Line)
	}
}

func TestParseMappingSkipsCommentsAndBlank(t *testing.T) {
	rows, err := ParseMapping("# 註解\n\n" + row)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UserAccount != "u1" {
		t.Fatalf("應只解析出 u1 一筆：%+v", rows)
	}
}

func TestConnName(t *testing.T) {
	rows, err := ParseMapping("u|p|g|rdp|1.2.3.4|3389|acct|pw|dom|\nu|p|g|rdp|1.2.3.4|3389|acct|pw|dom|a\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		row   Row
		alive bool
		want  string
	}{
		{rows[0], true, "【dom】acct@1.2.3.4"},
		{rows[0], false, "【dom】acct@1.2.3.4.X"},
		{rows[1], true, "【dom】acct@1.2.3.4【a】"},
	} {
		if got := ConnName(c.row, c.alive); got != c.want {
			t.Errorf("ConnName(%+v, %v) = %q，要 %q", c.row, c.alive, got, c.want)
		}
	}
}

func TestPlanKeepsAdminAndRoot(t *testing.T) {
	rows, _ := ParseMapping(row)
	state := State{
		Users:       map[string]bool{"guacadmin": true, "u_old": true, "u1": true},
		Groups:      map[string]string{"ROOT": "0", "g_old": "5"},
		Connections: map[string]string{},
	}
	plan := BuildPlan(rows, []bool{true}, state, "guacadmin")
	if !reflect.DeepEqual(plan.DeleteUsers, []string{"u_old"}) {
		t.Errorf("DeleteUsers = %v", plan.DeleteUsers)
	}
	if !reflect.DeepEqual(plan.DeleteGroups, []string{"g_old"}) {
		t.Errorf("DeleteGroups = %v", plan.DeleteGroups)
	}
	if !reflect.DeepEqual(plan.CreateGroups, []string{"g1"}) {
		t.Errorf("CreateGroups = %v", plan.CreateGroups)
	}
}

func TestPlanExistingConnectionIsPut(t *testing.T) {
	rows, _ := ParseMapping(row)
	state := State{Users: map[string]bool{}, Groups: map[string]string{}, Connections: map[string]string{"【dom】acct@127.0.0.1": "7"}}
	plan := BuildPlan(rows, []bool{true}, state, "guacadmin")
	if !reflect.DeepEqual(plan.UpdateConnections, []string{"【dom】acct@127.0.0.1"}) {
		t.Errorf("UpdateConnections = %v", plan.UpdateConnections)
	}
	if len(plan.CreateConnections) != 0 {
		t.Errorf("CreateConnections 應為空：%v", plan.CreateConnections)
	}
}

func TestReadConf(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sync.conf")
	text := "# 註解\nA=http://x/guacamole   # 行尾註解\nB='a b'\nC=\"q\"\nD=\n\nnoequals\n"
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadConf(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "http://x/guacamole", "B": "a b", "C": "q", "D": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadConf = %v，要 %v", got, want)
	}
}
