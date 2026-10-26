// Package bundle 把服務某一軌的 kustomize 渲染產物翻成 Quadlet Bundle。
package bundle

import (
	"github.com/flysangel/appctl/internal/quadlet"
	"github.com/flysangel/appctl/internal/render"
	"github.com/flysangel/appctl/internal/setting"
)

// Translate 依序 render、解析、讀 setting.conf、翻譯。
func Translate(root, svc, track string) (*quadlet.Bundle, error) {
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
