package setting_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flysangel/appctl/internal/setting"
)

func parse(t *testing.T, s string) (*setting.Setting, error) {
	t.Helper()
	return setting.Parse(strings.NewReader(s))
}

func TestParsePorts(t *testing.T) {
	s, err := parse(t, "# 註解\n[ports]\ngateway = 9005:9005\na = 1:2, 3:4/udp\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []setting.PortMap{
		{ContainerName: "gateway", HostPort: 9005, ContainerPort: 9005, Protocol: "TCP"},
		{ContainerName: "a", HostPort: 1, ContainerPort: 2, Protocol: "TCP"},
		{ContainerName: "a", HostPort: 3, ContainerPort: 4, Protocol: "UDP"},
	}
	if !reflect.DeepEqual(s.Ports, want) {
		t.Fatalf("got %+v want %+v", s.Ports, want)
	}
}

func TestParseRejectsUnknownSection(t *testing.T) {
	_, err := parse(t, "[env]\nA = 1\n")
	if err == nil || !strings.Contains(err.Error(), "env") {
		t.Fatalf("want error mentioning env, got %v", err)
	}
}

func TestParseRejectsUnknownKeyFormat(t *testing.T) {
	for _, in := range []string{"[ports]\ngateway = 9005\n", "[ports]\ngateway = a:b\n", "[ports]\ngateway = 1:2/sctp\n", "[ports]\ngateway = 0:80\n", "[ports]\ngateway = 1:70000\n", "gateway = 1:2\n"} {
		if _, err := parse(t, in); err == nil {
			t.Errorf("want error for %q", in)
		}
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "services", "x", "podman")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := setting.Load(root, "x", "podman"); err == nil {
		t.Fatal("want error when file missing")
	}
	if err := os.WriteFile(filepath.Join(dir, "setting.conf"), []byte("[ports]\nweb = 80:8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := setting.Load(root, "x", "podman")
	if err != nil || len(s.Ports) != 1 {
		t.Fatalf("got %+v %v", s, err)
	}
}

func deployment(containers ...map[string]any) []map[string]any {
	cs := make([]any, len(containers))
	for i, c := range containers {
		cs[i] = c
	}
	return []map[string]any{{
		"kind": "Deployment",
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": cs}}},
	}}
}

func container(name string, ports ...map[string]any) map[string]any {
	ps := make([]any, len(ports))
	for i, p := range ports {
		ps[i] = p
	}
	return map[string]any{"name": name, "ports": ps}
}

func TestValidateOK(t *testing.T) {
	docs := deployment(container("web", map[string]any{"containerPort": float64(8080)}))
	s := &setting.Setting{Ports: []setting.PortMap{{ContainerName: "web", HostPort: 80, ContainerPort: 8080, Protocol: "TCP"}}}
	if err := setting.Validate(s, docs); err != nil {
		t.Fatal(err)
	}
	pod := []map[string]any{{"kind": "Pod", "spec": map[string]any{"containers": []any{
		map[string]any{"name": "web", "ports": []any{map[string]any{"containerPort": 53, "protocol": "UDP"}}},
	}}}}
	s = &setting.Setting{Ports: []setting.PortMap{{ContainerName: "web", HostPort: 53, ContainerPort: 53, Protocol: "UDP"}}}
	if err := setting.Validate(s, pod); err != nil {
		t.Fatal(err)
	}
}

func TestValidateUnknownContainer(t *testing.T) {
	docs := deployment(container("web", map[string]any{"containerPort": 80}))
	s := &setting.Setting{Ports: []setting.PortMap{{ContainerName: "nope", HostPort: 80, ContainerPort: 80, Protocol: "TCP"}}}
	if err := setting.Validate(s, docs); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
}

func TestValidatePortNotDeclared(t *testing.T) {
	docs := deployment(container("web", map[string]any{"containerPort": 80}))
	s := &setting.Setting{Ports: []setting.PortMap{{ContainerName: "web", HostPort: 81, ContainerPort: 81, Protocol: "TCP"}}}
	if err := setting.Validate(s, docs); err == nil {
		t.Fatal("want error")
	}
	// 協定不符也算沒宣告
	s = &setting.Setting{Ports: []setting.PortMap{{ContainerName: "web", HostPort: 80, ContainerPort: 80, Protocol: "UDP"}}}
	if err := setting.Validate(s, docs); err == nil {
		t.Fatal("want error for protocol mismatch")
	}
}

func TestValidateMissingPorts(t *testing.T) {
	docs := deployment(container("web", map[string]any{"containerPort": 80}))
	if err := setting.Validate(&setting.Setting{}, docs); err == nil {
		t.Fatal("want error")
	}
}

func TestHostPort(t *testing.T) {
	s, err := parse(t, "[ports]\nweb = 18080:8080\nweb = 53:53/udp\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.HostPort("web", 8080); err != nil || got != 18080 {
		t.Fatalf("got %d, %v", got, err)
	}
	for _, c := range []struct {
		name string
		port int
	}{{"web", 53}, {"web", 9999}, {"nope", 8080}} {
		if _, err := s.HostPort(c.name, c.port); err == nil || !strings.Contains(err.Error(), c.name) {
			t.Errorf("%s:%d 應回錯並帶容器名：%v", c.name, c.port, err)
		}
	}
}
