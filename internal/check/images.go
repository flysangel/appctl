// Package check 是離線的靜態檢查：manifest 的 image、服務層規則、機密一致性，
// 另有需要網路的 registry 存在性檢查。
package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/flysangel/appctl/internal/run"
)

var digestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// ImageProblem 檢查 image 是不是完整參照。合格回空字串，不合格回原因。
func ImageProblem(ref string) string {
	host, _, ok := strings.Cut(ref, "/")
	if !ok {
		return "沒有 registry 主機"
	}
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "第一段不是 registry 主機"
	}
	digest := digestRe.MatchString(ref)
	if strings.Contains(ref, "@") && !digest {
		return "digest 格式不對（要 sha256: 加 64 個十六進位字元）"
	}
	name := digestRe.ReplaceAllString(ref, "")
	// tag 只看最後一個 / 之後：localhost:5000/foo 的 : 是埠號，不是 tag
	last := name[strings.LastIndex(name, "/")+1:]
	_, tag, hasTag := strings.Cut(last, ":")
	if hasTag && tag == "latest" {
		return "不准用 latest"
	}
	if !hasTag && !digest {
		return "沒有 tag 也沒有 digest"
	}
	return ""
}

// imagesIn 遞迴取出節點裡所有 image 欄位的字串值。
func imagesIn(node any) []string {
	var out []string
	switch n := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := n[k].(string); ok && k == "image" {
				out = append(out, s)
			} else {
				out = append(out, imagesIn(n[k])...)
			}
		}
	case []any:
		for _, item := range n {
			out = append(out, imagesIn(item)...)
		}
	}
	return out
}

// Images 回傳文件裡所有不重複的 image 參照，已排序。
func Images(docs []map[string]any) []string {
	seen := map[string]bool{}
	for _, d := range docs {
		for _, ref := range imagesIn(d) {
			seen[ref] = true
		}
	}
	out := make([]string, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// Manifests 掃 services/ 的 YAML 原始檔（略過 secret/），回傳所有問題。
// 每個 image 都要是完整參照，kustomization.yaml 不准有 images:。
func Manifests(root string) []string {
	services := filepath.Join(root, "services")
	var errs []string
	_ = filepath.WalkDir(services, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(services, path)
		parts := strings.Split(rel, string(filepath.Separator))
		if d.IsDir() {
			if len(parts) == 2 && parts[1] == "secret" {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		show, _ := filepath.Rel(root, path)
		errs = append(errs, checkYAMLFile(path, show)...)
		return nil
	})
	return errs
}

func checkYAMLFile(path, show string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("%s：讀取失敗（%v）", show, err)}
	}
	var errs []string
	dec := yamlv3.NewDecoder(bytes.NewReader(b))
	for {
		var doc any
		err := dec.Decode(&doc)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errs
			}
			return append(errs, fmt.Sprintf("%s：YAML 解析失敗", show))
		}
		if doc == nil {
			continue
		}
		if m, ok := doc.(map[string]any); ok && filepath.Base(path) == "kustomization.yaml" {
			if _, has := m["images"]; has {
				errs = append(errs, fmt.Sprintf("%s：不准有 images:，image 直接寫完整參照在 manifest", show))
			}
		}
		for _, ref := range imagesIn(doc) {
			if p := ImageProblem(ref); p != "" {
				errs = append(errs, fmt.Sprintf("%s：%s %s", show, ref, p))
			}
		}
	}
}

// ImagesExist 用 skopeo 確認每個 image 仍在來源 registry。
// 一個 image 都沒有也算失敗，避免渲染沒產出東西時假綠燈。
func ImagesExist(ctx context.Context, r run.Runner, docs []map[string]any) []string {
	refs := Images(docs)
	if len(refs) == 0 {
		return []string{"渲染產物零個 image"}
	}
	var errs []string
	for _, ref := range refs {
		if p := ImageProblem(ref); p != "" {
			errs = append(errs, fmt.Sprintf("%s %s", ref, p))
			continue
		}
		_, err := r.Run(ctx, "skopeo", []string{"inspect", "--raw", "docker://" + ref}, nil)
		if errors.Is(err, exec.ErrNotFound) {
			return []string{"skopeo not installed，無法檢查 registry"}
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s 在來源 registry 查不到：%s", ref, errReason(err)))
		}
	}
	return errs
}

// errReason 取錯誤的 stderr 第一行；沒有 stderr 時用錯誤本身。
func errReason(err error) string {
	var ee *run.ExitError
	if errors.As(err, &ee) {
		for _, l := range strings.Split(ee.Stderr, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				return l
			}
		}
		return fmt.Sprintf("結束碼 %d", ee.Code)
	}
	return err.Error()
}
