package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/arrase/code-reducer/internal/security"
	"github.com/arrase/code-reducer/internal/tools"
)

type FileChange struct {
	Path   string
	Status string // "Added", "Modified", "Deleted"
}

func isAllowedFile(repoRoot, relPath string, ignores []string) bool {
	if tools.ShouldIgnorePath(relPath, ignores) {
		return false
	}

	components := strings.Split(relPath, string(filepath.Separator))
	ignoredDirs := map[string]bool{
		".git":             true,
		"node_modules":     true,
		"dist":             true,
		"build":            true,
		"cache":            true,
		"code-reducer":     true,
		".gemini":          true,
		"bower_components": true,
		"__pycache__":      true,
		".pytest_cache":    true,
		".mypy_cache":      true,
		".tox":             true,
		"venv":             true,
		".venv":            true,
	}
	for _, comp := range components {
		if ignoredDirs[comp] || strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".egg-info") {
			return false
		}
	}

	name := filepath.Base(relPath)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".pdf" ||
		ext == ".exe" || ext == ".dll" || ext == ".so" || ext == ".o" || ext == ".a" ||
		ext == ".zip" || ext == ".gz" || ext == ".tar" || ext == ".lock" || ext == ".pyc" ||
		ext == ".pyo" || ext == ".pyd" || tools.NameSuffixIgnored(name) {
		return false
	}

	absPath, err := security.SafeResolve(repoRoot, relPath)
	if err != nil {
		return false
	}

	if tools.IsBinaryFile(absPath) {
		return false
	}

	return true
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

		safeName := strings.ReplaceAll(n.Path, string(filepath.Separator), "_")
		if safeName == "." || safeName == "" {
			safeName = "root"
		}
		modulePath := filepath.Join(docsDir, "modules", safeName+".md")
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
		d := filepath.Dir(f)
		if d == "." {
			root.Files = append(root.Files, f)
			continue
		}

		parts := strings.Split(d, string(filepath.Separator))
		curr := root
		currPath := ""
		for _, part := range parts {
			if currPath == "" {
				currPath = part
			} else {
				currPath = currPath + string(filepath.Separator) + part
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
