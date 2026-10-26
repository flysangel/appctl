package k8s

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/flysangel/appctl/internal/run"
)

const manifest = "kind: Secret\nstringData:\n  password: fake-secret-value\n"

func yes(*os.File, io.Writer, string, string) error { return nil }

func noArgHasManifest(t *testing.T, f *run.Fake) {
	t.Helper()
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if strings.Contains(a, "fake-secret-value") {
				t.Errorf("機密值不應出現在命令列：%s", c.Line())
			}
		}
	}
}

func TestApplyPipesManifest(t *testing.T) {
	f := &run.Fake{}
	f.On("kubectl apply", "deployment.apps/x configured\n", 0)
	var out bytes.Buffer
	if err := Apply(context.Background(), f, &out, []byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if out.String() != "deployment.apps/x configured\n" {
		t.Errorf("kubectl 輸出應寫到 Out：%q", out.String())
	}
	if len(f.Calls) != 1 || f.Calls[0].Line() != "kubectl apply -f -" {
		t.Fatalf("應只跑 kubectl apply -f -：%v", f.Lines())
	}
	if string(f.Calls[0].Stdin) != manifest {
		t.Errorf("stdin 應等於渲染產物：%q", f.Calls[0].Stdin)
	}
	noArgHasManifest(t, f)
}

func TestApplyReturnsKubectlError(t *testing.T) {
	f := &run.Fake{}
	f.On("kubectl apply", "", 1)
	if err := Apply(context.Background(), f, io.Discard, []byte(manifest)); err == nil {
		t.Fatal("kubectl 失敗時應回錯")
	}
}

func TestDeleteListsStatefulSetPVCs(t *testing.T) {
	f := &run.Fake{}
	f.On("kubectl delete", "deployment.apps \"minio\" deleted\n", 0)
	f.On("kubectl get pvc", "persistentvolumeclaim/minio-data0-minio-0\npersistentvolumeclaim/other-data\n", 0)
	var out bytes.Buffer
	env := Env{Runner: f, Out: &out, Confirm: yes}
	if err := Delete(context.Background(), env, "minio", []byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 || f.Calls[0].Line() != "kubectl delete -f - --ignore-not-found" {
		t.Fatalf("應先跑 kubectl delete：%v", f.Lines())
	}
	if string(f.Calls[0].Stdin) != manifest {
		t.Errorf("kubectl delete 的 stdin 應等於 manifest：%q", f.Calls[0].Stdin)
	}
	if f.Calls[1].Line() != "kubectl get pvc -n default -o name --ignore-not-found" {
		t.Errorf("列 PVC 的指令不對：%s", f.Calls[1].Line())
	}
	s := out.String()
	if !strings.Contains(s, `deployment.apps "minio" deleted`) {
		t.Errorf("kubectl delete 的輸出應寫到 Out：%q", s)
	}
	if !strings.Contains(s, "persistentvolumeclaim/minio-data0-minio-0") || !strings.Contains(s, "kubectl delete") {
		t.Errorf("應列出 PVC 與手動清除指令：%q", s)
	}
	if strings.Contains(s, "other-data") {
		t.Errorf("不該列出無關 PVC：%q", s)
	}
	noArgHasManifest(t, f)
}

func TestDeleteNoPVCNoInfo(t *testing.T) {
	f := &run.Fake{}
	var out bytes.Buffer
	if err := Delete(context.Background(), Env{Runner: f, Out: &out, Confirm: yes}, "grafana", []byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("沒有動態 PVC 時不該印東西：%q", out.String())
	}
}

func TestDeleteRefusedWhenNotConfirmed(t *testing.T) {
	f := &run.Fake{}
	no := func(*os.File, io.Writer, string, string) error { return errors.New("已取消刪除") }
	err := Delete(context.Background(), Env{Runner: f, Out: io.Discard, Confirm: no}, "grafana", []byte(manifest))
	if err == nil {
		t.Fatal("未確認應回錯")
	}
	if len(f.Calls) != 0 {
		t.Errorf("未確認時不該呼叫任何指令：%v", f.Lines())
	}
}

func TestDeleteSurvivesPVCListFailure(t *testing.T) {
	f := &run.Fake{}
	f.On("kubectl get pvc", "", 1)
	var out bytes.Buffer
	if err := Delete(context.Background(), Env{Runner: f, Out: &out, Confirm: yes}, "minio", []byte(manifest)); err != nil {
		t.Fatalf("列 PVC 失敗不該讓 delete 回錯：%v", err)
	}
	if !strings.Contains(out.String(), "Info:") || !strings.Contains(out.String(), "kubectl get pvc") {
		t.Errorf("應印 Info 請使用者自查：%q", out.String())
	}
}

func TestDeleteFailureSkipsPVCList(t *testing.T) {
	f := &run.Fake{}
	f.On("kubectl delete", "", 1)
	err := Delete(context.Background(), Env{Runner: f, Out: io.Discard, Confirm: yes}, "minio", []byte(manifest))
	if err == nil {
		t.Fatal("kubectl delete 失敗應回錯")
	}
	if f.Called("kubectl get pvc") {
		t.Errorf("delete 失敗時不該列 PVC：%v", f.Lines())
	}
}

func TestStopRejectedOnK8s(t *testing.T) {
	err := Stop("grafana")
	if err == nil || !strings.Contains(err.Error(), "kubectl scale") {
		t.Fatalf("應回錯並指向 kubectl scale：%v", err)
	}
}
