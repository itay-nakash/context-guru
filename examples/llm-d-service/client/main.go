// Command client is a minimal, dependency-free example of calling the
// context-guru compaction service the way the llm-d-router
// request-inline-compaction step does: POST the inference request body to the
// service and, on a 200 with a non-empty JSON object, use the returned (smaller)
// body in place of the original. Any other outcome is passthrough (keep the
// original). Copy compact() into your own router step and adapt as needed.
//
//	go run ./examples/llm-d-service/client            # default http://localhost:4000
//	go run ./examples/llm-d-service/client -addr http://compactor:4000
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", envOr("COMPACTOR_ADDR", "http://localhost:4000"), "compaction service base URL")
	flag.Parse()

	original := sampleRequest() // an OpenAI request whose tool output is a big JSON array

	compacted, ok := compact(*addr, original)
	if !ok {
		fmt.Println("compaction skipped (passthrough) — using the original body")
		compacted = original
	}
	fmt.Printf("original  : %d bytes\n", len(original))
	fmt.Printf("compacted : %d bytes  (%.0f%% of original)\n",
		len(compacted), 100*float64(len(compacted))/float64(len(original)))
	fmt.Printf("cg markers present: %v (want false)\n", bytes.Contains(compacted, []byte("<<cg:")))
}

// compact POSTs body to {addr}/compact and returns the replacement body. It
// mirrors the llm-d step's contract exactly: only a 200 with a non-empty JSON
// object counts as a successful compaction; every failure mode (network error,
// non-200, empty/invalid JSON) is passthrough — return ok=false and keep the
// original body.
func compact(addr string, body []byte) ([]byte, bool) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodPost, addr+"/compact", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	// Optional per-request override (leave off to use the service's loaded config):
	//   req.URL.RawQuery = "preset=summarize"
	//   req.Header.Set("x-context-guru-pipeline", "format,toon")

	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	var probe map[string]any
	if err := json.Unmarshal(out, &probe); err != nil || len(probe) == 0 {
		return nil, false
	}
	return out, true
}

// sampleRequest builds an OpenAI chat request whose single tool output is a large
// uniform JSON array — exactly the shape the TOON config compacts deterministically.
func sampleRequest() []byte {
	rows := make([]map[string]any, 0, 40)
	for i := 1; i <= 40; i++ {
		rows = append(rows, map[string]any{
			"id":     i,
			"name":   fmt.Sprintf("User %d", i),
			"email":  fmt.Sprintf("user%d@example.com", i),
			"role":   "member",
			"active": i%2 == 0,
		})
	}
	toolOutput, _ := json.Marshal(rows)

	req := map[string]any{
		"model": "gpt-4o-mini",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant."},
			map[string]any{"role": "user", "content": "List the active users."},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": string(toolOutput)},
		},
	}
	b, _ := json.Marshal(req)
	return b
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
