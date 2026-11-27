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
// replaced, and the query and fragment of every OAuth URL dropped (see
// redactOAuthURLs). argv is not modified.
func redact(argv, values []string) []string {
	var out []string
	for i, a := range argv {
		r := redactOAuthURLs(RedactString(a, values))
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

// oauthStrongKeys are normalized (lowercase, without "_" and "-") query and
// fragment parameter names that only occur in OAuth and OpenID flows; one of
// them marks the URL.
var oauthStrongKeys = map[string]bool{
	"clientid": true, "clientsecret": true, "clientassertion": true,
	"responsetype": true, "redirecturi": true, "requesturi": true,
	"codechallenge": true, "codechallengemethod": true, "codeverifier": true,
	"accesstoken": true, "idtoken": true, "refreshtoken": true,
	"idtokenhint": true, "nonce": true, "granttype": true, "assertion": true,
}

// oauthWeakKeys also occur in ordinary URLs (an issue list filtered by
// state); two distinct ones mark the URL.
var oauthWeakKeys = map[string]bool{
	"state": true, "code": true, "scope": true, "prompt": true,
	"loginhint": true, "audience": true, "resource": true,
}

// oauthPathSegments are URL path segments of OAuth endpoints and callbacks.
var oauthPathSegments = map[string]bool{
	"authorize": true, "authorise": true, "authorization": true, "auth": true,
	"token": true, "callback": true, "consent": true, "device": true,
}

// redactOAuthURLs replaces the whole query and fragment of every http(s) URL
// in s that looks like part of an OAuth flow with "?[redacted]", keeping the
// origin and path. It scans text without parsing, so malformed URLs, queries
// and escapes are redacted as well; whether a URL is valid never decides
// whether it is logged. Other URLs and text are unchanged.
//
// An argument that starts with a URL, or with a "--flag=" and a URL, is one
// candidate to its end: a standalone URL may carry raw quotes, angle brackets
// and backticks in its query, so no delimiter splits it. Otherwise s is a
// command line, and a URL in it runs to the quote that opened it (the quotes
// inside are part of it, as are angle brackets) or, unquoted, to the next
// whitespace.
func redactOAuthURLs(s string) string {
	if prefix := standaloneURLPrefix(s); prefix >= 0 {
		if r, ok := redactOAuthCandidate(s[prefix:]); ok {
			return s[:prefix] + r
		}
	}
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		start := nextHTTPURL(s, pos)
		if start < 0 {
			break
		}
		end, tail := urlEnd(s, start)
		pos = end
		r, ok := redactOAuthCandidate(s[start : end-len(tail)])
		if !ok {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(r)
		last = end - len(tail)
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// isNotFlagNameRune reports whether r cannot be part of a command-line flag
// name: letters, digits, "-" and "_" can.
func isNotFlagNameRune(r rune) bool {
	isName := r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	return !isName
}

// standaloneURLPrefix returns the index at which the URL of an argument that
// is one URL starts (0, or after a "--flag=" prefix), or -1.
func standaloneURLPrefix(s string) int {
	i := 0
	if strings.HasPrefix(s, "-") {
		eq := strings.IndexByte(s, '=')
		if eq < 0 || strings.IndexFunc(s[:eq], isNotFlagNameRune) >= 0 {
			return -1
		}
		i = eq + 1
	}
	if hasPrefixFold(s[i:], "http://") || hasPrefixFold(s[i:], "https://") {
		return i
	}
	return -1
}

// nextHTTPURL returns the index of the next "http://" or "https://" in s at
// or after from, ignoring case, or -1.
func nextHTTPURL(s string, from int) int {
	for i := from; i+7 <= len(s); i++ {
		if s[i]|0x20 != 'h' {
			continue
		}
		rest := s[i:]
		if hasPrefixFold(rest, "http://") || hasPrefixFold(rest, "https://") {
			return i
		}
	}
	return -1
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// urlEnd returns the end of the URL candidate starting at start in a command
// line, and the trailing text that is not part of it. A URL directly after a
// quote (an escaped one included) runs to the next quote of that kind; the
// backslashes that escape it are not part of the URL. Any other URL runs to
// whitespace or a control character, so quotes and angle brackets in a
// malformed query cannot cut it short and leave the rest in the log; the
// sentence punctuation, parentheses and backticks closing it are left out.
func urlEnd(s string, start int) (end int, tail string) {
	if q := openingQuote(s, start); q != 0 {
		end = len(s)
		if i := strings.IndexByte(s[start:], q); i >= 0 {
			end = start + i
		}
		return end, s[start+len(strings.TrimRight(s[start:end], "\\")) : end]
	}
	end = len(s)
	for i := start; i < len(s); i++ {
		if s[i] <= ' ' || s[i] == 0x7f {
			end = i
			break
		}
	}
	return end, trailingPunct(s[start:end])
}

// openingQuote returns the quote character right before start, looking past
// escaping backslashes, or 0.
func openingQuote(s string, start int) byte {
	i := start - 1
	for i >= 0 && s[i] == '\\' {
		i--
	}
	if i >= 0 && (s[i] == '"' || s[i] == '\'' || s[i] == '`') {
		return s[i]
	}
	return 0
}

// trailingPunct returns the run of sentence punctuation, closing parentheses
// and backticks ending s, which belongs to the surrounding text.
func trailingPunct(s string) string {
	i := len(s)
	for i > 0 && strings.IndexByte(".,;!)`", s[i-1]) >= 0 {
		i--
	}
	return s[i:]
}

// redactOAuthCandidate returns cand with its query and fragment replaced by
// "?[redacted]" when cand is an OAuth URL with a query or fragment.
func redactOAuthCandidate(cand string) (string, bool) {
	cut := strings.IndexAny(cand, "?#")
	if cut < 0 {
		return cand, false
	}
	base, rest := cand[:cut], cand[cut:]
	if !oauthPath(base) && !oauthParams(rest) {
		return cand, false
	}
	return base + "?" + redacted, true
}

// oauthPath reports whether the path of base (scheme://authority/path)
// names an OAuth endpoint.
func oauthPath(base string) bool {
	_, rest, _ := strings.Cut(base, "://")
	_, path, _ := strings.Cut(rest, "/")
	path = strings.ToLower(unescapeLenient(path))
	if strings.Contains(path, "oauth") {
		return true
	}
	for seg := range strings.FieldsFuncSeq(path, func(r rune) bool { return r == '/' || r == ';' }) {
		if oauthPathSegments[seg] {
			return true
		}
	}
	return false
}

// oauthParams reports whether the query and fragment text carries OAuth
// parameters: one strong key, or two distinct weak ones.
func oauthParams(rest string) bool {
	weak := map[string]bool{}
	for tok := range strings.FieldsFuncSeq(rest, func(r rune) bool { return r == '?' || r == '&' || r == ';' || r == '#' }) {
		key, _, _ := strings.Cut(unescapeLenient(tok), "=")
		key = strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
		switch {
		case oauthStrongKeys[key] || strings.HasPrefix(key, "oauth"):
			return true
		case oauthWeakKeys[key]:
			weak[key] = true
		}
	}
	return len(weak) >= 2
}

// unescapeLenient percent-decodes s up to three times, so doubly encoded
// names are seen, leaving invalid escapes as they are.
func unescapeLenient(s string) string {
	for range 3 {
		if !strings.Contains(s, "%") {
			break
		}
		var b strings.Builder
		changed := false
		for i := 0; i < len(s); i++ {
			if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
				b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
				i += 2
				changed = true
				continue
			}
			b.WriteByte(s[i])
		}
		if !changed {
			break
		}
		s = b.String()
	}
	return s
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}
