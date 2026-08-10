package dash

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// knownSecrets are the shapes a credential actually takes in the traffic this proxy
// sees. Each must be unrecoverable from a stored row.
//
// Each fixture is ASSEMBLED at run time from a prefix and a filler rather than written
// as a literal. These are synthetic, but a literal that matches a provider's real token
// grammar closely enough to exercise our patterns also matches GitHub's push-protection
// scanner, which blocks the push -- and "weaken the fixture until the scanner stops
// caring" would weaken the test. Assembling them keeps the test strong and the source
// scanner-clean.
var knownSecrets = buildSecretFixtures()

// fakeKey assembles a synthetic Anthropic-shaped key around a caller-supplied marker,
// so a test can assert on a unique canary without a token-shaped literal in the source.
func fakeKey(marker string) string {
	return "sk-" + "ant-" + "api03-" + marker + "0123456789ABCDEF"
}

func buildSecretFixtures() []string {
	const az = "abcdefghijklmnopqrstuvwxyz"
	const digits = "0123456789"
	up := strings.ToUpper(az)
	jwt := func(part string) string { return "eyJ" + strings.Repeat(part, 6) }
	return []string{
		"sk-ant-" + "api03-" + strings.Repeat(up[:8], 4), // Anthropic
		"sk-" + "proj-" + digits + az[:16],               // OpenAI project key
		"ghp" + "_" + digits + az[:12] + digits + az[:6], // GitHub PAT
		"github" + "_pat_" + "11" + up[:7] + "0" + az[:12] + "_" + digits + az[:16],
		"AKIA" + up[:8] + digits[:4] + up[8:12],                  // AWS access key id
		"xox" + "b-" + digits + "-" + az[:14],                    // Slack bot token
		jwt(az[:6]) + "." + jwt(az[6:12]) + "." + jwt(az[12:18]), // JWT
	}
}

func TestRedactHeadersIsAllowlistOnly(t *testing.T) {
	got := RedactHeaders(map[string][]string{
		"Authorization":       {"Bearer " + fakeKey("SUPERSECRETVALUE")},
		"X-Api-Key":           {fakeKey("ANOTHERSECRET")},
		"Cookie":              {"session=abc"},
		"X-Vendor-New-Auth":   {"a-header-nobody-thought-of"},
		"Proxy-Authorization": {"Basic dXNlcjpwYXNz"},
		"Content-Type":        {"application/json"},
		"User-Agent":          {"claude-cli/2.0.0"},
		"Anthropic-Version":   {"2023-06-01"},
	})
	// Allowlisted headers keep their values — they are how you identify the client.
	if got["content-type"] != "application/json" || got["user-agent"] != "claude-cli/2.0.0" {
		t.Errorf("allowlisted headers were redacted: %v", got)
	}
	// Everything else is redacted BY KEY, including a header this code has never
	// heard of — which is the whole point of an allowlist over a denylist.
	for _, k := range []string{"authorization", "x-api-key", "cookie", "x-vendor-new-auth", "proxy-authorization"} {
		if got[k] != Redacted {
			t.Errorf("header %q = %q; want %q", k, got[k], Redacted)
		}
	}
	// The key must still be listed, so a viewer can see WHAT was sent.
	if _, ok := got["authorization"]; !ok {
		t.Error("redacted headers should still list their key")
	}
	flat := strings.Join(mapValues(got), " ")
	for _, s := range []string{"SUPERSECRETVALUE", "ANOTHERSECRET", "dXNlcjpwYXNz"} {
		if strings.Contains(flat, s) {
			t.Errorf("secret substring %q survived header redaction", s)
		}
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestRedactConfigAllowlistsKeys(t *testing.T) {
	in := map[string]any{
		"preset":   "codesmart",
		"pipeline": []any{"format", "extract"},
		"components": map[string]any{
			"extract_llm": map[string]any{
				"strategy":   "code",
				"min_tokens": 3000,
				"model":      map[string]any{"source": "config", "api_key": fakeKey("LEAKME")},
			},
		},
		"anthropic_api_key":      fakeKey("ALSOLEAKME"),
		"AUTH_TOKEN":             "Bearer nope",
		"undocumented_new_field": "who knows what this holds",
	}
	out := RedactConfig(in).(map[string]any)

	if out["preset"] != "codesmart" {
		t.Errorf("allowlisted preset was redacted: %v", out["preset"])
	}
	// A key that is not on the allowlist is withheld even though it might be
	// harmless — an allowlist that leaks by default is not an allowlist.
	if out["undocumented_new_field"] != Redacted {
		t.Errorf("unknown key was not redacted: %v", out["undocumented_new_field"])
	}
	if out["anthropic_api_key"] != Redacted || out["AUTH_TOKEN"] != Redacted {
		t.Errorf("credential-named keys survived: %v", out)
	}
	// A credential nested inside an allowlisted subtree must still be caught.
	flat := sprintDeep(out)
	for _, s := range []string{"LEAKME", "ALSOLEAKME", "sk-ant"} {
		if strings.Contains(flat, s) {
			t.Errorf("secret %q survived config redaction; got %s", s, flat)
		}
	}
	// The allowlisted structure survives, so the view is still useful.
	if !strings.Contains(flat, "codesmart") || !strings.Contains(flat, "extract") {
		t.Errorf("redaction destroyed the useful structure: %s", flat)
	}
}

// TestRedactConfigKeepsComponentBlocksUseful is the balance the config view needs:
// component NAMES are user-chosen so they cannot be allowlisted, but their settings
// are exactly what a viewer came to see. Redacting the whole subtree (the first
// implementation) made the view useless; passing it through wholesale would leak a
// component's model credential. Names pass, fields are allowlisted, credentials go.
func TestRedactConfigKeepsComponentBlocksUseful(t *testing.T) {
	out := RedactConfig(map[string]any{
		"preset": "codesmart",
		"components": map[string]any{
			"extract_llm": map[string]any{
				"strategy":   "code",
				"min_tokens": 3000,
				"trigger":    map[string]any{"min_request_tokens": 3000},
				"model":      map[string]any{"source": "config", "api_key": fakeKey("NESTEDLEAK")},
			},
			"a_plugin_nobody_allowlisted": map[string]any{"max_tokens": 500},
		},
	}).(map[string]any)

	comps, ok := out["components"].(map[string]any)
	if !ok {
		t.Fatalf("components subtree was flattened to %v; the config view would show nothing", out["components"])
	}
	ex, ok := comps["extract_llm"].(map[string]any)
	if !ok {
		t.Fatalf("extract_llm block was redacted wholesale: %v", comps["extract_llm"])
	}
	if ex["strategy"] != "code" {
		t.Errorf("strategy = %v; an allowlisted component field must survive", ex["strategy"])
	}
	if ex["trigger"] == Redacted {
		t.Error("a nested allowlisted field (trigger) was redacted")
	}
	// An unknown component's block must still render (its name is user-chosen), with
	// its own fields allowlisted.
	plug, ok := comps["a_plugin_nobody_allowlisted"].(map[string]any)
	if !ok {
		t.Fatalf("an unregistered component's block was redacted wholesale: %v",
			comps["a_plugin_nobody_allowlisted"])
	}
	if plug["max_tokens"] != 500 {
		t.Errorf("max_tokens = %v; want 500", plug["max_tokens"])
	}
	// And the credential inside it is still gone.
	if strings.Contains(sprintDeep(out), "NESTEDLEAK") {
		t.Errorf("a credential nested two levels inside components leaked: %s", sprintDeep(out))
	}
}

// TestSecretishKeyIsAnchoredNotSubstring pins both directions of the key matcher.
// A substring match on "token" also swallows max_tokens/min_tokens and redacts every
// threshold in the config view; a match that is too narrow leaks a credential.
func TestSecretishKeyIsAnchoredNotSubstring(t *testing.T) {
	secret := []string{
		"api_key", "apikey", "ANTHROPIC_API_KEY", "access_key_id", "secret_key",
		"private_key", "auth_token", "access_token", "refresh_token", "session_token",
		"password", "passwd", "passphrase", "credential", "credentials",
		"authorization", "bearer", "cookie", "token", "key", "secret", "auth",
		"cheap_model_key", "model_api_key",
	}
	notSecret := []string{
		"max_tokens", "min_tokens", "min_request_tokens", "output_tokens",
		"tokens_before", "llm_max_per_request", "keep_first", "keep_last",
		"marker_mode", "monkey", "keyboard_layout", "strategy", "pipeline",
	}
	for _, k := range secret {
		if !secretishKey.MatchString(k) {
			t.Errorf("key %q is a credential name but was NOT matched; it would be stored", k)
		}
	}
	for _, k := range notSecret {
		if secretishKey.MatchString(k) {
			t.Errorf("key %q was matched as a credential; the config view loses a real setting", k)
		}
	}
}

func TestRedactContentScrubsKnownSecrets(t *testing.T) {
	for _, secret := range knownSecrets {
		text := "Exit code 0\nHere is the env dump:\nSOME_TOKEN=" + secret + "\nand inline " + secret + " too\n"
		got := RedactContent(text, 0)
		if strings.Contains(got, secret) {
			t.Errorf("secret %q survived content redaction:\n%s", secret, got)
		}
		if !strings.Contains(got, "Exit code 0") {
			t.Errorf("redaction destroyed surrounding content for %q", secret)
		}
	}
}

func TestRedactContentKeepsOrdinaryCode(t *testing.T) {
	code := `func Fib(n int) int {
	if n < 2 { return n }
	a, b := 0, 1
	for i := 2; i <= n; i++ { a, b = b, a+b }
	return b
}
// a comment mentioning the word key and token, harmlessly
skeleton := "skip-this-not-a-secret"
`
	got := RedactContent(code, 0)
	if strings.Contains(got, Redacted) {
		t.Errorf("ordinary code was shredded by the secret patterns:\n%s", got)
	}
}

func TestRedactContentCapAppliesAfterScrubbing(t *testing.T) {
	secret := fakeKey("TRAILINGSECRET")
	// Put the secret right at the cap boundary: a cap applied FIRST could truncate
	// the pattern into an unmatchable prefix and store it.
	text := strings.Repeat("x", 200) + secret + strings.Repeat("y", 200)
	got := RedactContent(text, 210)
	if strings.Contains(got, "TRAILINGSECRET") {
		t.Errorf("a secret at the truncation boundary survived:\n%s", got)
	}
	if len(got) > 260 { // cap + the truncation notice
		t.Errorf("cap not applied: %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncation should be visible, not silent")
	}
}

func TestRedactContentCapIsRuneSafe(t *testing.T) {
	// Cap lands mid-rune; the result must still be valid UTF-8.
	got := RedactContent(strings.Repeat("é", 100), 51)
	for i, r := range got {
		if r == '�' {
			t.Fatalf("cap produced an invalid rune at %d: %q", i, got)
		}
	}
}

// TestNoSecretReachesTheDatabase is the end-to-end version of the guarantee: run a
// captured event carrying a secret through the real capture path, then read every
// byte of every stored column back and assert the secret is not there.
func TestNoSecretReachesTheDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.db")
	rec, err := NewRecorder(Options{DBPath: path, CaptureContent: true, ContentCap: 1 << 20,
		ContentMaxPerRequest: 10})
	if err != nil {
		t.Fatal(err)
	}
	secret := fakeKey("DBLEAKCANARY")
	e := mkEvent(1000, "s", "m", 500, 100)
	e.Content = []ContentRow{{
		Path:   "messages.2",
		Before: "ANTHROPIC_API_KEY=" + secret + "\nsome more tool output\n",
		After:  "ANTHROPIC_API_KEY=" + secret,
	}}
	// The proxy redacts before Record; do the same here so the test exercises the
	// documented contract rather than an internal shortcut.
	for i := range e.Content {
		e.Content[i].Before = RedactContent(e.Content[i].Before, 1<<20)
		e.Content[i].After = RedactContent(e.Content[i].After, 1<<20)
	}
	rec.Record(e)
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Request(1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Content) == 0 {
		t.Fatal("no content stored; the test would pass vacuously")
	}
	for _, c := range got.Content {
		if strings.Contains(c.Before, secret) || strings.Contains(c.After, secret) {
			t.Fatalf("the secret reached disk: %q / %q", c.Before, c.After)
		}
		if !strings.Contains(c.Before, "ANTHROPIC_API_KEY") {
			t.Error("redaction removed the variable NAME too; the diff loses its meaning")
		}
	}
}

// sprintDeep flattens a redacted config tree to one string, so a test can assert
// no secret substring appears anywhere in it.
func sprintDeep(v any) string { return fmt.Sprintf("%v", v) }
