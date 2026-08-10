package proxy

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Usage is one response's provider-billed token tiers. ok=false means the
// provider told us nothing, in which case a caller must report the request as
// partially accounted rather than pricing it as free.
type Usage struct {
	FreshInput int64
	CacheRead  int64
	CacheWrite int64
	Output     int64
}

// parseUsage extracts the four billed token tiers from a buffered response body,
// in whichever dialect it is written. This is the number that actually matters on
// this workload — the request is ~99.95% cached and a cache write bills ~11.5x a
// read, so content-token savings alone cannot express the economics.
//
// Dialects handled:
//
//	Anthropic  usage.{input_tokens, output_tokens,
//	           cache_read_input_tokens, cache_creation_input_tokens}
//	OpenAI     usage.{prompt_tokens, completion_tokens,
//	           prompt_tokens_details.cached_tokens}
//
// Anthropic's `input_tokens` already EXCLUDES the cached tiers, so it is the fresh
// figure directly. OpenAI's `prompt_tokens` INCLUDES its cached_tokens, so fresh is
// the difference — getting this backwards double-counts the whole transcript on
// every turn, which is exactly the kind of error a "savings" number hides.
func parseUsage(body []byte) (Usage, bool) {
	u := gjson.GetBytes(body, "usage")
	if !u.Exists() {
		return Usage{}, false
	}
	var out Usage
	switch {
	case u.Get("input_tokens").Exists(): // Anthropic
		out.FreshInput = u.Get("input_tokens").Int()
		out.Output = u.Get("output_tokens").Int()
		out.CacheRead = u.Get("cache_read_input_tokens").Int()
		out.CacheWrite = u.Get("cache_creation_input_tokens").Int()
	case u.Get("prompt_tokens").Exists(): // OpenAI
		prompt := u.Get("prompt_tokens").Int()
		out.Output = u.Get("completion_tokens").Int()
		out.CacheRead = u.Get("prompt_tokens_details.cached_tokens").Int()
		out.FreshInput = prompt - out.CacheRead
		if out.FreshInput < 0 {
			out.FreshInput = 0
		}
	case u.Get("output_tokens").Exists():
		// An OUTPUT-ONLY block. Anthropic's streaming message_delta carries exactly
		// this, and it holds the FINAL completion count — treating it as "no usage"
		// under-reports every streamed response's output tokens.
		out.Output = u.Get("output_tokens").Int()
	default:
		return Usage{}, false
	}
	if out.FreshInput|out.CacheRead|out.CacheWrite|out.Output == 0 {
		return Usage{}, false
	}
	return out, true
}

// parseSSEUsage pulls usage out of a streamed response. Both dialects report it in
// terminal events (Anthropic: message_start carries the input tiers and
// message_delta the output; OpenAI: a final chunk with `usage`), so the tiers are
// merged across events, taking the maximum of each — a later event repeating a
// value must not double it.
func parseSSEUsage(raw []byte) (Usage, bool) {
	var out Usage
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		// Anthropic nests the first usage under message.usage; OpenAI puts it at the top.
		for _, path := range []string{"usage", "message.usage"} {
			u := gjson.Get(payload, path)
			if !u.Exists() {
				continue
			}
			one, ok := parseUsage([]byte(`{"usage":` + u.Raw + `}`))
			if !ok {
				continue
			}
			found = true
			out.FreshInput = max64(out.FreshInput, one.FreshInput)
			out.CacheRead = max64(out.CacheRead, one.CacheRead)
			out.CacheWrite = max64(out.CacheWrite, one.CacheWrite)
			out.Output = max64(out.Output, one.Output)
		}
	}
	return out, found
}

// responseUsage picks the right parser for a response's content type.
func responseUsage(contentType string, body []byte) (Usage, bool) {
	if len(body) == 0 {
		return Usage{}, false
	}
	if strings.Contains(contentType, "event-stream") {
		return parseSSEUsage(body)
	}
	return parseUsage(body)
}

// sniffMax bounds each half of the sniffer's window. Usage blocks are a few
// hundred bytes; 64 KiB each way is generous and hard-caps the memory an
// adversarially long response can make us hold per in-flight request.
const sniffMax = 64 << 10

// sniffer keeps a bounded head+tail window of a streamed response so its usage
// block can be read after the stream completes, without buffering the response.
// A disabled sniffer allocates nothing and does no work.
type sniffer struct {
	on    bool
	head  []byte
	tail  []byte
	total int // bytes written, so bytes() knows whether the head alone is the whole body
}

func newSniffer(on bool) *sniffer { return &sniffer{on: on} }

func (s *sniffer) write(p []byte) {
	if !s.on {
		return
	}
	s.total += len(p)
	if len(s.head) < sniffMax {
		n := min(len(p), sniffMax-len(s.head))
		s.head = append(s.head, p[:n]...)
	}
	s.tail = append(s.tail, p...)
	if len(s.tail) > sniffMax {
		// Keep the last sniffMax bytes, re-slicing into a fresh buffer so the old one
		// can be collected rather than growing forever behind the slice header.
		keep := append(make([]byte, 0, sniffMax), s.tail[len(s.tail)-sniffMax:]...)
		s.tail = keep
	}
}

// bytes returns the retained window: head, then tail, with a newline between so a
// truncated SSE line in the middle cannot glue two events into one bogus line.
func (s *sniffer) bytes() []byte {
	if !s.on {
		return nil
	}
	if s.total <= len(s.head) {
		return s.head // the whole response fit in the head window; head IS the body
	}
	out := make([]byte, 0, len(s.head)+len(s.tail)+1)
	out = append(out, s.head...)
	out = append(out, '\n')
	return append(out, s.tail...)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
