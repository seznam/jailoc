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

func TestResolveReleaseBuildCA(t *testing.T) {
	t.Parallel()

	sum := strings.Repeat("c", 64)
	pinned := agent.Release{
		Repo:    "owner/cli",
		Asset:   "cli_linux_{arch}.tar.gz",
		Binary:  "cli",
		Version: "1.0.0",
		SHA256:  map[string]string{"x64": "pinned-x64", "arm64": "pinned-arm64"},
	}
	cert, certPEM := newTLSTestCert(t)
	_, wrongPEM := newTLSTestCert(t)
	srv := newTLSTestServer(t, cert, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"assets":[
			{"name":"cli_linux_x64.tar.gz","digest":"sha256:` + sum + `"},
			{"name":"cli_linux_arm64.tar.gz","digest":"sha256:` + sum + `"}]}`))
	}))

	t.Run("succeeds with only the supplied CA", func(t *testing.T) {
		t.Parallel()
		got, err := resolveRelease(context.Background(), srv.URL, pinned, "2.0.0", mustBuildCA(t, certPEM))
		if err != nil {
			t.Fatalf("resolveRelease() error = %v", err)
		}
		want := map[string]string{"x64": sum, "arm64": sum}
		if got.Version != "2.0.0" || !maps.Equal(got.SHA256, want) {
			t.Fatalf("resolveRelease() = %+v, want checksums %v", got, want)
		}
	})

	t.Run("fails without a CA", func(t *testing.T) {
		t.Parallel()
		for _, ca := range [][]*BuildCA{nil, {nil}} {
			if _, err := resolveRelease(context.Background(), srv.URL, pinned, "2.0.0", ca...); err == nil || !strings.Contains(err.Error(), "certificate") {
				t.Fatalf("resolveRelease() without CA error = %v, want certificate error", err)
			}
		}
	})

	t.Run("fails with a wrong CA", func(t *testing.T) {
		t.Parallel()
		if _, err := resolveRelease(context.Background(), srv.URL, pinned, "2.0.0", mustBuildCA(t, wrongPEM)); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("resolveRelease() with wrong CA error = %v, want certificate error", err)
		}
	})

	t.Run("pinned version needs no fetch", func(t *testing.T) {
		t.Parallel()
		got, err := resolveRelease(context.Background(), srv.URL, pinned, "1.0.0", mustBuildCA(t, wrongPEM))
		if err != nil || !maps.Equal(got.SHA256, pinned.SHA256) {
			t.Fatalf("resolveRelease() = %+v, %v", got, err)
		}
	})

	t.Run("does not touch the default transport", func(t *testing.T) {
		t.Parallel()
		before := http.DefaultTransport.(*http.Transport).TLSClientConfig
		if _, err := resolveRelease(context.Background(), srv.URL, pinned, "2.0.0", mustBuildCA(t, certPEM)); err != nil {
			t.Fatalf("resolveRelease() error = %v", err)
		}
		if http.DefaultTransport.(*http.Transport).TLSClientConfig != before {
			t.Fatal("resolveRelease changed http.DefaultTransport")
		}
	})

	t.Run("rejects more than one CA", func(t *testing.T) {
		t.Parallel()
		ca := mustBuildCA(t, certPEM)
		if _, err := resolveRelease(context.Background(), srv.URL, pinned, "2.0.0", ca, ca); err == nil {
			t.Fatal("resolveRelease() with two CAs succeeded")
		}
	})
}
