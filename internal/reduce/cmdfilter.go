package reduce

import (
	"fmt"
	"regexp"
	"strings"
)

// Deterministic, LLM-free compaction of KNOWN command outputs (rtk-style): keep the
// signal (failures, errors, summary) and drop routine noise. Lossy but reversible —
// the caller stores the original and applies only when strictly smaller. Ported from
// the reference prototype's cmdfilter.py.

type commandRule struct {
	name    string
	command *regexp.Regexp
	mode    string // "keep" | "strip"
	keep    []*regexp.Regexp
	drop    []*regexp.Regexp
	head    int
	tail    int
}

func ci(pats ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		out[i] = regexp.MustCompile("(?i)" + p)
	}
	return out
}

var (
	failPats = []string{`\berror\b`, `\bfailed\b`, `\bfailure`, `\bpanic`, `traceback`,
		`\bexception\b`, `assert`, `\bFAIL\b`, `^E\s`, `error\[`, `:\d+:\d+`}
	summaryPats = []string{`test result:`, `^\s*=+.*(passed|failed|error)`, `^tests?:\s`,
		`^ok\s`, `^---\s*(FAIL|PASS)`, `in \d+\.\d+\s*s`}
)

func failAndSummary(extra ...string) []*regexp.Regexp {
	return ci(append(append(append([]string{}, failPats...), summaryPats...), extra...)...)
}

var commandRules = []commandRule{
	{"pytest", regexp.MustCompile(`pytest|python -m pytest`), "keep",
		failAndSummary(`warnings? summary`), nil, 1, 6},
	{"cargo", regexp.MustCompile(`\bcargo\s+(test|build|check|clippy|run)`), "keep",
		failAndSummary(),
		ci(`^\s*Compiling\b`, `^\s*Finished\b`, `^\s*Running\b`, `^\s*Downloading\b`,
			`^\s*Updating\b`, `test .* \.\.\. ok`), 1, 6},
	{"gotest", regexp.MustCompile(`\bgo\s+(test|build|vet)`), "keep",
		failAndSummary(`no test files`), nil, 1, 6},
	{"jsnode", regexp.MustCompile(`\b(npm|pnpm|yarn)\s+(run\s+)?test|jest|vitest|mocha`), "keep",
		failAndSummary(`✕|✗|×|✘`), ci(`✓|√|PASS\b|passing`), 1, 6},
	{"tsc_lint", regexp.MustCompile(`\btsc\b|eslint|ruff|mypy|flake8`), "keep",
		ci(append(append([]string{}, failPats...), `warning`, `\d+\s+problems?`)...), nil, 1, 6},
	{"install", regexp.MustCompile(`\b(npm|pnpm|yarn)\s+(install|i|ci|add)\b|pip3?\s+install|apt-get|brew\s+install`), "strip",
		ci(failPats...),
		ci(`already satisfied`, `requirement already`, `^\s*Downloading`, `^\s*Collecting`,
			`added \d+ packages`, `^npm warn`, `^\s*\|`, `^\s*[-\\|/]\s*$`,
			`packages are looking for funding`), 1, 6},
	{"gitstatus", regexp.MustCompile(`\bgit\s+status`), "keep",
		ci(`modified|new file|deleted|renamed|untracked|both modified`, `branch|ahead|behind|up to date`),
		ci(`use "git`, `^\s*$`), 1, 6},
}

const minCmdFilterLines = 8

func matchesAny(line string, pats []*regexp.Regexp) bool {
	for _, p := range pats {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}

func ruleFor(command string) *commandRule {
	if command == "" {
		return nil
	}
	for i := range commandRules {
		if commandRules[i].command.MatchString(command) {
			return &commandRules[i]
		}
	}
	return nil
}

// compactCommandOutput filters output per the rule matching command; returns ("",
// false) if no rule matches, the output is too small, or nothing was removed.
func compactCommandOutput(command, output string) (string, bool) {
	rule := ruleFor(command)
	if rule == nil || output == "" {
		return "", false
	}
	lines := strings.Split(output, "\n")
	n := len(lines)
	if n < minCmdFilterLines {
		return "", false
	}
	var kept []string
	for i, line := range lines {
		if i < rule.head || i >= n-rule.tail {
			kept = append(kept, line)
			continue
		}
		isKeep := matchesAny(line, rule.keep)
		if rule.mode == "keep" {
			if isKeep {
				kept = append(kept, line)
			}
		} else { // strip
			if matchesAny(line, rule.drop) && !isKeep {
				continue
			}
			kept = append(kept, line)
		}
	}
	if len(kept) >= n {
		return "", false
	}
	dropped := n - len(kept)
	result := strings.Join(kept, "\n")
	cmd := command
	if len(cmd) > 60 {
		cmd = cmd[:60]
	}
	result += fmt.Sprintf("\n[labcx: %d routine line(s) filtered from `%s`]", dropped, cmd)
	return result, true
}
