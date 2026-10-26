// appctl 是 app-services 的部署控制工具。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/flysangel/appctl/internal/bundle"
	"github.com/flysangel/appctl/internal/check"
	"github.com/flysangel/appctl/internal/gmm"
	"github.com/flysangel/appctl/internal/k8s"
	"github.com/flysangel/appctl/internal/podman"
	"github.com/flysangel/appctl/internal/quay"
	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
	"github.com/flysangel/appctl/internal/run"
	"github.com/flysangel/appctl/internal/smoke"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch verb := os.Args[1]; verb {
	case "render":
		err = runRender(os.Args[2:])
	case "apply", "stop", "delete":
		err = runVerb(verb, os.Args[2:])
	case "smoke":
		err = runSmoke(os.Args[2:])
	case "check":
		err = runCheck(os.Args[2:])
	case "check-images":
		err = runCheckImages(os.Args[2:])
	case "check-secret":
		err = runCheckSecret(os.Args[2:])
	case "gmm":
		err = runGmm(os.Args[2:])
	case "quay":
		err = runQuay(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "Error: 未知的子命令 %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法：appctl render|apply|stop|delete <svc> --track <軌>\n      sudo appctl smoke [--allow-guarded] <svc>...\n      appctl check\n      appctl check-images <svc>\n      appctl check-secret <svc>\n      appctl gmm seed|check --track <軌>\n      appctl quay init --track <軌>")
}

// parseSvcTrack 解析 <svc> --track <軌>，<svc> 在 flag 前後都可以。
func parseSvcTrack(verb string, args []string) (svc, track string, err error) {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	t := fs.String("track", "", "軌目錄名，例如 k8s 或 podman")
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		svc, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if svc == "" && fs.NArg() == 1 {
		svc = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return "", "", fmt.Errorf("參數過多")
	}
	if svc == "" || *t == "" {
		return "", "", fmt.Errorf("用法：appctl %s <svc> --track <軌>", verb)
	}
	return svc, *t, nil
}

func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return repo.Root(cwd)
}

func runRender(args []string) error {
	svc, track, err := parseSvcTrack("render", args)
	if err != nil {
		return err
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	out, err := render.Render(root, svc, track)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}

// runVerb 處理 apply、stop、delete。名字以 podman 開頭的軌走 Quadlet。
func runVerb(verb string, args []string) error {
	svc, track, err := parseSvcTrack(verb, args)
	if err != nil {
		return err
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	tracks, err := repo.Tracks(root, svc)
	if err != nil {
		return err
	}
	if !slices.Contains(tracks, track) {
		return fmt.Errorf("%s 沒有 %s 軌（有：%s）", svc, track, strings.Join(tracks, "、"))
	}
	ctx := context.Background()
	if track == "k8s" {
		return runK8s(ctx, verb, root, svc, track)
	}
	if !strings.HasPrefix(track, "podman") {
		return fmt.Errorf("%s 軌的 %s 還沒實作", track, verb)
	}
	env := podman.DefaultEnv(root, track)
	if verb == "stop" {
		if err := podman.Stop(ctx, env, svc); err != nil {
			return err
		}
		fmt.Printf("Success: 已停止 %s（%s），資料卷保留\n", svc, track)
		return nil
	}
	b, err := bundle.Translate(root, svc, track)
	if err != nil {
		return err
	}
	if verb == "apply" {
		if err := podman.Apply(ctx, env, b); err != nil {
			return err
		}
		fmt.Printf("Success: 已部署 %s（%s）\n", svc, track)
		return nil
	}
	if err := podman.Delete(ctx, env, b); err != nil {
		return err
	}
	fmt.Printf("Success: 已刪除 %s（%s），含資料卷\n", svc, track)
	return nil
}

// runSmoke 跑 podman 軌煙測。最後的 delete 會刪卷，要 root 與 tty。
func runSmoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	allow := fs.Bool("allow-guarded", false, "連 ncb、kadm、xadm-* 也跑（delete 會刪它們的卷）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	return smoke.Run(context.Background(), podman.DefaultEnv(root, "podman"), root, fs.Args(), *allow)
}

// runK8s 處理 k8s 軌：渲染後經 stdin 交給 kubectl，不要求 root。
func runK8s(ctx context.Context, verb, root, svc, track string) error {
	if verb == "stop" {
		return k8s.Stop(svc)
	}
	manifest, err := render.Render(root, svc, track)
	if err != nil {
		return err
	}
	if verb == "apply" {
		if err := k8s.Apply(ctx, run.Exec{}, os.Stdout, manifest); err != nil {
			return err
		}
		fmt.Printf("Success: 已部署 %s（%s）\n", svc, track)
		return nil
	}
	env := k8s.Env{Runner: run.Exec{}, Track: track, Stdin: os.Stdin, Out: os.Stdout}
	if err := k8s.Delete(ctx, env, svc, manifest); err != nil {
		return err
	}
	fmt.Printf("Success: 已刪除 %s（%s），含資料卷\n", svc, track)
	return nil
}

// runCheck 跑離線靜態檢查：manifest 的 image 與服務層規則。
func runCheck(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("用法：appctl check")
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	var problems []string
	if errs := check.Manifests(root); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "Error: manifest 的 image 檢查未通過：")
		printIndented(errs)
		problems = append(problems, errs...)
	}
	errs, infos := check.Services(root)
	for _, i := range infos {
		fmt.Println("Info:", i)
	}
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "Error: 服務層檢查未通過：")
		printIndented(errs)
		problems = append(problems, errs...)
	}
	if errs := check.Secrets(root); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "Error: 機密一致性檢查未通過：")
		printIndented(errs)
		problems = append(problems, errs...)
	}
	if len(problems) > 0 {
		return fmt.Errorf("靜態檢查未通過，共 %d 項", len(problems))
	}
	fmt.Println("Success: services/ 的 image、服務層與機密一致性檢查都通過")
	return nil
}

func printIndented(lines []string) {
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, "  "+l)
	}
}

func singleSvc(verb string, args []string) (string, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("用法：appctl %s <svc>", verb)
	}
	return args[0], nil
}

// runCheckImages 渲染服務的每一軌，確認每個 image 都還在來源 registry（需要網路與 skopeo）。
func runCheckImages(args []string) error {
	svc, err := singleSvc("check-images", args)
	if err != nil {
		return err
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	tracks, err := repo.Tracks(root, svc)
	if err != nil {
		return err
	}
	var docs []map[string]any
	for _, track := range tracks {
		out, err := render.Render(root, svc, track)
		if err != nil {
			return err
		}
		d, err := render.Docs(out)
		if err != nil {
			return err
		}
		docs = append(docs, d...)
	}
	errs := check.ImagesExist(context.Background(), run.Exec{}, docs)
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "Error: %s 的 image 檢查未通過：\n", svc)
		printIndented(errs)
		return fmt.Errorf("%s 有 %d 個 image 有問題", svc, len(errs))
	}
	fmt.Printf("Success: %s 所有 image 皆為完整參照且存在於來源 registry\n", svc)
	return nil
}

// runCheckSecret 比對機密真值在各檔之間是否一致，只印鍵名。
func runCheckSecret(args []string) error {
	svc, err := singleSvc("check-secret", args)
	if err != nil {
		return err
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	if errs := check.Secret(root, svc); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "Error: "+e)
		}
		return fmt.Errorf("%s 的機密檢查未通過", svc)
	}
	fmt.Printf("Success: %s 的機密在各檔之間一致\n", svc)
	return nil
}

// runGmm 處理 gmm seed 與 gmm check。k8s 軌先 port-forward svc/gmm，用完就停；
// podman 軌讀 sync.conf 的 GUACAMOLE_SERVER。一般使用者身分就能跑。
func runGmm(args []string) error {
	const use = "用法：appctl gmm seed|check --track <軌>"
	if len(args) == 0 || (args[0] != "seed" && args[0] != "check") {
		return errors.New(use)
	}
	sub := args[0]
	fs := flag.NewFlagSet("gmm "+sub, flag.ContinueOnError)
	t := fs.String("track", "", "軌目錄名，例如 k8s 或 podman")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *t == "" {
		return errors.New(use)
	}
	track := *t
	root, err := repoRoot()
	if err != nil {
		return err
	}
	tracks, err := repo.Tracks(root, "gmm")
	if err != nil {
		return err
	}
	if !slices.Contains(tracks, track) {
		return fmt.Errorf("gmm 沒有 %s 軌（有：%s）", track, strings.Join(tracks, "、"))
	}
	ctx := context.Background()
	opts := gmm.Options{Root: root, Stdin: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	switch {
	case track == "k8s":
		port, stop, err := k8s.PortForward(ctx, run.Exec{}, "svc/gmm", 8080)
		if err != nil {
			return err
		}
		defer stop()
		opts.Server = fmt.Sprintf("http://127.0.0.1:%d/guacamole", port)
		fmt.Printf("Info: 已用 kubectl port-forward 連到 svc/gmm，本機埠 %d\n", port)
	case strings.HasPrefix(track, "podman"):
	default:
		return fmt.Errorf("%s 軌的 gmm %s 還沒實作", track, sub)
	}
	if sub == "seed" {
		return gmm.Seed(ctx, opts)
	}
	return gmm.Check(ctx, opts)
}

// runQuay 處理 quay init。k8s 軌先 port-forward svc/quay，用完就停；
// podman 軌連 setting.conf 發佈的主機埠 8080。一般使用者身分就能跑。
func runQuay(args []string) error {
	const use = "用法：appctl quay init --track <軌>"
	if len(args) == 0 || args[0] != "init" {
		return errors.New(use)
	}
	fs := flag.NewFlagSet("quay init", flag.ContinueOnError)
	t := fs.String("track", "", "軌目錄名，例如 k8s 或 podman")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *t == "" {
		return errors.New(use)
	}
	track := *t
	root, err := repoRoot()
	if err != nil {
		return err
	}
	tracks, err := repo.Tracks(root, "quay")
	if err != nil {
		return err
	}
	if !slices.Contains(tracks, track) {
		return fmt.Errorf("quay 沒有 %s 軌（有：%s）", track, strings.Join(tracks, "、"))
	}
	ctx := context.Background()
	opts := quay.Options{Root: root, Out: os.Stdout}
	switch {
	case track == "k8s":
		port, stop, err := k8s.PortForward(ctx, run.Exec{}, "svc/quay", 8080)
		if err != nil {
			return err
		}
		defer stop()
		opts.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
		fmt.Printf("Info: 已用 kubectl port-forward 連到 svc/quay，本機埠 %d\n", port)
	case strings.HasPrefix(track, "podman"):
		opts.BaseURL = "http://127.0.0.1:8080"
	default:
		return fmt.Errorf("%s 軌的 quay init 還沒實作", track)
	}
	return quay.Init(ctx, opts)
}
