package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildTree(t *testing.T) {
	files := []string{
		"root.go",
		"cmd/main.go",
		"internal/app/app.go",
		"internal/app/config.go",
		"internal/util/util.go",
	}

	tree := buildTree(files)

	if tree.Path != "." {
		t.Errorf("expected root path '.', got %s", tree.Path)
	}
	if len(tree.Files) != 1 || tree.Files[0] != "root.go" {
		t.Errorf("expected root to have 'root.go', got %v", tree.Files)
	}

	cmdNode := tree.Children["cmd"]
	if cmdNode == nil || cmdNode.Path != "cmd" {
		t.Errorf("expected cmd node with path 'cmd', got %v", cmdNode)
	}
	if len(cmdNode.Files) != 1 || cmdNode.Files[0] != "cmd/main.go" {
		t.Errorf("expected cmd node to have 'cmd/main.go', got %v", cmdNode.Files)
	}

	internalNode := tree.Children["internal"]
	appNode := internalNode.Children["app"]
	if appNode == nil || appNode.Path != "internal/app" {
		t.Errorf("expected internal/app node, got %v", appNode)
	}
	if len(appNode.Files) != 2 {
		t.Errorf("expected 2 files in internal/app, got %d", len(appNode.Files))
	}
}

func TestDetermineAndPropagateAffected(t *testing.T) {
	tempDir := t.TempDir()
	docsDir := filepath.Join(tempDir, "docs")
	os.MkdirAll(filepath.Join(docsDir, "modules"), 0755)

	// Create dummy module files so os.Stat doesn't fail
	for _, m := range []string{".", "cmd", "internal", "internal/app"} {
		safeName := toSafeMarkdownFilename(m)
		fullPath := filepath.Join(docsDir, "modules", safeName)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		os.WriteFile(fullPath, []byte("test"), 0644)
	}

	tree := &DirNode{
		Path: ".",
		Children: map[string]*DirNode{
			"cmd": {
				Path:  "cmd",
				Files: []string{"cmd/main.go"},
			},
			"internal": {
				Path: "internal",
				Children: map[string]*DirNode{
					"app": {
						Path:  "internal/app",
						Files: []string{"internal/app/app.go"},
					},
				},
			},
		},
	}

	cache := newEmptyCache()
	// All modules exist in cache except we will simulate changes
	cache.Modules["."] = "root module"
	cache.Modules["cmd"] = "cmd module"
	cache.Modules["internal"] = "internal module"
	cache.Modules["internal/app"] = "app module"

	changes := []FileChange{
		{Path: "internal/app/app.go", Status: StatusModified},
	}

	affected := determineAffected(tree, tempDir, "docs", cache, changes)

	if !affected["internal/app"] {
		t.Errorf("expected internal/app to be affected")
	}
	if affected["cmd"] {
		t.Errorf("expected cmd not to be affected")
	}

	propagated := propagateAffected(tree, affected)

	if !propagated["."] {
		t.Errorf("expected root to be affected after propagation")
	}
	if !propagated["internal"] {
		t.Errorf("expected internal to be affected after propagation")
	}
	if !propagated["internal/app"] {
		t.Errorf("expected internal/app to be affected after propagation")
	}
	if propagated["cmd"] {
		t.Errorf("expected cmd not to be affected after propagation")
	}
}
