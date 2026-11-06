package docker

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
)

func TestResolveRelease(t *testing.T) {
	t.Parallel()

	x64 := strings.Repeat("a", 64)
	arm64 := strings.Repeat("b", 64)
	pinned := agent.Release{
		Repo:    "owner/cli",
		Asset:   "cli_linux_{arch}.tar.gz",
		Binary:  "cli",
		Version: "1.0.0",
		SHA256:  map[string]string{"x64": "pinned-x64", "arm64": "pinned-arm64"},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/cli/releases/tags/2.0.0":
			_, _ = w.Write([]byte(`{"assets":[
				{"name":"cli_linux_x64.tar.gz","digest":"sha256:` + x64 + `"},
				{"name":"cli_linux_arm64.tar.gz","digest":"sha256:` + arm64 + `"},
				{"name":"cli_mac_arm64.tar.gz","digest":"sha256:` + x64 + `"}]}`))
		case "/repos/owner/cli/releases/tags/3.0.0":
			_, _ = w.Write([]byte(`{"assets":[{"name":"cli_linux_x64.tar.gz","digest":"sha256:` + x64 + `"}]}`))
		case "/repos/owner/cli/releases/tags/4.0.0":
			_, _ = w.Write([]byte(`{"assets":[
				{"name":"cli_linux_x64.tar.gz","digest":"sha256:` + x64 + `"},
				{"name":"cli_linux_arm64.tar.gz","digest":"md5:abc"}]}`))
		case "/repos/owner/cli/releases/tags/5.0.0":
			_, _ = w.Write([]byte(`{"assets":[
				{"name":"cli_linux_x64.tar.gz","digest":"sha256:` + x64 + `"},
				{"name":"cli_linux_arm64.tar.gz","digest":"sha256:$(reboot)"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	tests := []struct {
		version string
		want    map[string]string
		wantErr string
	}{
		{version: "1.0.0", want: pinned.SHA256},
		{version: "2.0.0", want: map[string]string{"x64": x64, "arm64": arm64}},
		{version: "3.0.0", wantErr: "has no asset cli_linux_arm64.tar.gz"},
		{version: "4.0.0", wantErr: "has no SHA-256 digest"},
		{version: "5.0.0", wantErr: "has no SHA-256 digest"},
		{version: "9.9.9", wantErr: "unexpected status 404"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()
			got, err := resolveRelease(context.Background(), ts.URL, pinned, tt.version)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveRelease() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRelease() error = %v", err)
			}
			if got.Version != tt.version || !maps.Equal(got.SHA256, tt.want) {
				t.Fatalf("resolveRelease() = %+v, want version %s with %v", got, tt.version, tt.want)
			}
		})
	}
	if pinned.SHA256["x64"] != "pinned-x64" {
		t.Fatal("resolveRelease changed the pinned checksums")
	}
}
