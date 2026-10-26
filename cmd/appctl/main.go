// appctl 是 app-services 的部署控制工具。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/flysangel/appctl/internal/podman"
	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
	"github.com/flysangel/appctl/internal/setting"
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
	fmt.Fprintln(os.Stderr, "用法：appctl render|apply|stop|delete <svc> --track <軌>")
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
	if !strings.HasPrefix(track, "podman") {
		return fmt.Errorf("%s 軌的 %s 還沒實作", track, verb)
	}
	ctx := context.Background()
	env := podman.DefaultEnv(root, track)
	if verb == "stop" {
		if err := podman.Stop(ctx, env, svc); err != nil {
			return err
		}
		fmt.Printf("Success: 已停止 %s（%s），資料卷保留\n", svc, track)
		return nil
	}
	b, err := translate(root, svc, track)
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

func translate(root, svc, track string) (*quadlet.Bundle, error) {
	out, err := render.Render(root, svc, track)
	if err != nil {
		return nil, err
	}
	docs, err := render.Docs(out)
	if err != nil {
		return nil, err
	}
	s, err := setting.Load(root, svc, track)
	if err != nil {
		return nil, err
	}
	return quadlet.Translate(svc, docs, s)
}
