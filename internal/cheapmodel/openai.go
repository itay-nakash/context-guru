package cheapmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OpenAI calls a small OpenAI chat-completions model with a single user prompt and
// returns the content of the first choice. It implements engine.Model.
type OpenAI struct {
	BaseURL   string // default https://api.openai.com
	APIKey    string
	Model     string // e.g. gpt-4o-mini
	MaxTokens int    // default 2048
	Client    *http.Client
}

func (o OpenAI) Complete(ctx context.Context, prompt string) (string, error) {
	base := o.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	maxTok := o.MaxTokens
	if maxTok == 0 {
		maxTok = 2048
	}
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":      o.Model,
		"max_tokens": maxTok,
		"messages":   []any{map[string]any{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+o.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cheapmodel: status %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", nil
	}
	return out.Choices[0].Message.Content, nil
}
