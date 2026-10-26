package render_test

import (
	"os/exec"
	"reflect"
	"sort"
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
					g, _ := yaml.Marshal(gotDocs)
					w, _ := yaml.Marshal(wantDocs)
					t.Errorf("渲染不一致\n--- got ---\n%s\n--- want ---\n%s", g, w)
				}
			})
		}
	}
}
