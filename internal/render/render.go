// Package render 用內嵌的 kustomize 渲染服務的軌目錄。
package render

import (
	"bytes"
	"fmt"
	"path/filepath"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

// Render 渲染 services/<svc>/<track>，回傳多文件 YAML。
func Render(root, svc, track string) ([]byte, error) {
	dir := filepath.Join(root, "services", svc, track)
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	m, err := k.Run(filesys.MakeFsOnDisk(), dir)
	if err != nil {
		return nil, fmt.Errorf("渲染 %s 失敗：%w", dir, err)
	}
	return m.AsYaml()
}

// Docs 把多文件 YAML 拆成多份文件，略過空文件。
func Docs(b []byte) ([]map[string]any, error) {
	var out []map[string]any
	for _, part := range bytes.Split(b, []byte("\n---")) {
		part = bytes.TrimPrefix(part, []byte("---"))
		if len(bytes.TrimSpace(part)) == 0 {
			continue
		}
		var d map[string]any
		if err := yaml.Unmarshal(part, &d); err != nil {
			return nil, fmt.Errorf("解析 YAML 失敗：%w", err)
		}
		if len(d) == 0 {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}
