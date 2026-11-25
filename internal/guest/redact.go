package guest

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// minSecretLen is the shortest secret value redacted from logs; shorter
// values would mask ordinary words and arguments.
const minSecretLen = 8

// redacted replaces secret values and matched credentials in logs.
const redacted = "[redacted]"

var (
	// credTokenRe matches well-known API token and key formats:
	// GitHub (ghp_, gho_, ghu_, ghs_, ghr_, github_pat_), GitLab (glpat-, gloas-),
	// OpenAI/Anthropic (sk-ant-, sk-proj-, sk-...), Slack (xox[baprs]-),
	// AWS access keys (AKIA, ASIA), Google API keys (AIza), HuggingFace (hf_),
	// npm (npm_), PyPI (pypi-), Stripe (sk_live_, rk_live_),
	// DigitalOcean (dop_v1_), and JWTs (eyJ...eyJ...).
	credTokenRe = regexp.MustCompile(
		`\b(?:` +
			`gh[pousr]_[A-Za-z0-9_]{20,}` +
			`|github_pat_[A-Za-z0-9_]{20,}` +
			`|gl(?:pat|oas)-[A-Za-z0-9_\-.]{16,}` +
			`|sk-ant-[A-Za-z0-9_\-]{16,}` +
			`|sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{20,}` +
			`|(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}` +
			`|xox[baprs]-[A-Za-z0-9\-]{10,}` +
			`|(?:AKIA|ASIA)[0-9A-Z]{16}` +
			`|AIza[0-9A-Za-z_\-]{35}` +
			`|hf_[A-Za-z0-9]{20,}` +
			`|npm_[A-Za-z0-9]{20,}` +
			`|pypi-[A-Za-z0-9_\-]{20,}` +
			`|dop_v1_[a-f0-9]{40,}` +
			`|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}` +
			`)\b`,
	)

	// credBearerRe matches Authorization Bearer/Basic credentials.
	credBearerRe = regexp.MustCompile(`(?i)(\b(?:authorization\s*[:=]\s*)?(?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]{8,}`)

	// credURLUserInfoRe matches user:password@ in URLs.
	credURLUserInfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)([^@\s/]{4,})(@)`)

	// credKeyValRe matches KEY=VALUE or KEY: VALUE where KEY names a secret/token/password/credential.
	credKeyValRe = regexp.MustCompile(
		`(?i)(\b[A-Za-z0-9_.-]*(?:SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|APIKEY|ACCESS_KEY|PRIVATE_KEY|CLIENT_SECRET|AUTH_TOKEN|CREDENTIALS?)[A-Za-z0-9_.-]*\s*[:=]\s*["']?)([^\s"',;]{8,})`,
	)
)

// RedactString returns s with every occurrence of values and common credential
// patterns replaced with [redacted].
func RedactString(s string, values []string) string {
	for _, v := range values {
		if v != "" {
			s = strings.ReplaceAll(s, v, redacted)
		}
	}
	return redactCredentialPatterns(s)
}

func redactCredentialPatterns(s string) string {
	s = credTokenRe.ReplaceAllString(s, redacted)
	s = credBearerRe.ReplaceAllString(s, "${1}"+redacted)
	s = credURLUserInfoRe.ReplaceAllString(s, "${1}"+redacted+"${3}")
	s = credKeyValRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := credKeyValRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		val := sub[2]
		if val == redacted || strings.HasPrefix(val, "/") || strings.HasPrefix(val, "./") || strings.HasPrefix(val, "~/") {
			return m
		}
		return sub[1] + redacted
	})
	return s
}

// redact returns argv with every occurrence of a value or credential pattern
// replaced. argv is not modified.
func redact(argv, values []string) []string {
	var out []string
	for i, a := range argv {
		r := RedactString(a, values)
		if r != a && out == nil {
			out = slices.Clone(argv)
		}
		if out != nil {
			out[i] = r
		}
	}
	if out == nil {
		return argv
	}
	return out
}

// CollectSecretValues extracts redaction values (>= 8 bytes, trimmed, whole
// values and individual lines) from raw file secret contents and KEY=VALUE
// environment entries, sorted longest first.
func CollectSecretValues(files [][]byte, env []string) []string {
	seen := map[string]bool{}
	add := func(v string) {
		for _, s := range append([]string{v}, strings.Split(v, "\n")...) {
			if s = strings.TrimSpace(s); len(s) >= minSecretLen {
				seen[s] = true
			}
		}
	}
	for _, data := range files {
		add(string(data))
	}
	for _, line := range env {
		if _, v, ok := strings.Cut(line, "="); ok {
			add(v)
		}
	}
	values := slices.Collect(maps.Keys(seen))
	slices.SortFunc(values, func(a, b string) int {
		if len(b) != len(a) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return values
}

// SecretsValues extracts the redaction values from a Secrets payload.
func SecretsValues(s Secrets) []string {
	files := make([][]byte, 0, len(s.Files))
	for _, v := range s.Files {
		files = append(files, v)
	}
	return CollectSecretValues(files, s.Env)
}

// MergeSecretValues returns the values of old and added without duplicates,
// longest first.
func MergeSecretValues(old, added []string) []string {
	values := slices.Concat(old, added)
	slices.Sort(values)
	values = slices.Compact(values)
	slices.SortStableFunc(values, func(a, b string) int { return len(b) - len(a) })
	return values
}

func mergeSecretValues(old, added []string) []string {
	return MergeSecretValues(old, added)
}
