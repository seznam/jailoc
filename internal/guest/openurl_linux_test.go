//go:build linux

package guest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInstallXDGOpenAndRegister(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := installXDGOpen(root); err != nil {
		t.Fatalf("installXDGOpen: %v", err)
	}
	target := filepath.Join(root, XDGOpenPath)
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat %s: %v", target, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("mode = %v, want executable", info.Mode())
	}
	content, err := os.ReadFile(target) //nolint:gosec // G304: test path inside t.TempDir()
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	if !strings.Contains(string(content), BinaryPath+" open-url") {
		t.Fatalf("script = %q, want open-url invocation", string(content))
	}

	const authURL = "https://auth.example.com/authorize?client_id=a%2Fb&redirect_uri=http%3A%2F%2Flocalhost%3A19876%2Fmcp%2Foauth%2Fcallback&scope=x+y%20z&state=%7E%2B"

	var gotPort uint16
	var gotPath, gotURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Port uint16 `json:"port"`
			Path string `json:"path"`
			URL  string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotPort, gotPath, gotURL = req.Port, req.Path, req.URL
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ts.Close)

	if err := registerOAuthCallback(ts.URL+"/register", 19876, "/mcp/oauth/callback", authURL); err != nil {
		t.Fatalf("registerOAuthCallback: %v", err)
	}
	if gotPort != 19876 || gotPath != "/mcp/oauth/callback" {
		t.Fatalf("registered (%d, %q), want (19876, /mcp/oauth/callback)", gotPort, gotPath)
	}
	if gotURL != authURL {
		t.Fatalf("registered url = %q, want byte-exact %q", gotURL, authURL)
	}
}

func TestRegisterOAuthCallbackNon2xxDoesNotEchoURLOrBody(t *testing.T) {
	t.Parallel()

	const authURL = "https://auth.example.com/authorize?state=secret-state-123&code_challenge=abc%2Fdef"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		http.Error(w, "reflected: "+string(body), http.StatusBadRequest)
	}))
	t.Cleanup(ts.Close)

	err := registerOAuthCallback(ts.URL+"/register", 19876, "/cb", authURL)
	if err == nil {
		t.Fatal("registerOAuthCallback: want error for 400 response")
	}
	for _, leak := range []string{authURL, "secret-state-123", "abc%2Fdef", "reflected"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q leaks %q", err.Error(), leak)
		}
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error %q, want status code", err.Error())
	}
}

func TestFirstURLArgReturnsExactRawArgument(t *testing.T) {
	t.Parallel()

	const raw = "https://auth.example.com/a?x=%2F&y=a+b#frag"
	if got := firstURLArg([]string{"--", "", raw, "other"}); got != raw {
		t.Fatalf("firstURLArg = %q, want %q", got, raw)
	}
}

// TestRegisterOAuthCallbackIgnoresHTTPProxy must not call t.Parallel: it uses
// t.Setenv. 0.0.0.0 is not loopback, so http.ProxyFromEnvironment would route
// it through HTTP_PROXY, while a direct dial on Linux reaches the loopback
// test server.
func TestRegisterOAuthCallbackIgnoresHTTPProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxied", http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ts.Close)

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	endpoint := strings.Replace(ts.URL, "127.0.0.1", "0.0.0.0", 1) + "/register"
	if err := registerOAuthCallback(endpoint, 19876, "/cb", "https://auth.example.com/authorize?state=1"); err != nil {
		t.Fatalf("registerOAuthCallback with HTTP_PROXY set: %v (proxy hits: %d)", err, proxyHits.Load())
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("proxy hits = %d, want 0", proxyHits.Load())
	}
	if hits.Load() != 1 {
		t.Fatalf("broker hits = %d, want 1", hits.Load())
	}
}

// TestXDGOpenWrapperPropagatesGuestStatus executes the installed wrapper with a
// fake guest binary in place of BinaryPath. It must forward exactly one quoted
// argument per input and exit with the guest's status, so a successful host
// launch is not masked as failure. Fake behavior is passed via cmd.Env rather
// than t.Setenv.
//
// The parent is deliberately not parallel and writes every executable fixture
// before any subtest runs: a fork in one parallel case would otherwise inherit
// another case's still-open-for-write CLOEXEC descriptor until its exec, and
// executing that file fails with ETXTBSY (golang/go#22315). Non-parallel tests
// finish before parallel tests resume, so no unrelated fork overlaps the writes
// either. Only the executions are parallel.
func TestXDGOpenWrapperPropagatesGuestStatus(t *testing.T) {
	root := t.TempDir()
	if err := installXDGOpen(root); err != nil {
		t.Fatalf("installXDGOpen: %v", err)
	}
	installed, err := os.ReadFile(filepath.Join(root, XDGOpenPath)) //nolint:gosec // G304: test path inside t.TempDir()
	if err != nil {
		t.Fatalf("read installed wrapper: %v", err)
	}

	const authURL = "https://auth.example.com/authorize?client_id=a&redirect_uri=http%3A%2F%2Flocalhost%3A19876%2Fcb&state=x y'z\"$q"

	tests := []struct {
		name   string
		status int
		args   []string

		wrapper string
		record  string
	}{
		{name: "success", status: 0, args: []string{authURL}},
		{name: "failure", status: 1, args: []string{authURL}},
		{name: "distinct status", status: 7, args: []string{authURL}},
		{name: "no arguments", status: 0, args: nil},
	}

	const fakeScript = "#!/bin/sh\n" +
		"{ printf '%s\\n' \"$#\"; for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } > \"$RECORD\"\n" +
		"exit \"$STATUS\"\n"
	for i := range tests {
		dir := t.TempDir()
		fake := filepath.Join(dir, "jailoc-guest")
		if err := os.WriteFile(fake, []byte(fakeScript), 0o700); err != nil { //nolint:gosec // G306: test script must be executable
			t.Fatalf("write fake guest: %v", err)
		}
		script := strings.Replace(string(installed), BinaryPath, fake, 1)
		if script == string(installed) {
			t.Fatalf("installed wrapper %q does not reference BinaryPath %q", installed, BinaryPath)
		}
		tests[i].wrapper = filepath.Join(dir, "xdg-open")
		if err := os.WriteFile(tests[i].wrapper, []byte(script), 0o700); err != nil { //nolint:gosec // G306: test script must be executable
			t.Fatalf("write wrapper: %v", err)
		}
		tests[i].record = filepath.Join(dir, "record")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.CommandContext(t.Context(), tt.wrapper, tt.args...) //nolint:gosec // G204: wrapper under t.TempDir()
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "RECORD=" + tt.record, "STATUS=" + strconv.Itoa(tt.status)}
			err := cmd.Run()

			gotStatus := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run wrapper: %v", err)
				}
				gotStatus = exitErr.ExitCode()
			}
			if gotStatus != tt.status {
				t.Fatalf("wrapper exit status = %d, want %d", gotStatus, tt.status)
			}

			recorded, err := os.ReadFile(tt.record) //nolint:gosec // G304: test path inside t.TempDir()
			if err != nil {
				t.Fatalf("read record: %v", err)
			}
			want := append([]string{"open-url"}, tt.args...)
			wantRecord := strconv.Itoa(len(want)) + "\n" + strings.Join(want, "\n") + "\n"
			if string(recorded) != wantRecord {
				t.Fatalf("fake guest saw %q, want %q", recorded, wantRecord)
			}
		})
	}
}

func TestOpenURLRejectsURLsThatAreNotOpened(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		leak string
	}{
		{name: "no arguments", args: nil},
		{name: "only separator and empty", args: []string{"--", ""}},
		{name: "non-OAuth URL", args: []string{"https://example.com/docs?token=secret-token-1"}, leak: "secret-token-1"},
		{name: "non-loopback redirect", args: []string{"https://auth.example.com/a?redirect_uri=https%3A%2F%2Fexample.com%2Fcb&state=secret-token-1"}, leak: "secret-token-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := OpenURL(tt.args)
			if err == nil {
				t.Fatal("OpenURL: want error because the URL is not opened, got nil")
			}
			if tt.leak != "" && strings.Contains(err.Error(), tt.leak) {
				t.Fatalf("error %q leaks %q", err.Error(), tt.leak)
			}
		})
	}
}

func TestRegisterOAuthCallbackBrowserLaunchFailureGuidance(t *testing.T) {
	t.Parallel()

	const authURL = "https://auth.example.com/authorize?state=secret-state-123"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		http.Error(w, "reflected: "+string(body), http.StatusBadGateway)
	}))
	t.Cleanup(ts.Close)

	err := registerOAuthCallback(ts.URL+"/register", 19876, "/cb", authURL)
	if err == nil {
		t.Fatal("registerOAuthCallback: want error for 502 response")
	}
	for _, leak := range []string{authURL, "secret-state-123", "reflected"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q leaks %q", err.Error(), leak)
		}
	}
	for _, want := range []string{"502", "could not open browser", "jailoc auth"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q, want it to contain %q", err.Error(), want)
		}
	}
}

// TestRegisterOAuthCallbackOnlyAcceptsHTTP202 pins the ack contract: only 202
// means the host accepted the URL for launch. Other 2xx statuses, including the
// 200 an older broker returned without opening anything, must not be reported
// as a successful open.
func TestRegisterOAuthCallbackOnlyAcceptsHTTP202(t *testing.T) {
	t.Parallel()

	const authURL = "https://auth.example.com/authorize?state=secret-state-123&code_challenge=abc%2Fdef"

	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "202 accepted", status: http.StatusAccepted},
		{name: "200 ok", status: http.StatusOK, wantErr: true},
		{name: "201 created", status: http.StatusCreated, wantErr: true},
		{name: "204 no content", status: http.StatusNoContent, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if tt.status != http.StatusNoContent {
					w.Header().Set("Content-Type", "text/plain")
				}
				w.WriteHeader(tt.status)
				if tt.status != http.StatusNoContent {
					_, _ = w.Write([]byte("reflected: " + string(body)))
				}
			}))
			t.Cleanup(ts.Close)

			err := registerOAuthCallback(ts.URL+"/register", 19876, "/cb", authURL)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("registerOAuthCallback: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("registerOAuthCallback: want error for status %d, got nil", tt.status)
			}
			for _, leak := range []string{authURL, "secret-state-123", "abc%2Fdef", "reflected"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("error %q leaks %q", err.Error(), leak)
				}
			}
			if !strings.Contains(err.Error(), strconv.Itoa(tt.status)) {
				t.Fatalf("error %q, want status %d", err.Error(), tt.status)
			}
		})
	}
}
