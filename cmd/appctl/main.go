// appctl 是 app-services 的部署控制工具。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/repo"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "render":
		err = runRender(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "用法：appctl render <svc> --track <軌>")
}

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	track := fs.String("track", "", "軌目錄名，例如 k8s 或 podman")
	// 允許 <svc> 在 flag 前後都可以
	var svc string
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		svc, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if svc == "" && fs.NArg() == 1 {
		svc = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("參數過多")
	}
	if svc == "" || *track == "" {
		return fmt.Errorf("用法：appctl render <svc> --track <軌>")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := repo.Root(cwd)
	if err != nil {
		return err
	}
	out, err := render.Render(root, svc, *track)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}
