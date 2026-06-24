package reduce

import (
	"path"
	"regexp"
	"strings"
)

// Code-reference signals. The question is not "was the basename restated" but: was
// the read file's PATH referenced later, or a SYMBOL it DEFINES used later, or a
// distinctive LITERAL it contains reused later? Every signal biases toward KEEP (the
// safe direction; reductions are reversible). Ported from winnow's signals/refs.py.
//
// ponytail: definedSymbols uses regex per language instead of tree-sitter. It biases
// identically (toward keep) with zero CGO/grammar deps. Upgrade to tree-sitter
// (go-tree-sitter) if precise symbol/skeleton extraction is ever needed.

var (
	identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	// def/func/function/fn/class/struct/enum/trait/interface NAME — common langs.
	defRe = regexp.MustCompile(
		`(?m)\b(?:func|function|def|fn|class|struct|enum|trait|interface|type)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	constRe  = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)
	numRe    = regexp.MustCompile(`(?:^|[^\w.])(\d{3,})(?:[^\w.]|$)`)
	quotedRe = regexp.MustCompile(`['"]([^'"\n]{3,40})['"]`)
)

var stopLiterals = set(
	"todo", "fixme", "note", "xxx", "hack", "true", "false", "null", "none",
	"get", "post", "put", "delete", "patch", "and", "or", "not", "the", "for",
	"error", "warning", "info", "debug", "string", "number", "object", "array",
)

func isCodePath(fp string) bool {
	switch strings.ToLower(path.Ext(fp)) {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".rs", ".java", ".c", ".h",
		".cc", ".cpp", ".hpp", ".rb", ".php", ".cs", ".kt", ".swift", ".scala":
		return true
	}
	return false
}

// definedSymbols returns names of functions/classes/etc. defined in code text.
// Empty for non-code paths. Drops names shorter than 3 chars (noisy).
func definedSymbols(text, filePath string) map[string]struct{} {
	if filePath == "" || !isCodePath(filePath) {
		return nil
	}
	out := map[string]struct{}{}
	for _, m := range defRe.FindAllStringSubmatch(text, -1) {
		if len(m[1]) >= 3 {
			out[m[1]] = struct{}{}
		}
	}
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
	for _, tok := range []string{filePath, path.Base(filePath)} {
		re := regexp.MustCompile(`(?:^|[^\w./-])` + regexp.QuoteMeta(tok) + `(?:[^\w]|$)`)
		if re.MatchString(text) {
			return true
		}
	}
	return false
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
