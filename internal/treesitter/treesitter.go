//go:build cg_skeleton

// Package treesitter wraps the tree-sitter binding for the reduce signals and
// skeletonizer. CGO-backed; fail-open (unknown language / parse error => ok=false).
// Gated behind the cg_skeleton build tag (its only consumer is the skeleton
// component), so the default pure-Go build never links the tree-sitter cgo
// bindings or the ~15 grammar subpackages. See stub.go for the no-tag package.
//
// It pairs the official github.com/tree-sitter/go-tree-sitter binding (which
// owns the *Tree/*Language types and the explicit Close lifecycle the callers
// rely on) with the per-language grammar subpackages of
// github.com/alexaandru/go-sitter-forest. Each subpackage exposes
// GetLanguage() unsafe.Pointer, which sitter.NewLanguage adopts directly.
//
// Only the grammars referenced by extToLang are linked, instead of importing
// the forest root package (which links all ~490 grammars into the binary).
package treesitter

import (
	"path"
	"strings"
	"unsafe"

	sitterc "github.com/alexaandru/go-sitter-forest/c"
	csharp "github.com/alexaandru/go-sitter-forest/c_sharp"
	cpp "github.com/alexaandru/go-sitter-forest/cpp"
	golang "github.com/alexaandru/go-sitter-forest/go"
	java "github.com/alexaandru/go-sitter-forest/java"
	javascript "github.com/alexaandru/go-sitter-forest/javascript"
	kotlin "github.com/alexaandru/go-sitter-forest/kotlin"
	php "github.com/alexaandru/go-sitter-forest/php"
	python "github.com/alexaandru/go-sitter-forest/python"
	ruby "github.com/alexaandru/go-sitter-forest/ruby"
	rust "github.com/alexaandru/go-sitter-forest/rust"
	scala "github.com/alexaandru/go-sitter-forest/scala"
	swift "github.com/alexaandru/go-sitter-forest/swift"
	tsx "github.com/alexaandru/go-sitter-forest/tsx"
	typescript "github.com/alexaandru/go-sitter-forest/typescript"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

var extToLang = map[string]string{
	".go": "go", ".py": "python", ".js": "javascript", ".jsx": "javascript",
	".ts": "typescript", ".tsx": "tsx", ".rs": "rust", ".java": "java",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp",
	".rb": "ruby", ".php": "php", ".cs": "c_sharp", ".kt": "kotlin",
	".swift": "swift", ".scala": "scala",
}

// langPtr maps a grammar name to its raw tree-sitter language pointer accessor.
// Keyed by the grammar names that extToLang can return.
var langPtr = map[string]func() unsafe.Pointer{
	"go":         golang.GetLanguage,
	"python":     python.GetLanguage,
	"javascript": javascript.GetLanguage,
	"typescript": typescript.GetLanguage,
	"tsx":        tsx.GetLanguage,
	"rust":       rust.GetLanguage,
	"java":       java.GetLanguage,
	"c":          sitterc.GetLanguage,
	"cpp":        cpp.GetLanguage,
	"ruby":       ruby.GetLanguage,
	"php":        php.GetLanguage,
	"c_sharp":    csharp.GetLanguage,
	"kotlin":     kotlin.GetLanguage,
	"swift":      swift.GetLanguage,
	"scala":      scala.GetLanguage,
}

// LangForExt maps a file path to a tree-sitter grammar name, or "" if unsupported.
func LangForExt(p string) string {
	return extToLang[strings.ToLower(path.Ext(p))]
}

// Parse parses src under the named grammar. ok=false on unknown grammar or a nil
// language; the caller treats that as "skip" (fail-open). Caller must Close the tree.
func Parse(lang string, src []byte) (*sitter.Tree, *sitter.Language, bool) {
	getPtr := langPtr[lang]
	if getPtr == nil {
		return nil, nil, false
	}
	raw := getPtr()
	if raw == nil {
		return nil, nil, false
	}
	language := sitter.NewLanguage(raw)
	if language == nil {
		return nil, nil, false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return nil, nil, false
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, nil, false
	}
	return tree, language, true
}
