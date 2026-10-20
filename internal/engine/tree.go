package engine

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/arrase/code-reducer/internal/tools"
)

type ChangeStatus string

const (
	StatusAdded    ChangeStatus = "Added"
	StatusModified ChangeStatus = "Modified"
	StatusDeleted  ChangeStatus = "Deleted"
)

type FileChange struct {
	Path   string
	Status ChangeStatus
}

func propagateAffected(node *DirNode, affectedDirs map[string]bool) map[string]bool {
	newAffected := make(map[string]bool)

	var propagate func(*DirNode) bool
	propagate = func(n *DirNode) bool {
		isAffected := affectedDirs[n.Path]
		for _, child := range n.Children {
			if propagate(child) {
				isAffected = true
			}
		}
		if isAffected {
			newAffected[n.Path] = true
		}
		return isAffected
	}
	propagate(node)
	return newAffected
}

func isNodeAffected(n *DirNode, changedFiles map[string]bool, repoRoot, docsDir string, cache *MetadataCache) bool {
	if cache.Modules[n.Path] == "" {
		return true
	}

	for _, f := range n.Files {
		if changedFiles[f] {
			return true
		}
	}

	modulePath := filepath.Join(docsDir, "modules", toSafeMarkdownFilename(n.Path))
	return !tools.FileExistsSafely(repoRoot, modulePath)
}

func determineAffected(node *DirNode, repoRoot, docsDir string, cache *MetadataCache, filteredChanges []FileChange) map[string]bool {
	affectedDirs := make(map[string]bool)
	changedFiles := make(map[string]bool)
	for _, c := range filteredChanges {
		changedFiles[c.Path] = true
		if c.Status == StatusDeleted {
			for d := path.Dir(c.Path); ; d = path.Dir(d) {
				affectedDirs[d] = true
				if d == "." {
					break
				}
			}
		}
	}

	var checkNode func(n *DirNode)
	checkNode = func(n *DirNode) {
		if isNodeAffected(n, changedFiles, repoRoot, docsDir, cache) {
			affectedDirs[n.Path] = true
		}

		for _, child := range n.Children {
			checkNode(child)
		}
	}
	checkNode(node)
	return affectedDirs
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

func collectTreeDirs(tree *DirNode) map[string]bool {
	dirs := make(map[string]bool)
	var walk func(n *DirNode)
	walk = func(n *DirNode) {
		dirs[n.Path] = true
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(tree)
	return dirs
}

func collectTreeNodes(tree *DirNode) map[string]*DirNode {
	nodes := make(map[string]*DirNode)
	var walk func(n *DirNode)
	walk = func(n *DirNode) {
		nodes[n.Path] = n
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(tree)
	return nodes
}

