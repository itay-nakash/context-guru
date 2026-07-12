package offload

import (
	"regexp"
	"strings"

	"github.com/kagenti/context-guru/components"
	"github.com/kagenti/context-guru/expand"
	"github.com/kagenti/context-guru/internal/treesitter"
	"github.com/kagenti/context-guru/schema"
	"github.com/maximhq/bifrost/core/schemas"
	sitter "github.com/tree-sitter/go-tree-sitter"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("skeleton", newSkeleton) }

// Skeleton strips function/method bodies from fenced code blocks, keeping
// signatures/imports/types (after headroom's code-aware compressor). It drops
// information (the bodies), so it is an Offload: the whole original message is
// stashed and an expand marker left for recovery. Class bodies are preserved so
// method signatures survive.
//
// v1 targets fenced ```lang code blocks (where the language is explicit); file
// reads without a fence/path are a later addition.
type Skeleton struct{ minTokens int }

type skeletonConfig struct {
	MinTokens int `yaml:"min_tokens"`
}

func newSkeleton(raw []byte) (components.Component, error) {
	cfg := skeletonConfig{MinTokens: 80}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Skeleton{minTokens: cfg.MinTokens}, nil
}

func (Skeleton) Name() string                 { return "skeleton" }
func (Skeleton) Enabled(*components.Ctx) bool { return true }

var fenceRe = regexp.MustCompile("(?s)```([A-Za-z0-9+#_-]*)\n(.*?)\n```")

// fenceLang maps a fenced code-block language token to a tree-sitter grammar.
var fenceLang = map[string]string{
	"go": "go", "golang": "go", "py": "python", "python": "python",
	"js": "javascript", "javascript": "javascript", "jsx": "javascript",
	"ts": "typescript", "typescript": "typescript", "tsx": "tsx",
	"rs": "rust", "rust": "rust", "java": "java", "c": "c", "h": "c",
	"cpp": "cpp", "c++": "cpp", "cc": "cpp", "rb": "ruby", "ruby": "ruby",
	"php": "php", "cs": "c_sharp", "csharp": "c_sharp", "kt": "kotlin",
	"kotlin": "kotlin", "swift": "swift", "scala": "scala",
}

func (s *Skeleton) Offload(req *schemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	var keys []string
	for i := range req.Input {
		m := &req.Input[i]
		if m.Role != schemas.ChatMessageRoleTool {
			continue // only tool outputs, like every sibling offloader — never mangle the user's/assistant's own code
		}
		if !schema.Rewritable(*m) {
			continue // non-text blocks would be dropped by a text rewrite
		}
		content := schema.MessageText(*m)
		if content == "" || !strings.Contains(content, "```") {
			continue
		}
		matches := fenceRe.FindAllStringSubmatchIndex(content, -1)
		if matches == nil {
			continue
		}
		var out strings.Builder
		last, changed := 0, false
		for _, mt := range matches { // mt: full[0,1] lang[2,3] body[4,5]
			grammar := fenceLang[strings.ToLower(content[mt[2]:mt[3]])]
			body := content[mt[4]:mt[5]]
			if grammar == "" || schema.TextTokens(body) < s.minTokens {
				continue
			}
			skel, ok := skeletonize([]byte(body), grammar)
			if !ok || schema.TextTokens(skel) >= schema.TextTokens(body) {
				continue
			}
			out.WriteString(content[last:mt[4]])
			out.WriteString(skel)
			last = mt[5]
			changed = true
		}
		if !changed {
			continue
		}
		out.WriteString(content[last:])
		key := hashKey(content)
		c.Store.Put(key, []byte(content))
		schema.SetMessageText(m, out.String()+"\n"+expand.Marker(key)+" [full source: call "+expand.ToolName+"]")
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		rep.Skipped = true
	}
	return keys, nil
}

// skeletonize parses src and replaces function/method/constructor bodies with a
// placeholder, keeping everything else. Returns ok=false on parse failure or
// when there is nothing to elide (fail-open: caller leaves the block untouched).
func skeletonize(src []byte, grammar string) (string, bool) {
	tree, _, ok := treesitter.Parse(grammar, src)
	if !ok || tree == nil {
		return "", false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil {
		return "", false
	}
	var ranges [][2]uint
	var walk func(n *sitter.Node, parentKind string)
	walk = func(n *sitter.Node, parentKind string) {
		kind := n.Kind()
		if isBodyKind(kind) && isDeclKind(parentKind) {
			ranges = append(ranges, [2]uint{n.StartByte(), n.EndByte()})
			return // don't recurse into an elided body (avoids nested double-elision)
		}
		for i := uint(0); i < n.ChildCount(); i++ {
			if ch := n.Child(i); ch != nil {
				walk(ch, kind)
			}
		}
	}
	walk(root, "")
	if len(ranges) == 0 {
		return "", false
	}
	var b strings.Builder
	last := uint(0)
	for _, r := range ranges {
		b.Write(src[last:r[0]])
		b.WriteString(placeholder(src[r[0]:r[1]]))
		last = r[1]
	}
	b.Write(src[last:])
	return b.String(), true
}

func isBodyKind(kind string) bool {
	switch kind {
	case "block", "statement_block", "compound_statement", "suite", "function_body":
		return true
	}
	return strings.Contains(kind, "body")
}

func isDeclKind(parentKind string) bool {
	return strings.Contains(parentKind, "function") ||
		strings.Contains(parentKind, "method") ||
		strings.Contains(parentKind, "constructor")
}

// placeholder preserves a brace-delimited body's outer braces so the skeleton
// stays syntactically suggestive; otherwise elides to an ellipsis.
func placeholder(seg []byte) string {
	if len(seg) >= 2 && seg[0] == '{' && seg[len(seg)-1] == '}' {
		return "{ … }"
	}
	return "…"
}
