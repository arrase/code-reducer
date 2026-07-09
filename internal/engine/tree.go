package engine

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/arrase/code-reducer/internal/security"
)

type FileChange struct {
	Path   string
	Status string // "Added", "Modified", "Deleted"
}

func propagateAffected(node *DirNode, affectedDirs map[string]bool) bool {
	isAffected := affectedDirs[node.Path]
	for _, child := range node.Children {
		if propagateAffected(child, affectedDirs) {
			isAffected = true
		}
	}
	if isAffected {
		affectedDirs[node.Path] = true
	}
	return isAffected
}

func determineAffected(node *DirNode, repoRoot, docsDir string, cache *MetadataCache, filteredChanges []FileChange, affectedDirs map[string]bool) {
	changedFiles := make(map[string]bool)
	for _, c := range filteredChanges {
		changedFiles[c.Path] = true
	}

	var checkNode func(n *DirNode)
	checkNode = func(n *DirNode) {
		for _, f := range n.Files {
			if changedFiles[f] {
				affectedDirs[n.Path] = true
				break
			}
		}

		safeName := ToSafeMarkdownFilename(n.Path)
		modulePath := filepath.Join(docsDir, "modules", safeName)
		absModulePath, err := security.SafeResolve(repoRoot, modulePath)
		if err == nil {
			if _, err := os.Stat(absModulePath); os.IsNotExist(err) {
				affectedDirs[n.Path] = true
			}
		}

		if cache.Modules[n.Path] == "" {
			affectedDirs[n.Path] = true
		}

		for _, child := range n.Children {
			checkNode(child)
		}
	}
	checkNode(node)
}

type DirNode struct {
	Path     string
	Files    []string
	Children map[string]*DirNode
}

func buildTree(files []string) *DirNode {
	root := &DirNode{Path: ".", Children: make(map[string]*DirNode)}
	for _, f := range files {
		d := path.Dir(f)
		if d == "." {
			root.Files = append(root.Files, f)
			continue
		}

		parts := strings.Split(d, "/")
		curr := root
		currPath := ""
		for _, part := range parts {
			if currPath == "" {
				currPath = part
			} else {
				currPath = currPath + "/" + part
			}
			if _, ok := curr.Children[part]; !ok {
				curr.Children[part] = &DirNode{Path: currPath, Children: make(map[string]*DirNode)}
			}
			curr = curr.Children[part]
		}
		curr.Files = append(curr.Files, f)
	}
	return root
}
