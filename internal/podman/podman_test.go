package podman

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/run"
	"github.com/flysangel/appctl/internal/setting"
)

const manifest = `
apiVersion: v1
kind: Secret
metadata:
  name: fb
type: Opaque
data:
  PASS: %PASS%
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: fb-conf
data:
%CONF%
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: filebrowser
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: filebrowser
        image: quay.io/flysangel/fbs:base-v1.0.3
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 4000
        env:
        - name: PW
          valueFrom:
            secretKeyRef:
              name: fb
              key: PASS
        volumeMounts:
        - name: conf
          mountPath: /etc/fb
        - name: data
          mountPath: /srv
%SIDECAR%
      volumes:
      - name: conf
        configMap:
          name: fb-conf
      - name: data
        persistentVolumeClaim:
          claimName: filebrowser-data
%EXTRAVOL%
`

// sidecar 是第二個容器，掛第二個 PVC；用來測 manifest 拿掉容器與 PVC 的情況。
const sidecar = `      - name: sidecar
        image: quay.io/flysangel/fbs:base-v1.0.3
        volumeMounts:
        - name: extra
          mountPath: /extra`

const extraVol = `      - name: extra
        persistentVolumeClaim:
          claimName: filebrowser-extra`

// bundle 翻譯測試用的 filebrowser。conf 是 ConfigMap 的 data 區塊（每行縮兩格）。
func bundle(t *testing.T, pass, conf string) *quadlet.Bundle {
	t.Helper()
	return bundleWith(t, pass, conf, false)
}

// bundleWith 的 withSidecar 為 true 時，多一個 sidecar 容器與 filebrowser-extra 卷。
func bundleWith(t *testing.T, pass, conf string, withSidecar bool) *quadlet.Bundle {
	t.Helper()
	sc, ev := "", ""
	if withSidecar {
		sc, ev = sidecar, extraVol
	}
	y := strings.ReplaceAll(manifest, "%SIDECAR%", sc)
	y = strings.ReplaceAll(y, "%EXTRAVOL%", ev)
	y = strings.ReplaceAll(y, "%PASS%", base64.StdEncoding.EncodeToString([]byte(pass)))
	y = strings.ReplaceAll(y, "%CONF%", conf)
	d, err := render.Docs([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	s, err := setting.Parse(strings.NewReader("[ports]\nfilebrowser = 4000:4000\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := quadlet.Translate("filebrowser", d, s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const conf1 = `  a.conf: "A"
  b.conf: "B"`

// newFake 回一個「全新主機」的 Fake：沒有舊 pod、pod 服務沒在跑、卷刪得掉、不在 git repo 裡。
func newFake() *run.Fake {
	f := &run.Fake{}
	f.On("systemctl is-active", "inactive", 3)
	f.On("podman pod exists", "", 1)
	f.On("podman volume exists", "", 1)
	f.On("podman secret exists", "", 1)
	f.On("git", "", 128)
	return f
}

type harness struct {
	env  Env
	fake *run.Fake
	out  *bytes.Buffer
	dirs [3]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{fake: newFake(), out: &bytes.Buffer{}}
	h.dirs = [3]string{filepath.Join(root, "quadlet"), filepath.Join(root, "systemd"), filepath.Join(root, "state")}
	h.env = Env{
		Runner:     h.fake,
		UnitDir:    h.dirs[0],
		SystemdDir: h.dirs[1],
		StateDir:   h.dirs[2],
		Track:      "podman",
		IsRoot:     func() bool { return true },
		Stdin:      nil,
		Out:        h.out,
		Err:        h.out,
		Confirm:    func(*os.File, io.Writer, string, string) error { return nil },
	}
	return h
}

// reset 換一個新的 Fake，模擬下一次執行。
func (h *harness) reset() {
	h.fake = newFake()
	h.env.Runner = h.fake
	h.out.Reset()
}

func ctx() context.Context { return context.Background() }

func TestApplyRequiresRoot(t *testing.T) {
	h := newHarness(t)
	h.env.IsRoot = func() bool { return false }
	err := Apply(ctx(), h.env, bundle(t, "pass", conf1))
	if err == nil || !strings.Contains(err.Error(), "sudo appctl") {
		t.Fatalf("不是 root 應回錯並提到 sudo appctl：%v", err)
	}
	if len(h.fake.Calls) != 0 {
		t.Errorf("不是 root 時不應呼叫外部指令：%v", h.fake.Lines())
	}
	for _, fn := range []func(context.Context, Env) error{
		func(c context.Context, e Env) error { return Stop(c, e, "filebrowser") },
		func(c context.Context, e Env) error { return Delete(c, e, bundle(t, "pass", conf1)) },
	} {
		if err := fn(ctx(), h.env); err == nil || !strings.Contains(err.Error(), "sudo appctl") {
			t.Errorf("Stop、Delete 不是 root 也應回錯：%v", err)
		}
	}
	if len(h.fake.Calls) != 0 {
		t.Errorf("不是 root 時不應呼叫外部指令：%v", h.fake.Lines())
	}
}

func assertNothingWritten(t *testing.T, h *harness) {
	t.Helper()
	for _, d := range h.dirs {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("被拒絕時不應建立 %s", d)
		}
	}
	if h.fake.Called("systemctl daemon-reload") || h.fake.Called("podman secret") {
		t.Errorf("被拒絕時不應動主機：%v", h.fake.Lines())
	}
}

func TestApplyRefusesLegacyPod(t *testing.T) {
	h := newHarness(t)
	h.fake.On("podman pod exists filebrowser-pod", "", 0)
	err := Apply(ctx(), h.env, bundle(t, "pass", conf1))
	if err == nil || !strings.Contains(err.Error(), "sudo podman pod rm -f filebrowser-pod") {
		t.Fatalf("舊 pod 存在時應回錯並給清除指令：%v", err)
	}
	assertNothingWritten(t, h)
}

func TestApplyRefusesLegacyPodNamedSvc(t *testing.T) {
	// pod 服務沒在跑，卻有一個叫 filebrowser 的 pod：是 kube play 留下的。
	h := newHarness(t)
	h.fake.On("podman pod exists filebrowser", "", 0)
	h.fake.On("podman pod exists filebrowser-pod", "", 1)
	err := Apply(ctx(), h.env, bundle(t, "pass", conf1))
	if err == nil || !strings.Contains(err.Error(), "sudo podman pod rm -f filebrowser") {
		t.Fatalf("舊 pod 存在時應回錯並給清除指令：%v", err)
	}
	assertNothingWritten(t, h)
}

func TestApplyAllowsOwnRunningPod(t *testing.T) {
	// pod 服務在跑，叫 filebrowser 的 pod 是我們自己的，可以再 apply。
	h := newHarness(t)
	h.fake.On("systemctl is-active", "active", 0)
	h.fake.On("podman pod exists filebrowser", "", 0)
	h.fake.On("podman pod exists filebrowser-pod", "", 1)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatalf("自己的 pod 在跑時應允許再 apply：%v", err)
	}
}

func TestApplyTwiceIsIdempotent(t *testing.T) {
	h := newHarness(t)
	b := bundle(t, "pass", conf1)
	if err := Apply(ctx(), h.env, b); err != nil {
		t.Fatalf("第一次 apply：%v", err)
	}
	// 檔案寫到 Env 指定的目錄，ConfigFiles 從 /var/lib/appctl 換到 StateDir。
	for _, p := range []string{
		filepath.Join(h.dirs[0], "filebrowser-filebrowser.container"),
		filepath.Join(h.dirs[0], "filebrowser-data.volume"),
		filepath.Join(h.dirs[1], "filebrowser-pod.service"),
		filepath.Join(h.dirs[2], "filebrowser", "configmap", "fb-conf", "a.conf"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("應寫出 %s：%v", p, err)
		}
	}
	// secret 先 rm 再 create，值走 stdin，不進命令列。
	var rmAt, createAt = -1, -1
	for i, c := range h.fake.Calls {
		switch c.Line() {
		case "podman secret rm filebrowser_fb_PASS":
			rmAt = i
		case "podman secret create filebrowser_fb_PASS -":
			createAt = i
			if string(c.Stdin) != "pass" {
				t.Errorf("secret 值應走 stdin：%q", c.Stdin)
			}
		}
		if strings.Contains(c.Line(), "pass") && !strings.Contains(c.Line(), "filebrowser") {
			t.Errorf("secret 值不應出現在命令列：%s", c.Line())
		}
	}
	if rmAt < 0 || createAt < 0 || rmAt > createAt {
		t.Errorf("secret 應先 rm 再 create：%v", h.fake.Lines())
	}
	if !h.fake.Called("systemctl daemon-reload") {
		t.Errorf("應 daemon-reload：%v", h.fake.Lines())
	}
	if !h.fake.Called("systemctl restart filebrowser-pod.service filebrowser-filebrowser.service") {
		t.Errorf("第一次 apply 全部是新檔，應 restart：%v", h.fake.Lines())
	}

	h.reset()
	if err := Apply(ctx(), h.env, b); err != nil {
		t.Fatalf("第二次 apply：%v", err)
	}
	if h.fake.Called("systemctl restart") {
		t.Errorf("內容沒變，第二次不應 restart：%v", h.fake.Lines())
	}
	if !h.fake.Called("systemctl start filebrowser-pod.service filebrowser-filebrowser.service") {
		t.Errorf("內容沒變時應 start：%v", h.fake.Lines())
	}
	if !h.fake.Called("systemctl daemon-reload") {
		t.Errorf("第二次也應 daemon-reload：%v", h.fake.Lines())
	}
}

func TestApplyRestartsOnSecretChange(t *testing.T) {
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	h.reset()
	if err := Apply(ctx(), h.env, bundle(t, "pass2", conf1)); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Called("systemctl restart filebrowser-filebrowser.service") {
		t.Errorf("secret 值變了，用到它的容器應 restart：%v", h.fake.Lines())
	}
	if h.fake.Called("systemctl restart filebrowser-pod.service") {
		t.Errorf("pod 服務沒變，不應 restart：%v", h.fake.Lines())
	}
	if !h.fake.Called("systemctl start filebrowser-pod.service") {
		t.Errorf("pod 服務沒變，應 start：%v", h.fake.Lines())
	}
	hash, err := os.ReadFile(filepath.Join(h.dirs[2], "filebrowser", "deployed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(hash, []byte("pass")) {
		t.Errorf("hash 檔不應含明文")
	}
}

func TestApplyRestartsOnConfigChange(t *testing.T) {
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	h.reset()
	if err := Apply(ctx(), h.env, bundle(t, "pass", `  a.conf: "A2"`)); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Called("systemctl restart filebrowser-filebrowser.service") {
		t.Errorf("ConfigMap 變了，掛它的容器應 restart：%v", h.fake.Lines())
	}
	dir := filepath.Join(h.dirs[2], "filebrowser", "configmap", "fb-conf")
	if got, _ := os.ReadFile(filepath.Join(dir, "a.conf")); string(got) != "A2" {
		t.Errorf("a.conf 應更新成 A2：%q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.conf")); err == nil {
		t.Errorf("ConfigMap 刪掉的鍵，舊檔應清掉")
	}
}

func demoRepo(t *testing.T, h *harness, content string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "services", "filebrowser", "secret")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	k := "secretGenerator:\n  - name: fb\n    envs:\n      - secret.env\n    files:\n      - conf.yaml=config.yaml\n"
	for name, c := range map[string]string{"kustomization.yaml": k, "secret.env": content, "config.yaml": "real"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.env.RepoRoot = root
	return root
}

func TestApplyDemoNotice(t *testing.T) {
	h := newHarness(t)
	root := demoRepo(t, h, "PASS=demo\n")
	h.fake.On("git -C "+root+" show HEAD:services/filebrowser/secret/secret.env", "PASS=demo\n", 0)
	h.fake.On("git -C "+root+" show HEAD:services/filebrowser/secret/config.yaml", "demo", 0)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	want := "Info: services/filebrowser/secret/secret.env 還是版控裡的 demo 值，實際環境要改成真實值"
	if !strings.Contains(h.out.String(), want) {
		t.Errorf("應印 %q，實際：%q", want, h.out.String())
	}
	if strings.Contains(h.out.String(), "config.yaml 還是") {
		t.Errorf("config.yaml 已改成真值，不應提醒：%q", h.out.String())
	}
}

func TestApplyDemoNoticeSilentWithoutGit(t *testing.T) {
	h := newHarness(t)
	demoRepo(t, h, "PASS=demo\n")
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), "demo 值") {
		t.Errorf("git 失敗時應安靜跳過：%q", h.out.String())
	}
}

func TestStopKeepsVolumes(t *testing.T) {
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	h.reset()
	if err := Stop(ctx(), h.env, "filebrowser"); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Called("systemctl stop filebrowser-filebrowser.service filebrowser-pod.service") {
		t.Errorf("應停容器單元與 pod 服務：%v", h.fake.Lines())
	}
	if h.fake.Called("podman volume") {
		t.Errorf("stop 不應動卷：%v", h.fake.Lines())
	}
}

func TestStopWithoutRecordStopsPodService(t *testing.T) {
	h := newHarness(t)
	if err := Stop(ctx(), h.env, "filebrowser"); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Called("systemctl stop filebrowser-pod.service") {
		t.Errorf("沒有部署紀錄時應停 pod 服務：%v", h.fake.Lines())
	}
}

func TestDeleteAbortsWithoutTTY(t *testing.T) {
	h := newHarness(t)
	h.env.Confirm = nil // 用真的 confirm.Ask
	p := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(p, []byte("filebrowser\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h.env.Stdin = f
	if err := Delete(ctx(), h.env, bundle(t, "pass", conf1)); err == nil {
		t.Fatal("stdin 不是終端機時應回錯")
	}
	if h.fake.Called("systemctl stop") || h.fake.Called("podman volume rm") {
		t.Errorf("沒確認不應動主機：%v", h.fake.Lines())
	}
}

func TestDeleteAbortsOnConfirmError(t *testing.T) {
	h := newHarness(t)
	h.env.Confirm = func(*os.File, io.Writer, string, string) error { return errors.New("已取消") }
	if err := Delete(ctx(), h.env, bundle(t, "pass", conf1)); err == nil {
		t.Fatal("確認失敗應回錯")
	}
	if len(h.fake.Calls) != 0 {
		t.Errorf("沒確認不應呼叫外部指令：%v", h.fake.Lines())
	}
}

func TestDeleteReportsRemainingVolume(t *testing.T) {
	h := newHarness(t)
	h.fake.On("podman volume exists filebrowser-data", "", 0)
	err := Delete(ctx(), h.env, bundle(t, "pass", conf1))
	if err == nil || !strings.Contains(err.Error(), "filebrowser-data") ||
		!strings.Contains(err.Error(), "sudo podman volume rm filebrowser-data") {
		t.Fatalf("卷還在時應回錯並列出卷名與手動指令：%v", err)
	}
}

func TestDeleteRemovesEverything(t *testing.T) {
	h := newHarness(t)
	b := bundle(t, "pass", conf1)
	if err := Apply(ctx(), h.env, b); err != nil {
		t.Fatal(err)
	}
	h.reset()
	h.fake.On("systemctl stop", "", 5) // 停不掉（例如單元沒載入）不擋刪除
	if err := Delete(ctx(), h.env, b); err != nil {
		t.Fatalf("delete：%v", err)
	}
	for _, p := range []string{
		filepath.Join(h.dirs[0], "filebrowser-filebrowser.container"),
		filepath.Join(h.dirs[0], "filebrowser-data.volume"),
		filepath.Join(h.dirs[1], "filebrowser-pod.service"),
		filepath.Join(h.dirs[2], "filebrowser"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("delete 後 %s 應該不在", p)
		}
	}
	want := []string{
		"systemctl stop filebrowser-filebrowser.service filebrowser-pod.service",
		"podman secret rm filebrowser_fb_PASS",
		"systemctl daemon-reload",
		"podman volume rm filebrowser-data",
		"podman volume exists filebrowser-data",
	}
	lines := h.fake.Lines()
	i := 0
	for _, l := range lines {
		if i < len(want) && l == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Errorf("呼叫順序應含 %v，實際：%v", want, lines)
	}
}

func TestApplyRetriesRestartAfterFailure(t *testing.T) {
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	h.reset()
	h.fake.On("systemctl restart", "", 1)
	if err := Apply(ctx(), h.env, bundle(t, "pass2", conf1)); err == nil {
		t.Fatal("restart 失敗時 apply 應回錯")
	}
	h.reset()
	if err := Apply(ctx(), h.env, bundle(t, "pass2", conf1)); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Called("systemctl restart filebrowser-filebrowser.service") {
		t.Errorf("上次 restart 失敗，這次應再 restart：%v", h.fake.Lines())
	}
}

func TestApplyRemovesDroppedContainer(t *testing.T) {
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundleWith(t, "pass", conf1, true)); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(h.dirs[0], "filebrowser-sidecar.container")
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("第一次 apply 應寫出 sidecar：%v", err)
	}
	h.reset()
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Called("systemctl stop filebrowser-sidecar.service") {
		t.Errorf("manifest 拿掉的容器應停掉：%v", h.fake.Lines())
	}
	if _, err := os.Stat(unit); err == nil {
		t.Errorf("manifest 拿掉的容器單元檔應刪掉")
	}
	if h.fake.Called("podman volume rm") {
		t.Errorf("apply 不應刪卷：%v", h.fake.Lines())
	}
	rec, _ := os.ReadFile(filepath.Join(h.dirs[2], "filebrowser", "deployed.json"))
	if !bytes.Contains(rec, []byte("filebrowser-extra")) {
		t.Errorf("拿掉的卷應留在紀錄裡，delete 才刪得到：%s", rec)
	}
}

func TestApplyFailsWhenDroppedContainerKeepsRunning(t *testing.T) {
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundleWith(t, "pass", conf1, true)); err != nil {
		t.Fatal(err)
	}
	h.reset()
	h.fake.On("systemctl stop", "", 1)
	h.fake.On("systemctl is-active --quiet filebrowser-sidecar.service", "", 0)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err == nil {
		t.Fatal("拿掉的容器停不掉時應回錯")
	}
	if _, err := os.Stat(filepath.Join(h.dirs[0], "filebrowser-sidecar.container")); err != nil {
		t.Errorf("停不掉時不應刪單元檔：%v", err)
	}
}

func TestDeleteCoversVolumesFromEarlierApply(t *testing.T) {
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundleWith(t, "pass", conf1, true)); err != nil {
		t.Fatal(err)
	}
	h.reset()
	b := bundle(t, "pass", conf1)
	if err := Apply(ctx(), h.env, b); err != nil {
		t.Fatal(err)
	}
	h.reset()
	if err := Delete(ctx(), h.env, b); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"filebrowser-data", "filebrowser-extra"} {
		if !h.fake.Called("podman volume rm "+v) || !h.fake.Called("podman volume exists "+v) {
			t.Errorf("卷 %s 應 rm 並確認：%v", v, h.fake.Lines())
		}
	}
}

func TestDeleteCoversUnitsFromEarlierApply(t *testing.T) {
	// 上次 apply 有 sidecar，這次 delete 用的 Bundle 沒有：單元檔與卷都要清。
	h := newHarness(t)
	if err := Apply(ctx(), h.env, bundleWith(t, "pass", conf1, true)); err != nil {
		t.Fatal(err)
	}
	h.reset()
	if err := Delete(ctx(), h.env, bundle(t, "pass", conf1)); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Called("systemctl stop") || !strings.Contains(strings.Join(h.fake.Lines(), "\n"), "filebrowser-sidecar.service") {
		t.Errorf("紀錄裡的 sidecar 應一起停：%v", h.fake.Lines())
	}
	for _, p := range []string{
		filepath.Join(h.dirs[0], "filebrowser-sidecar.container"),
		filepath.Join(h.dirs[0], "filebrowser-extra.volume"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s 應刪掉", p)
		}
	}
	if !h.fake.Called("podman volume rm filebrowser-extra") {
		t.Errorf("紀錄裡的卷應刪：%v", h.fake.Lines())
	}
}

func TestDeleteRefusesWhenUnitKeepsRunning(t *testing.T) {
	h := newHarness(t)
	b := bundle(t, "pass", conf1)
	if err := Apply(ctx(), h.env, b); err != nil {
		t.Fatal(err)
	}
	h.reset()
	h.fake.On("systemctl stop", "", 1)
	h.fake.On("systemctl is-active", "active", 0)
	if err := Delete(ctx(), h.env, b); err == nil {
		t.Fatal("單元停不掉時應回錯")
	}
	if _, err := os.Stat(filepath.Join(h.dirs[0], "filebrowser-filebrowser.container")); err != nil {
		t.Errorf("停不掉時不應刪單元檔：%v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dirs[2], "filebrowser", "deployed.json")); err != nil {
		t.Errorf("停不掉時不應刪紀錄：%v", err)
	}
	if h.fake.Called("podman secret rm") || h.fake.Called("podman volume rm") {
		t.Errorf("停不掉時不應刪 secret 或卷：%v", h.fake.Lines())
	}
}

func TestDeleteReportsRemainingSecret(t *testing.T) {
	h := newHarness(t)
	h.fake.On("podman secret exists filebrowser_fb_PASS", "", 0)
	err := Delete(ctx(), h.env, bundle(t, "pass", conf1))
	if err == nil || !strings.Contains(err.Error(), "filebrowser_fb_PASS") {
		t.Fatalf("secret 還在時應回錯並列出名字：%v", err)
	}
}

func TestExistsQueryErrorIsNotFalse(t *testing.T) {
	h := newHarness(t)
	h.fake.On("podman volume exists filebrowser-data", "", 125)
	if err := Delete(ctx(), h.env, bundle(t, "pass", conf1)); err == nil {
		t.Error("podman volume exists 回 125 時 delete 應回錯，不能當成卷不在")
	}
	h = newHarness(t)
	h.fake.On("podman pod exists", "", 125)
	if err := Apply(ctx(), h.env, bundle(t, "pass", conf1)); err == nil {
		t.Error("podman pod exists 回 125 時 apply 應回錯，不能當成 pod 不在")
	}
	assertNothingWritten(t, h)
}
