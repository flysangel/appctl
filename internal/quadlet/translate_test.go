package quadlet_test

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
	"github.com/flysangel/appctl/internal/setting"
)

var update = flag.Bool("update", false, "重新產生 testdata 的 golden 檔")

func docs(t *testing.T, y string) []map[string]any {
	t.Helper()
	d, err := render.Docs([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func ports(t *testing.T, conf string) *setting.Setting {
	t.Helper()
	s, err := setting.Parse(strings.NewReader(conf))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func unit(t *testing.T, b *quadlet.Bundle, name string) string {
	t.Helper()
	for _, f := range b.Units {
		if f.Name == name {
			return string(f.Content)
		}
	}
	var names []string
	for _, f := range b.Units {
		names = append(names, f.Name)
	}
	t.Fatalf("找不到單元 %s，現有：%v", name, names)
	return ""
}

func TestTranslateFilebrowserGolden(t *testing.T) {
	root, err := repo.Root(".")
	if err != nil {
		t.Fatal(err)
	}
	out, err := render.Render(root, "filebrowser", "podman")
	if err != nil {
		t.Fatal(err)
	}
	d, err := render.Docs(out)
	if err != nil {
		t.Fatal(err)
	}
	s, err := setting.Load(root, "filebrowser", "podman")
	if err != nil {
		t.Fatal(err)
	}
	b, err := quadlet.Translate("filebrowser", d, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.ConfigFiles) != 0 || len(b.Secrets) != 0 {
		t.Fatalf("filebrowser 不該有 ConfigFiles 或 Secrets：%+v", b)
	}
	if strings.Join(b.Volumes, ",") != "filebrowser-data" {
		t.Fatalf("Volumes = %v", b.Volumes)
	}
	dir := filepath.Join("testdata", "filebrowser.golden")
	got := map[string]string{}
	for _, f := range b.Units {
		got[f.Name] = string(f.Content)
	}
	if *update {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for n, c := range got {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range entries {
		want = append(want, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if got[e.Name()] != string(b) {
			t.Errorf("%s 不一致\n--- got\n%s\n--- want\n%s", e.Name(), got[e.Name()], b)
		}
	}
	var gotNames []string
	for n := range got {
		gotNames = append(gotNames, n)
	}
	sort.Strings(gotNames)
	if strings.Join(gotNames, ",") != strings.Join(want, ",") {
		t.Fatalf("單元清單 = %v，golden = %v", gotNames, want)
	}
	if !strings.Contains(got["filebrowser-filebrowser.container"], "$$(jq") {
		t.Fatal("golden 的 $(jq 沒有跳脫成 $$(jq")
	}
}

func TestEscape(t *testing.T) {
	if got := quadlet.Escape("date +%T $HOME"); got != "date +%%T $$HOME" {
		t.Fatalf("Escape = %q", got)
	}
}

const oneContainer = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      containers:
      - name: app
        image: quay.io/x/app:v1
        ports:
        - containerPort: 80
`

func TestRejectsUnknownField(t *testing.T) {
	y := oneContainer + `        resources:
          limits:
            memory: 1Gi
        workingDir: /srv
`
	_, err := quadlet.Translate("demo", docs(t, y), ports(t, "[ports]\napp = 8080:80\n"))
	if err == nil {
		t.Fatal("應該回錯")
	}
	for _, want := range []string{"resources.limits.memory", "workingDir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("錯誤訊息沒列出 %s：%v", want, err)
		}
	}
}

func TestRejectsHostPort(t *testing.T) {
	y := oneContainer + "          hostPort: 80\n"
	_, err := quadlet.Translate("demo", docs(t, y), ports(t, "[ports]\napp = 8080:80\n"))
	if err == nil || !strings.Contains(err.Error(), "hostPort") {
		t.Fatalf("hostPort 應該被白名單擋下：%v", err)
	}
}

func TestRejectsK8sVarExpansion(t *testing.T) {
	s := ports(t, "[ports]\napp = 8080:80\n")
	bad := oneContainer + "        args: [\"echo $(MY_VAR)\"]\n"
	if _, err := quadlet.Translate("demo", docs(t, bad), s); err == nil || !strings.Contains(err.Error(), "$(MY_VAR)") {
		t.Fatalf("$(MY_VAR) 應該回錯：%v", err)
	}
	ok := oneContainer + "        args: [\"mkdir -p $(jq -r .root x)\"]\n"
	if _, err := quadlet.Translate("demo", docs(t, ok), s); err != nil {
		t.Fatalf("$(jq ...) 不該回錯：%v", err)
	}
}

func TestExecQuotesArgs(t *testing.T) {
	y := oneContainer + `        command: [/bin/sh, -c]
        args:
        - "echo \"a b\" $(jq -r .x f) 100%"
        - plain
`
	b, err := quadlet.Translate("demo", docs(t, y), ports(t, "[ports]\napp = 8080:80\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := unit(t, b, "demo-app.container")
	wantExec := `Exec="echo \"a b\" $$(jq -r .x f) 100%%" plain` + "\n"
	if !strings.Contains(c, wantExec) {
		t.Fatalf("Exec= 行不對，想要 %q\n%s", wantExec, c)
	}
	wantEP := `PodmanArgs="--entrypoint=[\"/bin/sh\",\"-c\"]"` + "\n"
	if !strings.Contains(c, wantEP) {
		t.Fatalf("entrypoint 行不對，想要 %q\n%s", wantEP, c)
	}
	if strings.Contains(c, "\nEntrypoint=") {
		t.Fatal("podman 4.9.3 沒有 Entrypoint= 鍵")
	}
}

func TestExecMultilineArg(t *testing.T) {
	y := oneContainer + `        args:
        - |
          until x; do \
            sleep 2; done
`
	b, err := quadlet.Translate("demo", docs(t, y), ports(t, "[ports]\napp = 8080:80\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := unit(t, b, "demo-app.container")
	want := `Exec="until x; do \\\n  sleep 2; done\n"` + "\n"
	if !strings.Contains(c, want) {
		t.Fatalf("多行參數要寫成一行並跳脫，想要 %q\n%s", want, c)
	}
}

func TestMultiContainerSharesPod(t *testing.T) {
	y := `
apiVersion: v1
kind: Pod
metadata:
  name: svc
spec:
  restartPolicy: OnFailure
  containers:
  - name: web
    image: quay.io/x/web:v1
    ports:
    - containerPort: 80
    - containerPort: 53
      protocol: UDP
  - name: db
    image: quay.io/x/db:v1
`
	b, err := quadlet.Translate("svc", docs(t, y), ports(t, "[ports]\nweb = 8080:80, 5353:53/udp\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"svc-web.container", "svc-db.container"} {
		c := unit(t, b, n)
		for _, want := range []string{
			"PodmanArgs=--pod=svc\n",
			"Requires=svc-pod.service\n",
			"After=svc-pod.service\n",
			"Restart=on-failure\n",
			"ContainerName=" + strings.TrimSuffix(n, ".container") + "\n",
		} {
			if !strings.Contains(c, want) {
				t.Errorf("%s 少了 %q\n%s", n, want, c)
			}
		}
	}
	pod := unit(t, b, "svc-pod.service")
	want := "ExecStart=/usr/bin/podman pod create --replace --name svc -p 8080:80/tcp -p 5353:53/udp\n"
	if !strings.Contains(pod, want) {
		t.Fatalf("pod 服務的 -p 不對，想要 %q\n%s", want, pod)
	}
	for _, f := range b.Units {
		wantDir := quadlet.DirQuadlet
		if f.Name == "svc-pod.service" {
			wantDir = quadlet.DirSystemd
		}
		if f.Dir != wantDir {
			t.Errorf("%s 的 Dir = %q，想要 %q", f.Name, f.Dir, wantDir)
		}
	}
}

func TestStatefulSetVolumeName(t *testing.T) {
	y := `
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: minio
spec:
  serviceName: minio
  replicas: 1
  template:
    spec:
      containers:
      - name: minio
        image: quay.io/x/minio:v1
        ports:
        - containerPort: 9000
        volumeMounts:
        - name: minio-data
          mountPath: /data
  volumeClaimTemplates:
  - metadata:
      name: minio-data
    spec:
      accessModes: [ReadWriteOnce]
      resources:
        requests:
          storage: 10Gi
`
	b, err := quadlet.Translate("minio", docs(t, y), ports(t, "[ports]\nminio = 9000:9000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := unit(t, b, "minio-data.volume"); !strings.Contains(v, "VolumeName=minio-data\n") {
		t.Fatalf("volume 單元：\n%s", v)
	}
	if c := unit(t, b, "minio-minio.container"); !strings.Contains(c, "Volume=minio-data.volume:/data\n") {
		t.Fatalf("容器沒掛卷：\n%s", c)
	}
	if strings.Join(b.Volumes, ",") != "minio-data" {
		t.Fatalf("Volumes = %v", b.Volumes)
	}
}

func TestConfigMapSecretAndTmpfs(t *testing.T) {
	y := `
apiVersion: v1
kind: Secret
metadata:
  name: app
type: Opaque
data:
  PASS: cGFzcw==
  conf.yaml: Y29uZg==
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-conf
data:
  a.conf: "A"
  b.conf: "B"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      serviceAccountName: app
      containers:
      - name: web
        image: quay.io/x/web:v1
        imagePullPolicy: IfNotPresent
        securityContext:
          privileged: true
        ports:
        - containerPort: 80
        env:
        - name: TZ
          value: Asia/Taipei
        - name: PW
          valueFrom:
            secretKeyRef:
              name: app
              key: PASS
        volumeMounts:
        - name: conf
          mountPath: /etc/app
        - name: one
          mountPath: /etc/one.conf
          subPath: b.conf
        - name: sec
          mountPath: /run/conf
        - name: shm
          mountPath: /dev/shm
        - name: cache
          mountPath: /cache
      volumes:
      - name: conf
        configMap:
          name: app-conf
      - name: one
        configMap:
          name: app-conf
      - name: sec
        secret:
          secretName: app
      - name: shm
        emptyDir:
          medium: Memory
          sizeLimit: 2Gi
      - name: cache
        emptyDir: {}
`
	b, err := quadlet.Translate("app", docs(t, y), ports(t, "[ports]\nweb = 8080:80\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := unit(t, b, "app-web.container")
	for _, want := range []string{
		"Environment=TZ=Asia/Taipei\n",
		"Secret=app_app_PASS,type=env,target=PW\n",
		"Secret=app_app_PASS,type=mount,target=/run/conf/PASS\n",
		"Secret=app_app_conf.yaml,type=mount,target=/run/conf/conf.yaml\n",
		"Volume=/var/lib/appctl/app/configmap/app-conf:/etc/app:ro\n",
		"Volume=/var/lib/appctl/app/configmap/app-conf/b.conf:/etc/one.conf:ro\n",
		"Tmpfs=/dev/shm:size=2147483648\n",
		"Volume=/cache\n",
		"PodmanArgs=--privileged\n",
		"Pull=missing\n",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("少了 %q\n%s", want, c)
		}
	}
	var cf []string
	for _, f := range b.ConfigFiles {
		cf = append(cf, f.Name+"="+string(f.Content))
	}
	if strings.Join(cf, ",") != "/var/lib/appctl/app/configmap/app-conf/a.conf=A,/var/lib/appctl/app/configmap/app-conf/b.conf=B" {
		t.Fatalf("ConfigFiles = %v", cf)
	}
	if len(b.Secrets) != 1 || b.Secrets[0].Name != "app" || string(b.Secrets[0].Data["PASS"]) != "pass" || string(b.Secrets[0].Data["conf.yaml"]) != "conf" {
		t.Fatalf("Secrets = %+v", b.Secrets)
	}
	if quadlet.PodmanSecretName("app", "app", "PASS") != "app_app_PASS" {
		t.Fatal("PodmanSecretName")
	}
	if strings.Contains(c, "serviceAccount") {
		t.Fatal("serviceAccountName 不該翻譯")
	}
}

func TestRejectsMissingSecretKey(t *testing.T) {
	y := oneContainer + `        env:
        - name: PW
          valueFrom:
            secretKeyRef:
              name: nope
              key: PASS
`
	_, err := quadlet.Translate("demo", docs(t, y), ports(t, "[ports]\napp = 8080:80\n"))
	if err == nil || !strings.Contains(err.Error(), "Secret nope") || !strings.Contains(err.Error(), "PW") {
		t.Fatalf("錯誤訊息要列出缺的 secret 名與變數名：%v", err)
	}
	y2 := `
apiVersion: v1
kind: Secret
metadata:
  name: s
data:
  OTHER: eA==
---
` + y[1:]
	y2 = strings.Replace(y2, "name: nope", "name: s", 1)
	_, err = quadlet.Translate("demo", docs(t, y2), ports(t, "[ports]\napp = 8080:80\n"))
	if err == nil || !strings.Contains(err.Error(), "沒有鍵 PASS") {
		t.Fatalf("錯誤訊息要列出缺的鍵：%v", err)
	}
}

func TestRejectsEmptyAndSemicolonArgs(t *testing.T) {
	s := ports(t, "[ports]\napp = 8080:80\n")
	for _, tc := range []struct{ extra, want string }{
		{"        args: [a, \"\", b]\n", "空字串"},
		{"        command: [sh, \"\"]\n", "空字串"},
		{"        args: [find, ., -exec, rm, \"{}\", \";\"]\n", ";"},
	} {
		_, err := quadlet.Translate("demo", docs(t, oneContainer+tc.extra), s)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q 應該回錯並提到 %q：%v", tc.extra, tc.want, err)
		}
	}
	// command 整個包在 --entrypoint 的 JSON 裡，單獨的 ; 不會被 systemd 當成分隔
	if _, err := quadlet.Translate("demo", docs(t, oneContainer+"        command: [find, ., -exec, rm, \"{}\", \";\"]\n"), s); err != nil {
		t.Fatalf("command 裡的 ; 不該回錯：%v", err)
	}
}

func TestPodmanSecretNameNoCollision(t *testing.T) {
	a := quadlet.PodmanSecretName("svc", "a", "b-c")
	b := quadlet.PodmanSecretName("svc", "a-b", "c")
	if a == b {
		t.Fatalf("撞名：%s", a)
	}
	c := quadlet.PodmanSecretName("x-y", "z", "k")
	d := quadlet.PodmanSecretName("x", "y-z", "k")
	if c == d {
		t.Fatalf("跨服務撞名：%s", c)
	}
	y := strings.Replace(oneContainer, "kind: Deployment", "kind: Deployment", 1)
	if _, err := quadlet.Translate("my_svc", docs(t, y), ports(t, "[ports]\napp = 8080:80\n")); err == nil || !strings.Contains(err.Error(), "_") {
		t.Fatalf("服務名含 _ 會讓 secret 名有歧義，應該回錯：%v", err)
	}
}

func TestRejectsContainerNamedPod(t *testing.T) {
	y := strings.Replace(oneContainer, "name: app", "name: pod", 1)
	_, err := quadlet.Translate("demo", docs(t, y), ports(t, "[ports]\npod = 8080:80\n"))
	if err == nil || !strings.Contains(err.Error(), "demo-pod") {
		t.Fatalf("容器名 pod 會和 pod 服務撞名，應該回錯：%v", err)
	}
}

func TestRejectsNilSetting(t *testing.T) {
	if _, err := quadlet.Translate("demo", docs(t, oneContainer), nil); err == nil || !strings.Contains(err.Error(), "setting.conf") {
		t.Fatalf("沒有 setting 應該回錯：%v", err)
	}
	if _, err := quadlet.Translate("demo", docs(t, oneContainer), &setting.Setting{}); err == nil {
		t.Fatal("空的 [ports] 應該回錯")
	}
}

func TestTranslateAllServices(t *testing.T) {
	root, err := repo.Root(".")
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := repo.Services(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, svc := range svcs {
		if _, err := os.Stat(filepath.Join(root, "services", svc, "podman")); err != nil {
			continue
		}
		n++
		t.Run(svc, func(t *testing.T) {
			out, err := render.Render(root, svc, "podman")
			if err != nil {
				t.Fatal(err)
			}
			d, err := render.Docs(out)
			if err != nil {
				t.Fatal(err)
			}
			s, err := setting.Load(root, svc, "podman")
			if err != nil {
				t.Fatal(err)
			}
			if err := setting.Validate(s, d); err != nil {
				t.Fatal(err)
			}
			b, err := quadlet.Translate(svc, d, s)
			if err != nil {
				t.Fatal(err)
			}
			if svc != "minio" {
				return
			}
			if strings.Join(b.Volumes, ",") != "minio-data" {
				t.Fatalf("Volumes = %v", b.Volumes)
			}
			var all strings.Builder
			for _, f := range b.Units {
				all.Write(f.Content)
			}
			for _, want := range []string{"VolumeName=minio-data", "Exec=server /data"} {
				if !strings.Contains(all.String(), want) {
					t.Errorf("單元缺 %q：\n%s", want, all.String())
				}
			}
		})
	}
	if n < 9 {
		t.Fatalf("只找到 %d 個有 podman 軌的服務", n)
	}
}
