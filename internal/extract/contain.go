// Package extract is the cheap-model tool-output extractor. A small model returns a
// SMALLER value of the same shape (selecting records/fields, never paraphrasing); the
// containment validator here PROVES the result is a lossless projection of the
// original, so a chatty model can never corrupt a value the agent relies on. On any
// failure it falls back to a deterministic projection, then to the original
// (fail-open). Ported from the reference prototype's actions/llm_compact.py.
//
// ponytail: the model returns the selected JSON directly and containment verifies it —
// no model-generated code (the reference prototype's Python sandbox) and no custom spec language. The
// containment proof is what makes the mechanism safe, whatever produced the subset.
package extract

import (
	"encoding/json"
	"strings"
)

// IsContained reports whether result is a lossless projection of original (both
// parsed values): string → contiguous substring OR an order-preserving
// subsequence of whole lines (so a log/code/traceback reduction that drops whole
// lines still proves lossless — every kept line appears verbatim, in order);
// list → order-preserving subsequence of contained items; dict → keys subset
// with contained values; numbers/bools/nil → equal where present; nil result →
// always allowed (dropping is fine).
func IsContained(result, original any) bool {
	return checkContained(result, original)
}

func checkContained(out, in any) bool {
	if out == nil {
		return true // dropping content is always allowed
	}
	if in == nil {
		return false
	}
	switch o := out.(type) {
	case string:
		s, ok := in.(string)
		return ok && (strings.Contains(s, o) || linesSubsequence(o, s))
	case bool:
		b, ok := in.(bool)
		return ok && b == o
	case json.Number:
		return numbersEqual(o, in)
	case float64:
		return numbersEqual(o, in)
	case []any:
		il, ok := in.([]any)
		if !ok || len(o) > len(il) {
			return false
		}
		i := 0
		for _, item := range o {
			matched := false
			for i < len(il) {
				if checkContained(item, il[i]) {
					matched = true
					i++
					break
				}
				i++
			}
			if !matched {
				return false
			}
		}
		return true
	case map[string]any:
		im, ok := in.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range o {
			iv, present := im[k]
			if !present {
				return false // extra key not in input
			}
			if !checkContained(v, iv) {
				return false
			}
		}
		return true
	default:
		return out == in
	}
}

// linesSubsequence reports whether every line of out appears, in order and
// byte-identical, as a line of in — i.e. out is in with whole lines dropped.
// This is the text analogue of the list-subsequence rule: a lossless projection
// that keeps whole lines verbatim (logs, source, tracebacks, search results).
func linesSubsequence(out, in string) bool {
	outL := strings.Split(out, "\n")
	inL := strings.Split(in, "\n")
	i := 0
	for _, ol := range outL {
		matched := false
		for i < len(inL) {
			if inL[i] == ol {
				matched = true
				i++
				break
			}
			i++
		}
		if !matched {
			return false
		}
	}
	return true
}

// numbersEqual compares two JSON-decoded numbers (json.Number or float64) by value.
func numbersEqual(a, b any) bool {
	return numStr(a) == numStr(b)
}

func numStr(v any) string {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	case float64:
		// json.Number-style rendering via json.Marshal keeps integers integral.
		b, _ := json.Marshal(n)
		return string(b)
	default:
		return ""
	}
}
