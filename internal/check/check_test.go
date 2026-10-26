package check

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/flysangel/appctl/internal/run"
)

const digest = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func joined(l []string) string { return strings.Join(l, "\n") }

// 對應 tests/test_check_manifests.py::test_image_problem
func TestImageProblem(t *testing.T) {
	tests := []struct {
		ref  string
		good bool
	}{
		{"quay.io/flysangel/grafana/grafana:13.1.1", true},
		{"grafana-grafana", false},
		{"quay.io/flysangel/x/y", false},
		{"quay.io/flysangel/x/y:latest", false},
		{"localhost:5000/foo", false},
		{"localhost:5000/foo:1", true},
		{"quay.io/flysangel/minio/minio@" + digest, true},
		{"quay.io/flysangel/minio/minio:v1@" + digest, true},
		{"quay.io/x/y@sha256:abc", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := ImageProblem(tt.ref)
			if tt.good && got != "" {
				t.Fatalf("應合格，得到 %q", got)
			}
			if !tt.good && got == "" {
				t.Fatal("應不合格，卻回空字串")
			}
		})
	}
}

func pod(image, key string) string {
	return "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\nspec:\n  " + key + ":\n  - name: c\n    image: " + image + "\n"
}

// 對應 test_cli_rejects_images_key、test_cli_skips_secret_dir、test_cli_finds_nested_image
func TestManifests(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr []string // 空表示要通過；否則每個字串都要出現在輸出
	}{
		{
			name: "TestCliRejectsImagesKey",
			files: map[string]string{
				"services/a/base/pod.yaml":           pod("quay.io/x/y:1", "containers"),
				"services/a/base/kustomization.yaml": "resources:\n  - pod.yaml\nimages:\n  - name: y\n    newTag: \"1\"\n",
			},
			wantErr: []string{"images:"},
		},
		{
			name: "TestCliSkipsSecretDir",
			files: map[string]string{
				"services/a/base/pod.yaml":      pod("quay.io/x/y:1", "containers"),
				"services/a/secret/config.yaml": "image: bad\n",
			},
		},
		{
			name:    "TestCliFindsNestedImage",
			files:   map[string]string{"services/a/base/pod.yaml": pod("shortname", "initContainers")},
			wantErr: []string{"services/a/base/pod.yaml", "shortname"},
		},
		{
			name:    "YAML 解析失敗要報錯",
			files:   map[string]string{"services/a/base/pod.yaml": "a: [\n"},
			wantErr: []string{"services/a/base/pod.yaml", "YAML 解析失敗"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range tt.files {
				write(t, root, rel, body)
			}
			out := joined(Manifests(root))
			if len(tt.wantErr) == 0 && out != "" {
				t.Fatalf("應通過，得到：%s", out)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(out, w) {
					t.Fatalf("輸出缺 %q：%s", w, out)
				}
			}
		})
	}
}

const deployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: NAME
spec:
  selector:
    matchLabels:
      app: NAME
  template:
    metadata:
      labels:
        app: NAME
    spec:
      containers:
      - name: c
        image: example.io/x:1
        ports:
        - containerPort: 8080
`

const crb = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: NAME
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: NAME
  namespace: default
`

const lb = `apiVersion: v1
kind: Service
metadata:
  name: NAME
spec:
  type: LoadBalancer
  selector:
    app: NAME
  ports:
  - port: 22
`

type svcOpts struct {
	podman       map[string]int // 軌名 -> 主機埠
	k8s          bool
	ns           string
	secret       bool
	clusterAdmin bool
	broken       bool
}

func service(t *testing.T, root, name string, o svcOpts) {
	t.Helper()
	if o.ns == "" {
		o.ns = "default"
	}
	sub := func(s string) string { return strings.ReplaceAll(s, "NAME", name) }
	write(t, root, "services/"+name+"/base/deployment.yaml", sub(deployment))
	res := "resources:\n  - deployment.yaml\n"
	if o.broken {
		res = "resources:\n  - missing.yaml\n"
	}
	write(t, root, "services/"+name+"/base/kustomization.yaml", res)
	extra := ""
	if o.secret {
		extra = "  - ../secret\n"
		write(t, root, "services/"+name+"/secret/kustomization.yaml",
			"generatorOptions:\n  disableNameSuffixHash: true\nsecretGenerator:\n  - name: "+name+"\n    envs:\n      - secret.env\n")
		write(t, root, "services/"+name+"/secret/secret.env", "PASS=changeme\n")
	}
	for track, port := range o.podman {
		write(t, root, "services/"+name+"/"+track+"/kustomization.yaml",
			"namespace: "+o.ns+"\nresources:\n  - ../base\n"+extra)
		write(t, root, "services/"+name+"/"+track+"/setting.conf",
			"[ports]\nc = "+strconv.Itoa(port)+":8080\n")
	}
	if o.k8s {
		res := "  - ../base\n" + extra
		if o.clusterAdmin {
			write(t, root, "services/"+name+"/k8s/crb.yaml", sub(crb))
			write(t, root, "services/"+name+"/k8s/service.yaml", sub(lb))
			res += "  - crb.yaml\n  - service.yaml\n"
		}
		write(t, root, "services/"+name+"/k8s/kustomization.yaml", "namespace: "+o.ns+"\nresources:\n"+res)
	}
}

// 對應 tests/test_check_services.py 的各案例
func TestServices(t *testing.T) {
	tests := []struct {
		name      string
		build     func(t *testing.T, root string)
		groups    string
		wantErr   []string // 空表示不得有錯誤
		wantInfo  []string
		wantNoErr bool
	}{
		{
			name: "TestConflictNotInGroupFails",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman": 8080}})
				service(t, r, "b", svcOpts{podman: map[string]int{"podman": 8080}})
			},
			groups:  "",
			wantErr: []string{"a", "b", "8080"},
		},
		{
			name: "TestConflictInGroupPasses",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman": 8080}})
				service(t, r, "b", svcOpts{podman: map[string]int{"podman": 8080}})
			},
			groups: "# 互斥\na b\n",
		},
		{
			name: "TestConflictSubsetOfLargerGroupPasses",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman": 8080}})
				service(t, r, "b", svcOpts{podman: map[string]int{"podman": 8080}})
			},
			groups: "a b c\n",
		},
		{
			name: "TestDifferentHostsDoNotConflict",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman": 8080}})
				service(t, r, "b", svcOpts{podman: map[string]int{"podman-h1": 8080}})
			},
		},
		{
			name: "TestPodmanHostSuffixSameHostConflicts",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman-h1": 8080}})
				service(t, r, "b", svcOpts{podman: map[string]int{"podman-h1": 8080}})
			},
			wantErr: []string{"主機 h1", "8080/TCP"},
		},
		{
			name:    "TestNamespaceMustBeDefault",
			build:   func(t *testing.T, r string) { service(t, r, "a", svcOpts{k8s: true, ns: "x"}) },
			wantErr: []string{"namespace"},
		},
		{
			name:     "TestClusterAdminLbWithoutSourceRangesWarns",
			build:    func(t *testing.T, r string) { service(t, r, "a", svcOpts{k8s: true, clusterAdmin: true}) },
			wantInfo: []string{"loadBalancerSourceRanges"},
		},
		{
			name: "TestRenderFailureReportsAndContinues",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman": 9000}, broken: true})
				service(t, r, "b", svcOpts{podman: map[string]int{"podman": 8080}})
				service(t, r, "c", svcOpts{podman: map[string]int{"podman": 8080}})
			},
			groups:  "",
			wantErr: []string{"a/podman", "8080"},
		},
		{
			name: "TestSecretServiceRendersWithTrackedSecret",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman": 8080}, k8s: true, secret: true})
			},
		},
		{
			name: "缺 setting.conf 的 podman 軌要報錯",
			build: func(t *testing.T, r string) {
				service(t, r, "a", svcOpts{podman: map[string]int{"podman": 8080}})
				if err := os.Remove(filepath.Join(r, "services/a/podman/setting.conf")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: []string{"a/podman", "setting.conf"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.build(t, root)
			if tt.groups != "" {
				write(t, root, "config/port-groups.conf", tt.groups)
			}
			errs, infos := Services(root)
			e, i := joined(errs), joined(infos)
			if len(tt.wantErr) == 0 && e != "" {
				t.Fatalf("不應有錯誤：%s", e)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(e, w) {
					t.Fatalf("錯誤缺 %q：%s", w, e)
				}
			}
			for _, w := range tt.wantInfo {
				if !strings.Contains(i, w) {
					t.Fatalf("info 缺 %q：%s", w, i)
				}
			}
			if len(tt.wantInfo) > 0 && e != "" {
				t.Fatalf("info 不應變成錯誤：%s", e)
			}
		})
	}
}

const (
	dbPW    = "fake-db-pass"
	redisPW = "fake-redis-pass"
	otherPW = "fake-other-pass"
)

const quayDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: quay
spec:
  template:
    spec:
      containers:
      - name: quay-app
        image: example.io/quay:1
      - name: postgresql
        image: example.io/pg:1
        env:
        - name: POSTGRES_USER
          value: quayuser
        - name: POSTGRES_DB
          value: quay
`

func quayConfig(user, db, name, redis1, redis2 string) string {
	return "BUILDLOGS_REDIS:\n    host: 127.0.0.1\n    password: \"" + redis1 + "\"\n    port: 6379\n" +
		"DB_URI: postgresql://" + user + ":" + db + "@127.0.0.1:5432/" + name + "\n" +
		"USER_EVENTS_REDIS:\n    host: 127.0.0.1\n    password: \"" + redis2 + "\"\n    port: 6379\n"
}

// 對應 tests/test_check_secret.py 的各案例
func TestSecret(t *testing.T) {
	goodEnv := "# 註解\nDB_PASSWORD=" + dbPW + "\nREDIS_PASSWORD=" + redisPW + "\n"
	good := quayConfig("quayuser", dbPW, "quay", redisPW, redisPW)
	tests := []struct {
		name    string
		env     string
		conf    string // 空字串表示不建檔
		wantErr string // 空表示要通過
	}{
		{"TestConsistentPasses", goodEnv, good, ""},
		{"TestDbPasswordMismatchFails", goodEnv, quayConfig("quayuser", otherPW, "quay", redisPW, redisPW), "DB_PASSWORD"},
		{"TestRedisMismatchFails", goodEnv, quayConfig("quayuser", dbPW, "quay", redisPW, otherPW), "USER_EVENTS_REDIS"},
		{"TestDbUserMismatchFails", goodEnv, quayConfig("someone", dbPW, "quay", redisPW, redisPW), "POSTGRES_USER"},
		{"TestDbNameMismatchFails", goodEnv, quayConfig("quayuser", dbPW, "other", redisPW, redisPW), "POSTGRES_DB"},
		{"TestMissingFileFails", goodEnv, "", "config.yaml"},
		{"密碼含保留字元仍可比對", "DB_PASSWORD=fake@pa:ss\nREDIS_PASSWORD=" + redisPW + "\n",
			quayConfig("quayuser", "fake@pa:ss", "quay", redisPW, redisPW), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "services/quay/base/deployment.yaml", quayDeployment)
			write(t, root, "services/quay/secret/secret.env", tt.env)
			if tt.conf != "" {
				write(t, root, "services/quay/secret/config.yaml", tt.conf)
			}
			out := joined(Secret(root, "quay"))
			for _, v := range []string{dbPW, redisPW, otherPW} {
				if strings.Contains(out, v) {
					t.Fatalf("輸出含機密值：%s", out)
				}
			}
			if tt.wantErr == "" && out != "" {
				t.Fatalf("應通過，得到：%s", out)
			}
			if tt.wantErr != "" && !strings.Contains(out, tt.wantErr) {
				t.Fatalf("輸出缺 %q：%s", tt.wantErr, out)
			}
		})
	}
}

// 對應 test_unknown_service_fails
func TestUnknownServiceFails(t *testing.T) {
	out := joined(Secret(t.TempDir(), "grafana"))
	if !strings.Contains(out, "沒有一致性規則") {
		t.Fatalf("輸出：%s", out)
	}
}

// 取代 bin/assert-images.sh 的守衛：短名、零個 image、registry 查不到
func TestImagesExist(t *testing.T) {
	docs := func(images ...string) []map[string]any {
		var cs []any
		for _, i := range images {
			cs = append(cs, map[string]any{"name": "c", "image": i})
		}
		return []map[string]any{{"kind": "Pod", "spec": map[string]any{"containers": cs}}}
	}
	t.Run("全部存在", func(t *testing.T) {
		f := &run.Fake{}
		errs := ImagesExist(context.Background(), f, docs("example.io/a:1", "example.io/b:2", "example.io/a:1"))
		if len(errs) != 0 {
			t.Fatalf("不應有錯誤：%v", errs)
		}
		want := []string{
			"skopeo inspect --raw docker://example.io/a:1",
			"skopeo inspect --raw docker://example.io/b:2",
		}
		if got := f.Lines(); joined(got) != joined(want) {
			t.Fatalf("呼叫 %v，要 %v", got, want)
		}
	})
	t.Run("registry 查不到", func(t *testing.T) {
		f := &run.Fake{}
		f.On("skopeo inspect --raw docker://example.io/b:2", "", 1)
		errs := ImagesExist(context.Background(), f, docs("example.io/a:1", "example.io/b:2"))
		if len(errs) != 1 || !strings.Contains(errs[0], "example.io/b:2") {
			t.Fatalf("錯誤：%v", errs)
		}
	})
	t.Run("短名不呼叫 skopeo", func(t *testing.T) {
		f := &run.Fake{}
		errs := ImagesExist(context.Background(), f, docs("demo"))
		if len(errs) != 1 || !strings.Contains(errs[0], "demo") {
			t.Fatalf("錯誤：%v", errs)
		}
		if len(f.Calls) != 0 {
			t.Fatalf("不該呼叫：%v", f.Lines())
		}
	})
	t.Run("零個 image 算失敗", func(t *testing.T) {
		errs := ImagesExist(context.Background(), &run.Fake{}, nil)
		if len(errs) != 1 || !strings.Contains(errs[0], "零個 image") {
			t.Fatalf("錯誤：%v", errs)
		}
	})
}
