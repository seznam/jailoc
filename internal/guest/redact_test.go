package guest

import (
	"slices"
	"testing"
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
		{
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
