package extract

import (
	"context"
	"regexp"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

const (
	starlarkMaxSteps = 50_000_000
	starlarkTimeout  = 2 * time.Second
)

// reBuiltins are the regex helpers injected into the sandbox so a model-written
// filter can trim words/sentences/parts, not just whole lines. Backed by stdlib
// regexp (RE2: linear-time, no catastrophic backtracking, pure-Go). A bad pattern
// returns a Starlark error → the program fails → the caller falls open.
//
//	re_sub(pattern, repl, s) -> string     (regexp.ReplaceAllString)
//	re_findall(pattern, s)   -> [string]   (all non-overlapping matches)
//	re_split(pattern, s)     -> [string]   (split on every match)
//	re_match(pattern, s)     -> bool       (does s contain a match)
func reBuiltins() starlark.StringDict {
	str := func(fn func(*regexp.Regexp, string) starlark.Value, arity3 bool, name string) *starlark.Builtin {
		return starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kw []starlark.Tuple) (starlark.Value, error) {
			var pat, repl, s string
			var err error
			if arity3 {
				err = starlark.UnpackArgs(b.Name(), args, kw, "pattern", &pat, "repl", &repl, "s", &s)
			} else {
				err = starlark.UnpackArgs(b.Name(), args, kw, "pattern", &pat, "s", &s)
			}
			if err != nil {
				return nil, err
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return nil, err
			}
			if arity3 {
				return starlark.String(re.ReplaceAllString(s, repl)), nil
			}
			return fn(re, s), nil
		})
	}
	toList := func(ss []string) starlark.Value {
		vs := make([]starlark.Value, len(ss))
		for i, s := range ss {
			vs[i] = starlark.String(s)
		}
		return starlark.NewList(vs)
	}
	return starlark.StringDict{
		"re_sub":     str(nil, true, "re_sub"),
		"re_findall": str(func(re *regexp.Regexp, s string) starlark.Value { return toList(re.FindAllString(s, -1)) }, false, "re_findall"),
		"re_split":   str(func(re *regexp.Regexp, s string) starlark.Value { return toList(re.Split(s, -1)) }, false, "re_split"),
		"re_match":   str(func(re *regexp.Regexp, s string) starlark.Value { return starlark.Bool(re.MatchString(s)) }, false, "re_match"),
	}
}

// runStarlark asks the model for a Starlark program whose contract is: read the
// global string INPUT (the full tool output), assign a string global OUTPUT (the
// filtered value). It runs sandboxed over the FULL body — no imports, no I/O, step +
// time limits — and returns OUTPUT, or "" on any failure (fail-open). Containment is
// verified by the caller (RunExtraction).
func runStarlark(ctx context.Context, body, goal string, keepIDs []string, model Model, rewrite bool) (out string) {
	if model == nil {
		return ""
	}
	src, err := model.Complete(ctx, buildCodePrompt(body, goal, keepIDs, rewrite))
	if err != nil {
		return ""
	}
	return execStarlark(ctx, body, stripFences(src))
}

// execStarlark runs a Starlark filter source over the body (INPUT global, json
// module, no imports, step + time limits) and returns OUTPUT, or "" on any
// failure (fail-open). Split out from runStarlark so tests/examples can run a
// captured source — the exact program the model wrote — deterministically.
func execStarlark(ctx context.Context, body, src string) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, starlarkTimeout)
	defer cancel()
	thread := &starlark.Thread{Name: "extract"} // Load==nil => load() disabled
	thread.SetMaxExecutionSteps(starlarkMaxSteps)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel(ctx.Err().Error())
		case <-done:
		}
	}()
	defer close(done)

	predeclared := starlark.StringDict{
		"json":  starjson.Module,
		"INPUT": starlark.String(body),
	}
	for k, v := range reBuiltins() {
		predeclared[k] = v
	}
	globals, err := starlark.ExecFile(thread, "extract.star", src, predeclared)
	if err != nil {
		return ""
	}
	res, ok := globals["OUTPUT"].(starlark.String)
	if !ok {
		return ""
	}
	return string(res)
}
