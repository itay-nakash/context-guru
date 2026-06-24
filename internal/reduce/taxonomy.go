package reduce

import (
	"encoding/json"
	"strings"
)

// Tool-name taxonomy covering common coding agents (Claude Code, Cursor, Aider,
// Cline, Continue, Codex). Ported from winnow's taxonomy.py.
//
// ponytail: defaults only; WINNOW_TOOLS-style env overrides land with the config
// package rather than being re-parsed here.
var (
	readTools = set(
		"read", "cat", "view", "open", "notebookread", "fetch_file",
		"read_file", "readfile", "open_file", "view_file", "get_file", "cat_file",
	)
	mutateTools = set(
		"edit", "write", "create", "apply_patch", "str_replace", "notebookedit",
		"edit_file", "write_file", "create_file", "str_replace_editor", "apply_diff",
		"replace_in_file", "insert", "search_replace",
	)
	fileKeys   = []string{"file_path", "path", "filename", "file", "target_file", "filepath"}
	offsetKeys = []string{"offset", "start_line", "line_start", "from_line"}
	limitKeys  = []string{"limit", "end_line", "line_end", "to_line"}
)

func set(xs ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		m[x] = struct{}{}
	}
	return m
}

func isReadTool(name string) bool {
	if name == "" {
		return false
	}
	_, ok := readTools[strings.ToLower(name)]
	return ok
}

func isMutateTool(name string) bool {
	if name == "" {
		return false
	}
	_, ok := mutateTools[strings.ToLower(name)]
	return ok
}

// fileArg pulls the file path out of a tool input.
func fileArg(input map[string]any) string {
	for _, k := range fileKeys {
		if v, ok := input[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// readRange extracts an (offset, limit) read range; nil pointers mean a full read.
func readRange(input map[string]any) (*int, *int) {
	return firstInt(input, offsetKeys), firstInt(input, limitKeys)
}

func firstInt(input map[string]any, keys []string) *int {
	for _, k := range keys {
		switch v := input[k].(type) {
		case bool:
			continue // bool excluded
		case float64:
			n := int(v)
			return &n
		case int:
			n := v
			return &n
		case json.Number:
			if i, err := v.Int64(); err == nil {
				n := int(i)
				return &n
			}
		}
	}
	return nil
}
