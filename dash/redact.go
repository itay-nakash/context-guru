package dash

import (
	"fmt"
	"regexp"
	"strings"
)

// Redaction happens BEFORE anything reaches the database. This is the whole
// design: a secret that lands in a row is a secret on disk forever, and a
// redact-on-read filter is one forgotten code path away from leaking it. So
// nothing sensitive is ever stored, and the API has no redaction step at all.
//
// Two mechanisms, matching gateway's (correct) choice of default:
//
//   - Headers: blanket-redact by KEY. Every header is dropped unless it is on a
//     short allowlist of headers known to be non-secret. A denylist of "the auth
//     headers we thought of" fails the moment a gateway invents a new one.
//   - Config: allowlist the KEYS we render. context-guru's effective config is
//     structured and finite, so naming the safe keys is tractable and safe.
//
// Content (transcript before/after text) cannot be allowlisted — it is arbitrary
// agent output — so it gets pattern-based scrubbing of the shapes that are
// unambiguously credentials, plus a hard size cap.

// Redacted is the placeholder written in place of any redacted value. It is
// deliberately visible: a blank would read as "the field was empty".
const Redacted = "«redacted»"

// headerAllowlist is the set of request headers safe to store verbatim. Anything
// not listed here is redacted by key, value unseen.
var headerAllowlist = map[string]bool{
	"content-type":                true,
	"content-length":              true,
	"user-agent":                  true,
	"accept":                      true,
	"accept-encoding":             true,
	"anthropic-version":           true,
	"anthropic-beta":              true,
	"x-stainless-lang":            true,
	"x-stainless-os":              true,
	"x-stainless-arch":            true,
	"x-stainless-package-version": true,
	"x-stainless-runtime":         true,
	"x-stainless-runtime-version": true,
	"x-app":                       true,
}

// RedactHeaders returns a storable copy of a request's headers: allowlisted keys
// keep their value, every other key is present (so you can see WHAT was sent)
// with its value replaced.
func RedactHeaders(h map[string][]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		lk := strings.ToLower(k)
		if headerAllowlist[lk] && len(vs) > 0 {
			out[lk] = vs[0]
			continue
		}
		out[lk] = Redacted
	}
	return out
}

// configAllowlist names the effective-config keys the /api/config view may
// render. Everything else in the resolved configuration is withheld, because a
// component's config block is free-form YAML and could carry an endpoint
// credential (e.g. a component's model: block).
var configAllowlist = map[string]bool{
	"preset": true, "pipeline": true, "mode": true, "cache_mode": true,
	"inject_expand": true, "store": true, "components": true,
	"store_enabled": true, "store_ttl_seconds": true, "store_max_entries": true,
	"listen_addr": true, "openai_upstream": true, "anthropic_upstream": true,
	"bob_upstream": true, "force_model": true, "cheap_model": true,
	"cheap_model_provider": true, "cheap_model_base": true,
	"dashboard": true, "db_path": true, "retention": true, "capture_content": true,
	"trusted_cidrs": true, "build_version": true, "build_commit": true,
	"max_tokens": true, "min_tokens": true, "head_lines": true, "tail_lines": true,
	"strategy": true, "source": true, "model": true, "trigger": true,
	"min_request_tokens": true, "llm_every_n_requests": true, "llm_max_per_request": true,
	"marker_mode": true, "min_items": true, "keep_first": true, "keep_last": true,
	"enabled": true, "ttl_seconds": true, "max_entries": true,
}

// secretishKey matches config keys that are credentials BY NAME, whatever the
// allowlist says — so a component block nesting an api_key under an otherwise
// allowlisted name cannot leak.
//
// It is deliberately anchored on whole words rather than substrings. A naive
// `(key|token|...)` substring match also swallows `max_tokens`, `min_tokens` and
// `min_request_tokens`, redacting every threshold in the config view and making it
// useless — the same "safety that destroys the feature" trap as redacting whole
// component blocks. `cache_key` and `api_key` still match; `max_tokens` does not.
var secretishKey = regexp.MustCompile(`(?i)(^|_)(api_?key|access_?key|secret_?key|private_?key|` +
	`auth_?token|access_?token|refresh_?token|id_?token|session_?token|bearer_?token|` +
	`key|token|secret|password|passwd|passphrase|credential|credentials|auth|authorization|bearer|cookie)($|_)`)

// openKeys name subtrees whose immediate child keys are USER-CHOSEN and therefore
// cannot be allowlisted: `components` is keyed by component name, and a plugin can
// register any name it likes. Their children pass through by name, and the
// allowlist then applies one level deeper to the block's own fields — otherwise the
// effective-config view redacts every component's configuration and shows nothing,
// which defeats the point of the view.
var openKeys = map[string]bool{"components": true}

// RedactConfig walks a decoded configuration tree and returns a copy in which
// only allowlisted keys survive with their values; everything else is replaced by
// the placeholder. Maps and slices are walked; scalars are passed through.
func RedactConfig(v any) any {
	return redactConfig(v, false)
}

func redactConfig(v any, openNames bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			lk := strings.ToLower(k)
			switch {
			case secretishKey.MatchString(lk):
				out[k] = Redacted
			case openNames:
				// This level's key is a user-chosen name (a component id); keep it and
				// resume allowlisting inside its block.
				out[k] = redactConfig(val, false)
			case openKeys[lk]:
				out[k] = redactConfig(val, true)
			case configAllowlist[lk]:
				out[k] = redactConfig(val, false)
			default:
				out[k] = Redacted
			}
		}
		return out
	case map[any]any: // yaml can produce this when a key is not a string
		conv := make(map[string]any, len(t))
		for k, val := range t {
			conv[fmt.Sprint(k)] = val
		}
		return redactConfig(conv, openNames)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactConfig(e, openNames)
		}
		return out
	default:
		return v
	}
}

// contentSecrets are the credential shapes that appear verbatim in agent output
// (a leaked env dump, a curl command in a shell transcript, a .env cat). Each is
// specific enough not to shred ordinary code.
var contentSecrets = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),                                               // OpenAI-style
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{16,}`),                                           // Anthropic
	regexp.MustCompile(`(?i)\bghp_[A-Za-z0-9]{20,}`),                                          // GitHub PAT
	regexp.MustCompile(`(?i)\bgithub_pat_[A-Za-z0-9_]{20,}`),                                  // GitHub fine-grained PAT
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                                // AWS access key id
	regexp.MustCompile(`(?i)\bxox[baprs]-[A-Za-z0-9-]{10,}`),                                  // Slack
	regexp.MustCompile(`(?i)\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), // JWT
	// KEY=value / "key": "value" assignments whose NAME says credential.
	regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:API_?KEY|AUTH_?TOKEN|SECRET|PASSWORD|ACCESS_?TOKEN|PRIVATE_?KEY)[A-Z0-9_]*)\s*[:=]\s*["']?[^\s"',}]{8,}`),
	regexp.MustCompile(`(?i)\b(authorization|x-api-key|proxy-authorization)\s*:\s*\S+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// RedactContent scrubs credential-shaped substrings from captured transcript text
// and caps its length. cap<=0 means no cap. The cap is applied AFTER scrubbing so
// a secret near the end cannot survive by being truncated into place.
func RedactContent(s string, cap int) string {
	for _, re := range contentSecrets {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			// Keep the assignment's name so a diff still shows WHAT was set.
			if i := strings.IndexAny(m, ":="); i > 0 {
				return m[:i+1] + " " + Redacted
			}
			return Redacted
		})
	}
	if cap > 0 && len(s) > cap {
		for cap > 0 && !isRuneStart(s[cap]) {
			cap--
		}
		return s[:cap] + "\n…[truncated: content capture cap reached]"
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
