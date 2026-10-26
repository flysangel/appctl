package gmm

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// State 是 guacamole 的現況。Groups 與 Connections 是名稱對 identifier。
type State struct {
	Users       map[string]bool
	Groups      map[string]string
	Connections map[string]string
}

// Plan 是同步計畫：要建、要覆寫、要刪的名稱。
type Plan struct {
	CreateUsers       []string
	CreateGroups      []string
	CreateConnections []string
	UpdateConnections []string
	DeleteUsers       []string
	DeleteGroups      []string
	DeleteConnections []string
}

// BuildPlan 比對 mapping 與現況。admin 帳號與 ROOT 群組永遠不刪。
// alive[i] 是 rows[i] 的目標埠有沒有探到。
func BuildPlan(rows []Row, alive []bool, state State, admin string) Plan {
	var plan Plan
	users, groups, names := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, r := range rows {
		users[r.UserAccount] = true
		groups[r.ConnGroup] = true
		n := ConnName(r, alive[i])
		if names[n] {
			continue
		}
		names[n] = true
		if _, ok := state.Connections[n]; ok {
			plan.UpdateConnections = append(plan.UpdateConnections, n)
		} else {
			plan.CreateConnections = append(plan.CreateConnections, n)
		}
	}
	for u := range users {
		if !state.Users[u] {
			plan.CreateUsers = append(plan.CreateUsers, u)
		}
	}
	for g := range groups {
		if _, ok := state.Groups[g]; !ok {
			plan.CreateGroups = append(plan.CreateGroups, g)
		}
	}
	for u := range state.Users {
		if !users[u] && u != admin {
			plan.DeleteUsers = append(plan.DeleteUsers, u)
		}
	}
	for g := range state.Groups {
		if !groups[g] && g != "ROOT" {
			plan.DeleteGroups = append(plan.DeleteGroups, g)
		}
	}
	for c := range state.Connections {
		if !names[c] {
			plan.DeleteConnections = append(plan.DeleteConnections, c)
		}
	}
	for _, s := range [][]string{plan.CreateUsers, plan.CreateGroups, plan.DeleteUsers, plan.DeleteGroups, plan.DeleteConnections} {
		sort.Strings(s)
	}
	return plan
}

// Print 印出同步計畫。
func (p Plan) Print(w io.Writer) {
	fmt.Fprintln(w, "Info: 同步計畫")
	for _, item := range []struct {
		label string
		names []string
	}{
		{"建立 user", p.CreateUsers},
		{"建立 group", p.CreateGroups},
		{"建立 connection", p.CreateConnections},
		{"覆寫 connection", p.UpdateConnections},
		{"刪除 user", p.DeleteUsers},
		{"刪除 group", p.DeleteGroups},
		{"刪除 connection", p.DeleteConnections},
	} {
		text := "無"
		if len(item.names) > 0 {
			text = strings.Join(item.names, "、")
		}
		fmt.Fprintf(w, "  %s：%s\n", item.label, text)
	}
}

// byName 把 guacamole 的列表（identifier 對物件）轉成名稱對 identifier。
// 同名有好幾筆時，取 identifier 排序最後的那筆，結果固定。
func byName(listing map[string]map[string]any) map[string]string {
	ids := make([]string, 0, len(listing))
	for id := range listing {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := map[string]string{}
	for _, id := range ids {
		if name, ok := listing[id]["name"].(string); ok {
			out[name] = id
		}
	}
	return out
}
