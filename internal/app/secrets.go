package app

import (
	"bufio"
	"context"
	"regexp"
	"strings"
)

type secretRule struct {
	name string
	re   *regexp.Regexp
}

var secretRules = []secretRule{
	{"Anthropic API key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"OpenAI API key", regexp.MustCompile(`\bsk-(proj-|svcacct-)?[A-Za-z0-9_\-]{32,}`)},
	{"GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})`)},
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"Stripe key", regexp.MustCompile(`\b[rs]k_live_[0-9A-Za-z]{20,}`)},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"credential assignment", regexp.MustCompile(`(?i)["']?[a-z0-9_]*(api[_-]?key|secret|token|passw(or)?d|auth)[a-z0-9_]*["']?\s*[:=]\s*["'][A-Za-z0-9_\-/+=.]{20,}["']`)},
}

const allowMarker = "claudesync:allow"

type secretHit struct {
	file string // repo-relative path
	rule string
	line string // redacted
}

func redact(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	for _, r := range secretRules {
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			if len(m) <= 8 {
				return "****"
			}
			return m[:4] + strings.Repeat("*", 8)
		})
	}
	return s
}

// scanStaged checks the lines added in the index for likely secrets.
func scanStaged(ctx context.Context, g gitRepo, allow []string) ([]secretHit, error) {
	out, err := g.run(ctx, "-c", "core.quotePath=false", "diff", "--cached", "-U0", "--no-color", "--no-ext-diff", "--no-renames")
	if err != nil {
		return nil, err
	}
	var allowRes []*regexp.Regexp
	for _, a := range allow {
		if re, err := regexp.Compile(a); err == nil {
			allowRes = append(allowRes, re)
		}
	}
	var hits []secretHit
	file := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
lines:
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			continue
		case strings.HasPrefix(line, "+") && file != "" && file != "/dev/null":
			added := line[1:]
			if strings.Contains(added, allowMarker) {
				continue
			}
			for _, re := range allowRes {
				if re.MatchString(added) {
					continue lines
				}
			}
			for _, r := range secretRules {
				if r.re.MatchString(added) {
					hits = append(hits, secretHit{file: file, rule: r.name, line: redact(added)})
					break
				}
			}
		}
	}
	return hits, sc.Err()
}
