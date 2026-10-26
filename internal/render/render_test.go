package render_test

import (
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
)

func sortDocs(docs []map[string]any) {
	key := func(d map[string]any) string {
		kind, _ := d["kind"].(string)
		md, _ := d["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		return kind + "/" + name
	}
	sort.SliceStable(docs, func(i, j int) bool { return key(docs[i]) < key(docs[j]) })
}

// redactSecrets 回傳複本，kind: Secret 的 data 與 stringData 每個值換成 <redacted>，鍵留著。
func redactSecrets(docs []map[string]any) []map[string]any {
	out := make([]map[string]any, len(docs))
	for i, d := range docs {
		if d["kind"] != "Secret" {
			out[i] = d
			continue
		}
		c := make(map[string]any, len(d))
		for k, v := range d {
			c[k] = v
		}
		for _, field := range []string{"data", "stringData"} {
			m, ok := d[field].(map[string]any)
			if !ok {
				continue
			}
			r := make(map[string]any, len(m))
			for k := range m {
				r[k] = "<redacted>"
			}
			c[field] = r
		}
		out[i] = c
	}
	return out
}

func TestRenderMatchesKubectl(t *testing.T) {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Skip("找不到 kubectl，略過比對")
	}
	root, err := repo.Root(".")
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := repo.Services(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) == 0 {
		t.Fatal("找不到任何服務")
	}
	for _, svc := range svcs {
		tracks, err := repo.Tracks(root, svc)
		if err != nil {
			t.Fatal(err)
		}
		for _, track := range tracks {
			t.Run(svc+"/"+track, func(t *testing.T) {
				got, err := render.Render(root, svc, track)
				if err != nil {
					t.Fatalf("Render: %v", err)
				}
				gotDocs, err := render.Docs(got)
				if err != nil {
					t.Fatalf("Docs(got): %v", err)
				}
				out, err := exec.Command(kubectl, "kustomize", root+"/services/"+svc+"/"+track).Output()
				if err != nil {
					t.Fatalf("kubectl kustomize: %v", err)
				}
				wantDocs, err := render.Docs(out)
				if err != nil {
					t.Fatalf("Docs(want): %v", err)
				}
				sortDocs(gotDocs)
				sortDocs(wantDocs)
				if !reflect.DeepEqual(gotDocs, wantDocs) {
					// 部署主機上 secret.env 是真值：印出前遮蔽 Secret 的值
					g, _ := yaml.Marshal(redactSecrets(gotDocs))
					w, _ := yaml.Marshal(redactSecrets(wantDocs))
					t.Errorf("渲染不一致\n--- got ---\n%s\n--- want ---\n%s", g, w)
				}
			})
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	docs := []map[string]any{
		{"kind": "Secret", "metadata": map[string]any{"name": "s"},
			"data":       map[string]any{"PASS": "ZmFrZS1leGFtcGxl"},
			"stringData": map[string]any{"TOKEN": "fake-example"}},
		{"kind": "ConfigMap", "data": map[string]any{"K": "v"}},
	}
	got := redactSecrets(docs)
	b, _ := yaml.Marshal(got)
	if strings.Contains(string(b), "ZmFrZS1leGFtcGxl") || strings.Contains(string(b), "fake-example") {
		t.Fatalf("Secret 的值應遮蔽：\n%s", b)
	}
	for _, k := range []string{"PASS: <redacted>", "TOKEN: <redacted>", "K: v", "name: s"} {
		if !strings.Contains(string(b), k) {
			t.Errorf("缺 %q：\n%s", k, b)
		}
	}
	if docs[0]["data"].(map[string]any)["PASS"] != "ZmFrZS1leGFtcGxl" {
		t.Error("不應改到原本的文件")
	}
}
