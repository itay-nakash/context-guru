package reduce

import (
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/kagenti/lab-context-engineering/internal/treesitter"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Code-reference signals. The question is not "was the basename restated" but: was
// the read file's PATH referenced later, or a SYMBOL it DEFINES used later, or a
// distinctive LITERAL it contains reused later? Every signal biases toward KEEP (the
// safe direction; reductions are reversible). Ported from the reference prototype's signals/refs.py.
//
// definedSymbols uses tree-sitter (go-tree-sitter + per-grammar forest), which catches
// methods the old regex missed and ignores commented-out definitions. It still biases
// toward KEEP and fails open (empty on non-code paths or parse failure).

var (
	identRe  = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	constRe  = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)
	numRe    = regexp.MustCompile(`(?:^|[^\w.])(\d{3,})(?:[^\w.]|$)`)
	quotedRe = regexp.MustCompile(`['"]([^'"\n]{3,40})['"]`)
)

var stopLiterals = set(
	"todo", "fixme", "note", "xxx", "hack", "true", "false", "null", "none",
	"get", "post", "put", "delete", "patch", "and", "or", "not", "the", "for",
	"error", "warning", "info", "debug", "string", "number", "object", "array",
)

func isCodePath(fp string) bool { return treesitter.LangForExt(fp) != "" }

// defNodeKinds: definition node kinds whose "name" field (or identifier child)
// names a symbol, across grammars.
var defNodeKinds = map[string]bool{
	"function_declaration": true, "function_definition": true, "function_item": true,
	"method_declaration": true, "method_definition": true, "method": true,
	"class_declaration": true, "class_definition": true, "class": true,
	"struct_item": true, "enum_item": true, "trait_item": true,
	"type_spec": true, "interface_declaration": true, "module": true,
}

// definedSymbols returns names of functions/classes/methods/types defined in code
// text. Empty for non-code paths or on any parse failure (fail-open). Drops names
// shorter than 3 chars.
func definedSymbols(text, filePath string) map[string]struct{} {
	lang := treesitter.LangForExt(filePath)
	if lang == "" {
		return nil
	}
	src := []byte(text)
	tree, _, ok := treesitter.Parse(lang, src)
	if !ok {
		return nil
	}
	defer tree.Close()
	out := map[string]struct{}{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if defNodeKinds[n.Kind()] {
			if name := n.ChildByFieldName("name"); name != nil {
				if s := name.Utf8Text(src); len(s) >= 3 {
					out[s] = struct{}{}
				}
			}
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return out
}

// salientLiterals returns distinctive literal values (ALL_CAPS, 3+ digit numbers,
// short quoted strings) whose later standalone reuse implies the content was used.
func salientLiterals(text string) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(s string) {
		if len(s) >= 3 {
			if _, stop := stopLiterals[strings.ToLower(s)]; !stop {
				out[s] = struct{}{}
			}
		}
	}
	for _, m := range constRe.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range numRe.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range quotedRe.FindAllStringSubmatch(text, -1) {
		add(strings.TrimSpace(m[1]))
	}
	return out
}

func isIdentifier(s string) bool {
	return s != "" && identRe.FindString(s) == s
}

// literalsUsed reports whether any salient literal reappears in text.
func literalsUsed(literals map[string]struct{}, text string) bool {
	if len(literals) == 0 || text == "" {
		return false
	}
	idents := tokenSet(identRe, text)
	for lit := range literals {
		if _, ok := idents[lit]; ok {
			return true
		}
		if !isIdentifier(lit) && strings.Contains(text, lit) {
			return true
		}
	}
	return false
}

// pathReferenced reports whether the full path or basename appears as a standalone
// path token (boundary-aware, not a naive substring).
func pathReferenced(filePath, text string) bool {
	if filePath == "" || text == "" {
		return false
	}
	// Compile the boundary patterns once per call (the caller scans this in an
	// O(items²) loop, so building the regex per token inside the loop was hot).
	for _, tok := range []string{filePath, path.Base(filePath)} {
		re := pathTokenRe(tok)
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// pathTokenCache memoizes the boundary-aware regex for each path token across calls.
var pathTokenCache sync.Map // string -> *regexp.Regexp

func pathTokenRe(tok string) *regexp.Regexp {
	if v, ok := pathTokenCache.Load(tok); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?:^|[^\w./-])` + regexp.QuoteMeta(tok) + `(?:[^\w]|$)`)
	pathTokenCache.Store(tok, re)
	return re
}

// symbolsUsed reports whether any defined symbol appears as a standalone identifier.
func symbolsUsed(symbols map[string]struct{}, text string) bool {
	if len(symbols) == 0 || text == "" {
		return false
	}
	idents := tokenSet(identRe, text)
	for s := range symbols {
		if _, ok := idents[s]; ok {
			return true
		}
	}
	return false
}

func tokenSet(re *regexp.Regexp, text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range re.FindAllString(text, -1) {
		out[t] = struct{}{}
	}
	return out
}
