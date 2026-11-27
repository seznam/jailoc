package guest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRedactString(t *testing.T) {
	t.Parallel()
	values := []string{"my-custom-secret-value-long", "my-custom-secret"}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "configured secret values longest first",
			in:   "val1=my-custom-secret-value-long val2=my-custom-secret",
			want: "val1=[redacted] val2=[redacted]",
		},
		{
			name: "github personal access token",
			in:   "token: " + "ghp_" + "1234567890abcdefghij1234567890abcdef",
			want: "token: [redacted]",
		},
		{
			name: "github fine-grained pat",
			in:   "github_pat_" + "11AA22BB33CC44DD55EE66_77889900aabbccddeeff",
			want: "[redacted]",
		},
		{
			name: "gitlab pat",
			in:   "glpat-" + "abcdef1234567890ghij",
			want: "[redacted]",
		},
		{
			name: "anthropic key",
			in:   "sk-ant-" + "api03-abcdef1234567890ghijklmnop",
			want: "[redacted]",
		},
		{
			name: "openai key",
			in:   "sk-proj-" + "abcdef1234567890ghijklmnopqrstuvwxyz",
			want: "[redacted]",
		},
		{
			name: "aws access key",
			in:   "aws_key=" + "AKIA" + "IOSFODNN7EXAMPLE",
			want: "aws_key=[redacted]",
		},
		{
			name: "slack token",
			in:   "xoxb-" + "123456789012-1234567890123-abcdefghijklmnopqrstuvwx",
			want: "[redacted]",
		},
		{
			name: "jwt token",
			in:   "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ" + "." + "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			want: "[redacted]",
		},
		{
			name: "bearer header",
			in:   "Authorization: Bearer supersecretbearertoken123",
			want: "Authorization: Bearer [redacted]",
		},
		{ //nolint:gosec // G101: fake credential for the redaction test
			name: "url userinfo password",
			in:   "https://user:hunter2password@example.com/repo.git",
			want: "https://user:[redacted]@example.com/repo.git",
		},
		{
			name: "env style secret assignment",
			in:   "DATABASE_PASSWORD=supersecretdbpass API_TOKEN=\"anothersecrettoken\"",
			want: "DATABASE_PASSWORD=[redacted] API_TOKEN=\"[redacted]\"",
		},
		{
			name: "file path in secret env var is preserved",
			in:   "AWS_SHARED_CREDENTIALS_FILE=/run/secrets/aws-credentials",
			want: "AWS_SHARED_CREDENTIALS_FILE=/run/secrets/aws-credentials",
		},
		{
			name: "ordinary boot and OOM log lines untouched",
			in:   `time=2026-11-24T12:00:00Z level=WARN msg="out of memory: kernel killed a process" pid=271 process=gopls anon_rss_mib=1536 oom_score_adj=0`,
			want: `time=2026-11-24T12:00:00Z level=WARN msg="out of memory: kernel killed a process" pid=271 process=gopls anon_rss_mib=1536 oom_score_adj=0`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RedactString(tt.in, values); got != tt.want {
				t.Errorf("RedactString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSecretsValues(t *testing.T) {
	t.Parallel()
	s := Secrets{
		Env: []string{"API_TOKEN=env-secret-value", "SHORT=x"},
		Files: map[string][]byte{
			"token": []byte("file-secret-value\n"),
			"key":   []byte("-----BEGIN KEY-----\nline-of-key-data\n-----END KEY-----\n"),
			"tiny":  []byte("abc"),
		},
	}
	got := SecretsValues(s)
	for _, want := range []string{"file-secret-value", "env-secret-value", "line-of-key-data"} {
		if !slices.Contains(got, want) {
			t.Errorf("SecretsValues() = %q, missing %q", got, want)
		}
	}
	for _, unwanted := range []string{"abc", "x"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("SecretsValues() = %q, contains %q", got, unwanted)
		}
	}
}

const (
	testClaudeAuthURL = "https://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e" +
		"&response_type=code&redirect_uri=https%3A%2F%2Fconsole.anthropic.com%2Foauth%2Fcode%2Fcallback" +
		"&scope=org%3Acreate_api_key+user%3Aprofile&code_challenge=Zm9vYmFyYmF6cXV4&code_challenge_method=S256&state=st4t3val"
	testClaudeAuthRedacted = "https://claude.ai/oauth/authorize?[redacted]"
)

// oauthLeakMarkers must never appear in a redacted argv.
var oauthLeakMarkers = []string{
	"9d1c250a", "Zm9vYmFy", "st4t3val", "code_challenge", "client_id", "redirect_uri",
	"challenge-secret", "state-secret", "frag-secret", "tok-secret", "verifier-secret",
}

func TestRedactOAuthURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"standalone claude url", testClaudeAuthURL, testClaudeAuthRedacted},
		{"http scheme", "http://localhost:1455/auth/callback?code=abc&state=xyz", "http://localhost:1455/auth/callback?[redacted]"},
		{"uppercase scheme and key casing", "HTTPS://Example.com/Authorize?CLIENT_ID=x&Redirect_URI=y", "HTTPS://Example.com/Authorize?[redacted]"},
		{"encoded key names", "https://idp.example/x?%72edirect%5Furi=http%3A%2F%2Flocalhost&a=b", "https://idp.example/x?[redacted]"},
		{"double encoded key", "https://idp.example/x?%2572edirect%255Furi=zz", "https://idp.example/x?[redacted]"},
		{"camel case key", "https://idp.example/x?codeChallenge=challenge-secret", "https://idp.example/x?[redacted]"},
		{"fragment only token", "https://app.example/cb#access_token=tok-secret&token_type=bearer", "https://app.example/cb?[redacted]"},
		{"query and fragment", "https://idp.example/oauth2/auth?x=1#frag-secret", "https://idp.example/oauth2/auth?[redacted]"},
		{"fragment containing query", "https://app.example/cb#/x?code_challenge=challenge-secret", "https://app.example/cb?[redacted]"},
		{"oauth path with opaque query", "https://idp.example/oauth/authorize?opaque-state-secret", "https://idp.example/oauth/authorize?[redacted]"},
		{"oauth path malformed query", "https://idp.example/oauth/authorize?%zz&&==state-secret", "https://idp.example/oauth/authorize?[redacted]"},
		{"malformed uri no host", "https:///oauth/authorize?client_id=state-secret", "https:///oauth/authorize?[redacted]"},
		{"malformed strong key bad escapes", "https://idp.example/x?client_id=%ZZ%&state=state-secret", "https://idp.example/x?[redacted]"},
		{"weak keys together", "https://idp.example/x?state=state-secret&code=tok-secret", "https://idp.example/x?[redacted]"},
		{"empty authority edge", "https://?client_id=state-secret", "https://?[redacted]"},
		{"double quoted", `"` + testClaudeAuthURL + `"`, `"` + testClaudeAuthRedacted + `"`},
		{"single quoted", `'` + testClaudeAuthURL + `'`, `'` + testClaudeAuthRedacted + `'`},
		{"flag assignment", "--url=" + testClaudeAuthURL, "--url=" + testClaudeAuthRedacted},
		{
			name: "sh -c single quoted command with double quoted url",
			in:   `xdg-open "` + testClaudeAuthURL + `" >/dev/null 2>&1`,
			want: `xdg-open "` + testClaudeAuthRedacted + `" >/dev/null 2>&1`,
		},
		{
			name: "sh -c unquoted url followed by more words",
			in:   "xdg-open " + testClaudeAuthURL + " && echo done",
			want: "xdg-open " + testClaudeAuthRedacted + " && echo done",
		},
		{
			name: "nested escaped quotes",
			in:   `sh -c \"jailoc-guest open-url \'` + testClaudeAuthURL + `\'\"`,
			want: `sh -c \"jailoc-guest open-url \'` + testClaudeAuthRedacted + `\'\"`,
		},
		{
			name: "escaped double quotes",
			in:   `echo \"` + testClaudeAuthURL + `\" done`,
			want: `echo \"` + testClaudeAuthRedacted + `\" done`,
		},
		{
			name: "command substitution keeps closing paren",
			in:   "echo $(xdg-open " + testClaudeAuthURL + ")",
			want: "echo $(xdg-open " + testClaudeAuthRedacted + ")",
		},
		{
			name: "trailing sentence punctuation kept",
			in:   "open " + testClaudeAuthURL + ".",
			want: "open " + testClaudeAuthRedacted + ".",
		},
		{
			name: "backtick quoted",
			in:   "echo `xdg-open " + testClaudeAuthURL + "`",
			want: "echo `xdg-open " + testClaudeAuthRedacted + "`",
		},
		{
			name: "two urls in one string",
			in:   "a " + testClaudeAuthURL + " b http://localhost:1/cb?code=tok-secret&state=state-secret c",
			want: "a " + testClaudeAuthRedacted + " b http://localhost:1/cb?[redacted] c",
		},
		{
			name: "oauth url nested inside another url query",
			in:   "https://proxy.example/r?u=" + testClaudeAuthURL,
			want: "https://proxy.example/r?[redacted]",
		},
		{
			name: "trailing value replaced earlier",
			in:   "https://idp.example/oauth/authorize?client_id=[redacted]",
			want: "https://idp.example/oauth/authorize?[redacted]",
		},
		{"plain non-oauth url", "https://example.com/search?q=go&page=2", "https://example.com/search?q=go&page=2"},
		{"issue filter with single weak key", "https://github.com/seznam/jailoc/issues?state=open", "https://github.com/seznam/jailoc/issues?state=open"},
		{"url without query", "https://example.com/docs/oauth", "https://example.com/docs/oauth"},
		{"plain fragment", "https://example.com/docs#install", "https://example.com/docs#install"},
		{"non http scheme", "ftp://example.com/x?client_id=abc", "ftp://example.com/x?client_id=abc"},
		{"non url argument", "--client_id=abc code_challenge=def", "--client_id=abc code_challenge=def"},
		{"empty", "", ""},
		{"bare scheme", "https://", "https://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			argv := []string{"prog", tt.in}
			orig := slices.Clone(argv)
			got := redact(argv, nil)
			if got[1] != tt.want {
				t.Errorf("redact(%q) = %q, want %q", tt.in, got[1], tt.want)
			}
			if !slices.Equal(argv, orig) {
				t.Errorf("redact mutated its input: %q, want %q", argv, orig)
			}
			if tt.want != tt.in {
				for _, m := range oauthLeakMarkers {
					if strings.Contains(got[1], m) {
						t.Errorf("redact(%q) = %q, leaks %q", tt.in, got[1], m)
					}
				}
			}
			if again := redact(got, nil); !slices.Equal(again, got) {
				t.Errorf("redact is not idempotent: %q then %q", got, again)
			}
		})
	}
}

func TestRedactKeepsSecretRedactionWithOAuthURLs(t *testing.T) {
	t.Parallel()
	values := []string{"my-custom-secret-value"}
	argv := []string{
		"sh", "-c",
		"TOKEN=ghp_" + "1234567890abcdefghij1234567890abcdef my-custom-secret-value " + testClaudeAuthURL,
	}
	got := redact(argv, values)
	want := "TOKEN=[redacted] [redacted] " + testClaudeAuthRedacted
	if got[2] != want {
		t.Errorf("redact() = %q, want %q", got[2], want)
	}
	if got[0] != "sh" || got[1] != "-c" {
		t.Errorf("redact() changed untouched args: %q", got)
	}
}

func TestRedactOAuthURLInputUntouchedAndShared(t *testing.T) {
	t.Parallel()
	clean := []string{"ls", "https://example.com/a?b=c"}
	got := redact(clean, nil)
	if &got[0] != &clean[0] {
		t.Error("redact copied an argv that needed no change")
	}
	argv := []string{"xdg-open", testClaudeAuthURL}
	orig := slices.Clone(argv)
	got = redact(argv, nil)
	if !slices.Equal(argv, orig) {
		t.Errorf("redact mutated its input: %q", argv)
	}
	if got[0] != "xdg-open" || got[1] != testClaudeAuthRedacted {
		t.Errorf("redact() = %q", got)
	}
}

func TestExecRecordSerializesRedactedOAuthURL(t *testing.T) {
	t.Parallel()
	argv := []string{
		"xdg-open", testClaudeAuthURL,
		"sh", "-c", `jailoc-guest open-url "` + testClaudeAuthURL + `#frag-secret"`,
	}
	rec := ExecRecord{Time: time.Unix(0, 0).UTC(), PID: 1, UID: 1000, Argv: redact(argv, nil)}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal exec record: %v", err)
	}
	line := string(data)
	for _, m := range oauthLeakMarkers {
		if strings.Contains(line, m) {
			t.Errorf("serialized record %s leaks %q", line, m)
		}
	}
	if !strings.Contains(line, "https://claude.ai/oauth/authorize?[redacted]") {
		t.Errorf("serialized record %s lacks the redacted origin and path", line)
	}
}

func TestRedactOAuthURLRawDelimiters(t *testing.T) {
	t.Parallel()
	const (
		head = "https://auth.example/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A56580%2Fcallback"
		want = "https://auth.example/oauth/authorize?[redacted]"
	)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"standalone raw angles", head + "&code_challenge=<challenge-secret>&state=state-secret", want},
		{"standalone leading angle", head + "&code_challenge=<challenge-secret&state=state-secret", want},
		{"standalone trailing angle", head + "&code_challenge=challenge-secret>&state=state-secret>", want},
		{"standalone raw double quote", head + `&code_challenge=chal"lenge-secret&state=state-secret`, want},
		{"standalone raw single quote", head + "&code_challenge=chal'lenge-secret&state=state-secret", want},
		{"standalone raw backtick", head + "&code_challenge=chal`lenge-secret&state=state-secret", want},
		{"standalone raw quotes and angles", head + `&code_challenge=<"challenge-secret'>&state=` + "`state-secret`", want},
		{"standalone raw quote in fragment", head + "#frag-secret'<>\"", want},
		{"standalone raw whitespace", head + "&code_challenge=challenge-secret state-secret", want},
		{"standalone raw backslash quote", head + `&code_challenge=chal\"lenge-secret&state=state-secret`, want},
		{"standalone raw trailing quote", head + "&state=state-secret'", want},
		{"standalone path marker with angle tail", "https://auth.example/oauth/authorize?<challenge-secret>'state-secret", want},
		{"flag prefix with raw delimiters", "--url=" + head + `&code_challenge=<chal"lenge-secret'>&state=state-secret`, "--url=" + want},
		{"short flag value with raw delimiters", "--open-url=" + head + "&state=<state-secret>", "--open-url=" + want},
		{"malformed angle only", "http://localhost:1/cb?code=<tok-secret&state=state-secret>", "http://localhost:1/cb?[redacted]"},
		{"single quoted with angles", "'" + head + "&code_challenge=<challenge-secret>&state=state-secret'", "'" + want + "'"},
		{"double quoted with angles", `"` + head + "&code_challenge=<challenge-secret>&state=state-secret\"", `"` + want + `"`},
		{"single quoted with double quote inside", "'" + head + `&code_challenge=chal"lenge-secret&state=<state-secret>'`, "'" + want + "'"},
		{"double quoted with single quote inside", `"` + head + "&code_challenge=chal'lenge-secret&state=<state-secret>\"", `"` + want + `"`},
		{"backtick quoted with angles", "`" + head + "&state=<state-secret>`", "`" + want + "`"},
		{
			name: "shell command with single quoted angle url",
			in:   "xdg-open '" + head + "&code_challenge=<challenge-secret>&state=state-secret' >/dev/null",
			want: "xdg-open '" + want + "' >/dev/null",
		},
		{
			name: "shell command with double quoted angle url",
			in:   `jailoc-guest open-url "` + head + "&code_challenge=<challenge-secret>&state='state-secret'\" && true",
			want: `jailoc-guest open-url "` + want + `" && true`,
		},
		{
			name: "escaped quoted angle url",
			in:   `sh -c \"xdg-open \'` + head + `&code_challenge=<challenge-secret>&state=state-secret\'\"`,
			want: `sh -c \"xdg-open \'` + want + `\'\"`,
		},
		{
			name: "unquoted embedded url with raw quote and angle",
			in:   "xdg-open " + head + `&code_challenge=<chal"lenge-secret'>&state=state-secret`,
			want: "xdg-open " + want,
		},
		{
			name: "unterminated quoted url",
			in:   `xdg-open "` + head + "&code_challenge=<challenge-secret>&state=state-secret",
			want: `xdg-open "` + want,
		},
		{
			name: "ordinary url with angles untouched",
			in:   "https://example.com/search?q=<b>go</b>&x='y'",
			want: "https://example.com/search?q=<b>go</b>&x='y'",
		},
		{
			name: "ordinary quoted url untouched",
			in:   `sh -c "curl 'https://example.com/a?b=c' > out"`,
			want: `sh -c "curl 'https://example.com/a?b=c' > out"`,
		},
		{
			name: "ordinary url then oauth path url in one arg",
			in:   "https://example.com/a?b=c then https://idp.example/oauth/authorize?z=<challenge-secret>",
			want: "https://example.com/a?b=c then https://idp.example/oauth/authorize?[redacted]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			argv := []string{"prog", tt.in}
			orig := slices.Clone(argv)
			got := redact(argv, nil)
			if got[1] != tt.want {
				t.Errorf("redact(%q) = %q, want %q", tt.in, got[1], tt.want)
			}
			if !slices.Equal(argv, orig) {
				t.Errorf("redact mutated its input: %q, want %q", argv, orig)
			}
			if tt.want != tt.in {
				for _, m := range oauthLeakMarkers {
					if strings.Contains(got[1], m) {
						t.Errorf("redact(%q) = %q, leaks %q", tt.in, got[1], m)
					}
				}
			}
			if again := redact(got, nil); !slices.Equal(again, got) {
				t.Errorf("redact is not idempotent: %q then %q", got, again)
			}
		})
	}
}

func TestExecRecordSerializesRawDelimiterOAuthURL(t *testing.T) {
	t.Parallel()
	argv := []string{
		"xdg-open",
		"https://auth.example/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A56580%2Fcallback&code_challenge=<challenge-secret>&state=state-secret",
		"sh", "-c",
		`xdg-open "https://auth.example/oauth/authorize?code_challenge=<challenge-secret>&state='state-secret'"`,
	}
	rec := ExecRecord{Time: time.Unix(0, 0).UTC(), PID: 1, UID: 1000, Argv: redact(argv, nil)}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal exec record: %v", err)
	}
	for _, m := range oauthLeakMarkers {
		if strings.Contains(string(data), m) {
			t.Errorf("serialized record %s leaks %q", data, m)
		}
	}
}
