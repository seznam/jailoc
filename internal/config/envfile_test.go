package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "basic key=value",
			content: "KEY=value",
			want:    []string{"KEY=value"},
		},
		{
			name:    "double quoted value",
			content: `KEY="value with spaces"`,
			want:    []string{"KEY=value with spaces"},
		},
		{
			name:    "single quoted value",
			content: `KEY='single quoted'`,
			want:    []string{"KEY=single quoted"},
		},
		{
			name:    "inline comment with space before hash",
			content: "KEY=value # this is a comment",
			want:    []string{"KEY=value"},
		},
		{
			name:    "hash without preceding space is not a comment",
			content: "KEY=value#notcomment",
			want:    []string{"KEY=value#notcomment"},
		},
		{
			name:    "full line comment",
			content: "# this is a comment\nKEY=val",
			want:    []string{"KEY=val"},
		},
		{
			name:    "empty lines skipped",
			content: "\n\nKEY=val\n\n",
			want:    []string{"KEY=val"},
		},
		{
			name:    "bare key without equals is skipped",
			content: "BAREKEY\nKEY=val",
			want:    []string{"KEY=val"},
		},
		{
			name:    "empty value",
			content: "KEY=",
			want:    []string{"KEY="},
		},
		{
			name:    "value containing equals sign",
			content: "KEY=a=b=c",
			want:    []string{"KEY=a=b=c"},
		},
		{
			name:    "whitespace around key and value",
			content: "  KEY  =  trimmed  ",
			want:    []string{"KEY=trimmed"},
		},
		{
			name:    "double quoted value preserves inner spaces and hash",
			content: `KEY="value # not a comment"`,
			want:    []string{"KEY=value # not a comment"},
		},
		{
			name:    "single quoted value preserves dollar signs",
			content: "KEY='$OTHER'",
			want:    []string{"KEY=$OTHER"},
		},
		{
			name:    "inline comment after double quoted value",
			content: `KEY="val" # comment`,
			want:    []string{"KEY=val"},
		},
		{
			name: "multiple entries in file order",
			content: `# header
A=1
B="two"
C='three'
BARE
D=four # comment
`,
			want: []string{"A=1", "B=two", "C=three", "D=four"},
		},
		{
			name:    "empty file",
			content: "",
			want:    nil,
		},
		{
			name:    "only comments and blank lines",
			content: "# comment\n\n# another",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write test file: %v", err)
			}

			got, err := ParseEnvFile(path)
			if err != nil {
				t.Fatalf("ParseEnvFile() unexpected error: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("ParseEnvFile() returned %d entries, want %d\ngot:  %v\nwant: %v", len(got), len(tt.want), got, tt.want)
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseEnvFileUnreadable(t *testing.T) {
	t.Parallel()

	_, err := ParseEnvFile("/nonexistent/path/.env")
	if err == nil {
		t.Fatal("ParseEnvFile() expected error for nonexistent file, got nil")
	}

	if !strings.Contains(err.Error(), "reading env file") {
		t.Errorf("error message %q should contain 'reading env file'", err.Error())
	}
}
