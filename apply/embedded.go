package apply

import (
	"os"
	"strconv"
	"strings"
)

// Embedded tool output (terminal-style agents).
//
// Some agents never send tool outputs as tool messages at all. Harbor's
// terminus-2 (and any agent built the same way) drives a plain chat loop: it
// calls the model with `messages=[{role:user, content:<prompt>}, {role:assistant,
// ...}, ...]` and feeds the terminal screen back as the *text of the next user
// message*, prefixed by a fixed template line ("New Terminal Output:", "Current
// Terminal Screen:", ...). There is no `tools` array, no `tool_calls`, no
// `role=tool`, and on the OpenAI route nothing for normalize's Anthropic branch
// to expand.
//
// Every component starts with `if m.Role != ChatMessageRoleTool { continue }`, so
// such traffic passes through completely untouched — measured on a 50-task
// terminal-bench run: 2,245 requests, 9 components, 20,205 component invocations,
// `acted=0` on every one, `tokens_before == tokens_after` to the byte. Not a
// threshold problem (median final context was 14k tokens): the text was never
// examined.
//
// This file recovers that traffic. When a user message's text carries one of the
// known preamble markers, the span from the marker to the end of the embedded
// output becomes a synthetic role=tool message the existing components already
// know how to shrink; a rewrite is spliced back into exactly that span, leaving
// the instruction prose around it byte-identical. It is deliberately narrow:
// only an explicit marker match qualifies, so ordinary user prose is never
// mistaken for a tool result.
//
// Reversibility caveat. These agents declare no tools, so they cannot call
// context_guru_expand and expand.Inject's "auto" mode correctly declines to
// advertise it. A default `marker_mode: full` therefore leaves a marker whose
// text invites a call the agent cannot make (the stash is still reachable
// out-of-band via GET /expand). Pair this with `marker_mode: summary` for a
// self-contained marker with no dangling tool reference.

// embeddedMarkers are the preamble lines terminal-style agents put in front of
// captured terminal output. Each is matched literally, at the start of a line, and
// the tool output is everything after the marker's own newline.
//
// Sourced from harbor's terminus-2 templates: `tmux_session.get_incremental_output`
// emits "New Terminal Output:" / "Current Terminal Screen:";
// `terminus-{json,xml}-plain.txt` and the completion-confirmation prompt use
// "Current terminal state:"; `timeout.txt` uses the "Here is the current state..."
// phrasing.
var embeddedMarkers = []string{
	"New Terminal Output:\n",
	"Current Terminal Screen:\n",
	"Current terminal state:\n",
	"Here is the current state of the terminal:\n",
}

// embeddedTrailers are instruction lines the agent appends AFTER the embedded
// terminal output. They are prose the model must keep reading verbatim (they tell
// it how to respond), so they are excluded from the extracted span rather than
// handed to the components as if they were tool output.
//
// From terminus-2's `_get_completion_confirmation_message`, which wraps the
// screen in "Current terminal state:\n{...}\n\nAre you sure...".
var embeddedTrailers = []string{
	"\nAre you sure you want to mark the task as complete?",
}

// embeddedMinChars is the floor for treating a marked span as tool output. Below
// it there is nothing worth compacting and the marker is more likely incidental
// (e.g. the system prompt's own description of the output format, which quotes
// these very strings). Overridable for experiments; 0 or invalid keeps the default.
var embeddedMinChars = envInt("CONTEXT_GURU_EMBEDDED_MIN_CHARS", 200)

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

// embeddedSpan locates embedded terminal output inside a user message's text.
// It returns the byte offsets of the output itself (excluding the marker line and
// any trailing instruction prose) and whether a usable span was found.
//
// The LAST marker wins. terminus-2 nests them — the completion-confirmation
// prompt is "Current terminal state:\n" + output that itself begins "New Terminal
// Output:\n" — and the innermost (last) marker is the one that actually precedes
// the payload, so taking it keeps the outer preamble out of the extracted text.
func embeddedSpan(text string) (start, end int, ok bool) {
	start = -1
	for _, mk := range embeddedMarkers {
		// Match only at a line start so the marker can't be picked up mid-sentence
		// (the system prompt mentions these strings when describing the format).
		for i := 0; ; {
			j := strings.Index(text[i:], mk)
			if j < 0 {
				break
			}
			at := i + j
			if (at == 0 || text[at-1] == '\n') && at+len(mk) > start {
				start = at + len(mk)
			}
			i = at + len(mk)
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	end = len(text)
	for _, tr := range embeddedTrailers {
		if k := strings.Index(text[start:], tr); k >= 0 && start+k < end {
			end = start + k
		}
	}
	if end-start < embeddedMinChars {
		return 0, 0, false
	}
	return start, end, true
}
