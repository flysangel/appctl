// Package render 用內嵌的 kustomize 渲染服務的軌目錄。
package render

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	yamlv3 "go.yaml.in/yaml/v3"
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
// 用 YAML 解析器切文件，內容裡以 --- 開頭的行（例如 PEM）不會被誤切。
func Docs(b []byte) ([]map[string]any, error) {
	var out []map[string]any
	dec := yamlv3.NewDecoder(bytes.NewReader(b))
	for {
		var node yamlv3.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("解析 YAML 失敗：%w", err)
		}
		raw, err := yamlv3.Marshal(&node)
		if err != nil {
			return nil, fmt.Errorf("解析 YAML 失敗：%w", err)
		}
		var d map[string]any
		if err := yaml.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("解析 YAML 失敗：%w", err)
		}
		if len(d) == 0 {
			continue
		}
		out = append(out, d)
	}
}
