package netbroker

import (
	"slices"
	"testing"
)

func TestCompilePathPattern(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		valid   bool
	}{
		{"/", true},
		{"/repos", true},
		{"/repos/", true},
		{"/repos/*", true},
		{"/repos/*/issues", true},
		{"/repos/**", true},
		{"/**", true},
		{"/a/*/b/**", true},
		{"/ünï/cødé", true},
		{"", false},
		{"repos", false},
		{"/repos//x", false},
		{"//", false},
		{"/a/**/b", false},
		{"/**/", false},
		{"/foo*", false},
		{"/*foo", false},
		{"/a/b*c", false},
		{"/a?b", false},
		{"/a#b", false},
		{"/a%2fb", false},
		{"/a\\b", false},
		{"/a\x00b", false},
		{"/a\nb", false},
		{"/a/./b", false},
		{"/a/../b", false},
		{"/..", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			t.Parallel()
			_, err := compilePathPattern(tt.pattern)
			if (err == nil) != tt.valid {
				t.Fatalf("compilePathPattern(%q) error = %v, want valid=%v", tt.pattern, err, tt.valid)
			}
		})
	}
}

func TestCanonicalPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		escaped string
		want    []string // nil: rejected
	}{
		{"/", []string{""}},
		{"/a", []string{"a"}},
		{"/a/b/", []string{"a", "b", ""}},
		{"/a%20b/c", []string{"a b", "c"}},
		{"/%41", []string{"A"}},
		{"/caf%C3%A9", []string{"café"}},
		{"", nil},
		{"a/b", nil},
		{"*", nil},
		{"//", nil},
		{"/a//b", nil},
		{"//a", nil},
		{"/a/%2F/b", nil},
		{"/a%2fb", []string{"a/b"}},
		{"/a%2Fb", []string{"a/b"}},
		{"/%40tarquinen%2Fopencode-dcp/latest", []string{"@tarquinen/opencode-dcp", "latest"}},
		{"/a%2Fb%2Fc/d", []string{"a/b/c", "d"}},
		{"/x%2F..%2Fadmin", nil},
		{"/x%2f..", nil},
		{"/..%2Fb", nil},
		{"/a/..%2Fb", nil},
		{"/%2E%2E/a", nil},
		{"/x%2F.%2Fy", nil},
		{"/%2Fa", nil},
		{"/a%2F", nil},
		{"/a%2F%2Fb", nil},
		{"/a%2F/b", nil},
		{"/a%5cb", nil},
		{"/a%5Cb", nil},
		{"/a\\b", nil},
		{"/a%00b", nil},
		{"/%2e%2e/etc", nil},
		{"/%2E%2E", nil},
		{"/a/%2e/b", nil},
		{"/a/./b", nil},
		{"/a/../b", nil},
		{"/a/..", nil},
		{"/a%zz", nil},
		{"/a%", nil},
		{"/a%0ab", nil},
		{"/a%7fb", nil},
	}
	for _, tt := range tests {
		t.Run(tt.escaped, func(t *testing.T) {
			t.Parallel()
			got, ok := canonicalPath(tt.escaped)
			if ok != (tt.want != nil) || !slices.Equal(got, tt.want) {
				t.Fatalf("canonicalPath(%q) = %q, %v; want %q", tt.escaped, got, ok, tt.want)
			}
		})
	}
}

func TestPathPatternMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"/repos/**", "/repos", true},
		{"/repos/**", "/repos/", true},
		{"/repos/**", "/repos/a", true},
		{"/repos/**", "/repos/a/b/c", true},
		{"/repos/**", "/reposx", false},
		{"/repos/**", "/other", false},
		{"/repos/*", "/repos/a", true},
		{"/repos/*", "/repos/a/", false},
		{"/repos/*", "/repos/", false},
		{"/repos/*", "/repos", false},
		{"/repos/*", "/repos/a/b", false},
		{"/repos/*/issues", "/repos/a/issues", true},
		{"/repos/*/issues", "/repos/a/pulls", false},
		{"/repos/*/issues/**", "/repos/a/issues", true},
		{"/repos/*/issues/**", "/repos/a/issues/1/comments", true},
		{"/", "/", true},
		{"/", "/a", false},
		{"/a", "/a", true},
		{"/a", "/a/", false},
		{"/a/", "/a/", true},
		{"/a/", "/a", false},
		{"/a", "/A", false},
		{"/**", "/", true},
		{"/**", "/a/b", true},
		{"/a b", "/a%20b", true},
		{"/*/latest", "/%40tarquinen%2Fopencode-dcp/latest", true},
		{"/*", "/a%2Fb", true},
		{"/a/*", "/a%2Fb", false},
		{"/@tarquinen/*", "/%40tarquinen%2Fopencode-dcp/latest", false},
		{"/@tarquinen/opencode-dcp/latest", "/%40tarquinen%2Fopencode-dcp/latest", false},
		{"/**", "/a%2Fb/c", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.path, func(t *testing.T) {
			t.Parallel()
			pp, err := compilePathPattern(tt.pattern)
			if err != nil {
				t.Fatalf("compile %q: %v", tt.pattern, err)
			}
			segs, ok := canonicalPath(tt.path)
			if !ok {
				t.Fatalf("canonicalPath(%q) rejected", tt.path)
			}
			if got := pp.match(segs); got != tt.want {
				t.Fatalf("pattern %q match %q = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestHTTPMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		match   HTTPMatch
		method  string
		path    string
		fold    bool
		want    bool
		invalid bool
	}{
		{name: "empty matches anything", match: HTTPMatch{}, method: "DELETE", path: "/x", want: true},
		{name: "star method", match: HTTPMatch{Methods: []string{"*"}}, method: "PATCH", path: "/", want: true},
		{name: "method only match", match: HTTPMatch{Methods: []string{"GET", "HEAD"}}, method: "HEAD", path: "/x", want: true},
		{name: "method only miss", match: HTTPMatch{Methods: []string{"GET", "HEAD"}}, method: "POST", path: "/x"},
		{name: "methods upper-cased", match: HTTPMatch{Methods: []string{"get"}}, method: "GET", path: "/x", want: true},
		{name: "lowercase request method is not GET", match: HTTPMatch{Methods: []string{"GET"}}, method: "get", path: "/x"},
		{name: "deny rules fold the request method", match: HTTPMatch{Methods: []string{"DELETE"}}, method: "delete", path: "/x", fold: true, want: true},
		{name: "path only match", match: HTTPMatch{Paths: []string{"/a/**"}}, method: "POST", path: "/a/b", want: true},
		{name: "path only miss", match: HTTPMatch{Paths: []string{"/a/**"}}, method: "GET", path: "/b"},
		{name: "any of several paths", match: HTTPMatch{Paths: []string{"/a", "/b/*"}}, method: "GET", path: "/b/c", want: true},
		{name: "method and path", match: HTTPMatch{Methods: []string{"GET"}, Paths: []string{"/a"}}, method: "GET", path: "/a", want: true},
		{name: "method hit path miss", match: HTTPMatch{Methods: []string{"GET"}, Paths: []string{"/a"}}, method: "GET", path: "/b"},
		{name: "method miss path hit", match: HTTPMatch{Methods: []string{"GET"}, Paths: []string{"/a"}}, method: "POST", path: "/a"},
		{name: "bad method", match: HTTPMatch{Methods: []string{"GE T"}}, invalid: true},
		{name: "empty method", match: HTTPMatch{Methods: []string{""}}, invalid: true},
		{name: "bad path", match: HTTPMatch{Paths: []string{"/a/**/b"}}, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := tt.match
			err := m.compile("allow", "r")
			if tt.invalid {
				if err == nil {
					t.Fatal("compile succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			segs, ok := canonicalPath(tt.path)
			if !ok {
				t.Fatalf("canonicalPath(%q) rejected", tt.path)
			}
			if got := m.matches(tt.method, segs, tt.fold); got != tt.want {
				t.Fatalf("matches(%s %s) = %v, want %v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestResplitPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		segs []string
		want []string // nil: no segment holds a slash
	}{
		{"plain", []string{"a", "b"}, nil},
		{"root", []string{""}, nil},
		{"encoded slash", []string{"a/b", "c"}, []string{"a", "b", "c"}},
		{"scoped package", []string{"@scope/name", "latest"}, []string{"@scope", "name", "latest"}},
		{"trailing slash kept", []string{"a/b", ""}, []string{"a", "b", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resplitPath(tt.segs); !slices.Equal(got, tt.want) {
				t.Fatalf("resplitPath(%q) = %q, want %q", tt.segs, got, tt.want)
			}
		})
	}
}

func TestHTTPMatchNeedsPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		match  HTTPMatch
		method string
		fold   bool
		want   bool
	}{
		{name: "empty rule", match: HTTPMatch{}, method: "GET"},
		{name: "methods only", match: HTTPMatch{Methods: []string{"*"}}, method: "GET"},
		{name: "paths for any method", match: HTTPMatch{Paths: []string{"/a"}}, method: "GET", want: true},
		{name: "paths for the method", match: HTTPMatch{Methods: []string{"GET"}, Paths: []string{"/a"}}, method: "GET", want: true},
		{name: "paths for another method", match: HTTPMatch{Methods: []string{"POST"}, Paths: []string{"/a"}}, method: "GET"},
		{name: "allow compares the method as sent", match: HTTPMatch{Methods: []string{"GET"}, Paths: []string{"/a"}}, method: "get"},
		{name: "deny folds the method", match: HTTPMatch{Methods: []string{"GET"}, Paths: []string{"/a"}}, method: "get", fold: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := tt.match
			if err := m.compile("allow", "r"); err != nil {
				t.Fatalf("compile: %v", err)
			}
			if got := m.needsPath(tt.method, tt.fold); got != tt.want {
				t.Fatalf("needsPath(%s) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}
