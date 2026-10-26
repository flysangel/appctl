// Package repo 定位 app-services repo 的根目錄、服務與軌。
package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Root 從 start 往上找，回傳同時含 services/ 與 Taskfile.yaml 的目錄。
func Root(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if isDir(filepath.Join(dir, "services")) && isFile(filepath.Join(dir, "Taskfile.yaml")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("從 %s 往上找不到含 services/ 與 Taskfile.yaml 的目錄", start)
		}
		dir = parent
	}
}

// Services 回傳 services/ 底下含 base/ 的服務名，已排序。
func Services(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "services"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && isDir(filepath.Join(root, "services", e.Name(), "base")) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Tracks 回傳服務的軌目錄名：k8s 與 podman 開頭的目錄，已排序。
func Tracks(root, svc string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "services", svc))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("找不到服務 %s", svc)
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && (n == "k8s" || strings.HasPrefix(n, "podman")) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
