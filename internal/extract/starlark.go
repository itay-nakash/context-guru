package extract

import (
	"context"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

const (
	starlarkMaxSteps = 50_000_000
	starlarkTimeout  = 2 * time.Second
)

// runStarlark asks the model for a Starlark program whose contract is: read the
// global string INPUT (the full tool output), assign a string global OUTPUT (the
// filtered value). It runs sandboxed over the FULL body — no imports, no I/O, step +
// time limits — and returns OUTPUT, or "" on any failure (fail-open). Containment is
// verified by the caller (RunExtraction).
func runStarlark(ctx context.Context, body, goal string, keepIDs []string, model Model) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	if model == nil {
		return ""
	}
	src, err := model.Complete(ctx, buildCodePrompt(body, goal, keepIDs))
	if err != nil {
		return ""
	}
	src = stripFences(src)

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
