// Package k8s 是 k8s 軌的動詞：渲染好的 manifest 只走 stdin 交給 kubectl，
// 機密值不進命令列。k8s 軌用一般使用者身分跑，不要求 root。
package k8s

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flysangel/appctl/internal/confirm"
	"github.com/flysangel/appctl/internal/run"
)

// Env 是 Delete 的環境。Confirm 是 nil 時用 confirm.Ask；測試換掉它。
type Env struct {
	Runner  run.Runner
	Track   string // 確認提示用；空字串視為 k8s
	Stdin   *os.File
	Out     io.Writer
	Confirm func(in *os.File, out io.Writer, svc, track string) error
}

// Apply 用 kubectl apply -f - 部署 manifest。
func Apply(ctx context.Context, r run.Runner, manifest []byte) error {
	if _, err := r.Run(ctx, "kubectl", []string{"apply", "-f", "-"}, manifest); err != nil {
		return fmt.Errorf("kubectl apply 失敗：%w", err)
	}
	return nil
}

// Delete 先確認，再 kubectl delete -f - --ignore-not-found，
// 最後列出 StatefulSet 動態建立、delete 不會移除的 PVC。
// 列 PVC 失敗只印 Info:，不讓已經成功的 delete 回錯。
func Delete(ctx context.Context, env Env, svc string, manifest []byte) error {
	track := env.Track
	if track == "" {
		track = "k8s"
	}
	ask := env.Confirm
	if ask == nil {
		ask = confirm.Ask
	}
	if err := ask(env.Stdin, env.Out, svc, track); err != nil {
		return err
	}
	if _, err := env.Runner.Run(ctx, "kubectl", []string{"delete", "-f", "-", "--ignore-not-found"}, manifest); err != nil {
		return fmt.Errorf("kubectl delete 失敗：%w", err)
	}
	out, err := env.Runner.Run(ctx, "kubectl", []string{"get", "pvc", "-n", "default", "-o", "name", "--ignore-not-found"}, nil)
	if err != nil {
		fmt.Fprintln(env.Out, "Info: 無法列出 PVC，請自行以 kubectl get pvc 確認是否有 StatefulSet 動態卷殘留")
		return nil
	}
	var found []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" && strings.Contains(l, "-"+svc+"-") {
			found = append(found, l)
		}
	}
	if len(found) > 0 {
		fmt.Fprintln(env.Out, strings.Join(found, "\n"))
		fmt.Fprintf(env.Out, "Info: 上列為 StatefulSet 動態 PVC，delete 不移除；如需清除請執行 kubectl delete -n default %s\n", strings.Join(found, " "))
	}
	return nil
}

// Stop 一律回錯：k8s 軌沒有 stop 動詞，暫停要用原生的 kubectl scale。
func Stop(svc string) error {
	return fmt.Errorf("k8s 軌沒有 stop。要暫停 %s 但保留資料，改用 kubectl scale -n default --replicas=0 deployment/<name>（minio 是 statefulset/minio；DaemonSet 沒有 replicas，只能 delete）", svc)
}
