// Package cheapmodel provides a minimal Anthropic Messages client used as the
// engine's injected extraction model. It implements engine.Model. (An OpenAI variant
// is a straightforward follow-up; the canonical model is Anthropic-shaped, so the
// Anthropic client is the natural first.)
package cheapmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Anthropic calls a small Anthropic model with a single user prompt and returns the
// text of the first content block.
type Anthropic struct {
	BaseURL   string // default https://api.anthropic.com
	APIKey    string
	Model     string // e.g. claude-haiku-4-5
	MaxTokens int    // default 2048
	Client    *http.Client
	// AuthScheme selects how the API key is sent. "" or "x-api-key" sends the
	// x-api-key header (Anthropic default). "bearer" sends
	// Authorization: Bearer <APIKey> instead, for gateways (e.g. an IBM LiteLLM
	// Anthropic-compatible endpoint) that authenticate with a bearer token. The
	// anthropic-version header is sent in both cases.
	AuthScheme string
}

func (a Anthropic) Complete(ctx context.Context, prompt string) (string, error) {
	base := a.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	maxTok := a.MaxTokens
	if maxTok == 0 {
		maxTok = 2048
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":      a.Model,
		"max_tokens": maxTok,
		"messages":   []any{map[string]any{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	if a.AuthScheme == "bearer" {
		req.Header.Set("Authorization", "Bearer "+a.APIKey)
	} else {
		req.Header.Set("x-api-key", a.APIKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cheapmodel: status %d", resp.StatusCode)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	recordUsage(out.Usage.InputTokens, out.Usage.OutputTokens) // track CG component LLM cost
	// Return the first content block that carries text. A leading non-text block
	// (e.g. "thinking") has an empty Text, so we skip it rather than returning "".
	for _, c := range out.Content {
		if c.Text != "" {
			return c.Text, nil
		}
	}
	return "", nil
}
