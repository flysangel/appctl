package render_test

import (
	"testing"

	"github.com/flysangel/appctl/internal/render"
)

func TestDocsEmptyInput(t *testing.T) {
	docs, err := render.Docs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Fatalf("want 0 docs, got %d", len(docs))
	}
}

func TestDocsSkipsEmptyDocuments(t *testing.T) {
	in := "---\n---\na: 1\n---\n\n---\n# only comment\n---\nb: 2\n"
	docs, err := render.Docs([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d: %v", len(docs), docs)
	}
	if docs[0]["a"] != float64(1) || docs[1]["b"] != float64(2) {
		t.Fatalf("unexpected docs: %v", docs)
	}
}

func TestDocsKeepsPEMBlockInOneDocument(t *testing.T) {
	in := "apiVersion: v1\nkind: ConfigMap\ndata:\n  cert: |\n    -----BEGIN CERTIFICATE-----\n    abc\n    -----END CERTIFICATE-----\n"
	docs, err := render.Docs([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
}

func TestDocsKeepsDashLineInsideQuotedScalar(t *testing.T) {
	in := "kind: ConfigMap\ndata:\n  cert: \"x\n-----BEGIN CERTIFICATE-----\n  y\"\n---\nkind: Pod\n"
	docs, err := render.Docs([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d: %v", len(docs), docs)
	}
}
