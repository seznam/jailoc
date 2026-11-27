//go:build linux

package netbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const netbrokerMainHelperEnv = "JAILOC_TEST_NETBROKER_MAIN"

// TestNetbrokerMainHelperProcess is not a test: spawned as a child of
// TestMainAutomaticOAuthOpenWithRealLauncher it runs the real Main with the
// arguments after "--", so Main wires the real browser launcher. In a normal
// run the marker is absent and it returns at once.
func TestNetbrokerMainHelperProcess(t *testing.T) {
	if os.Getenv(netbrokerMainHelperEnv) != "1" {
		return
	}
	i := slices.Index(os.Args, "--")
	if i < 0 {
		fmt.Fprintln(os.Stderr, "netbroker helper: missing --")
		os.Exit(2)
	}
	if err := Main(os.Args[i+1:]); err != nil {
		fmt.Fprintln(os.Stderr, "netbroker helper: main:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestMainAutomaticOAuthOpenWithRealLauncher drives the real Main in a child
// process: a registration POSTed from a gVisor guest must make the real
// browser launcher run the host opener found in the child's own PATH, with
// exactly the registered URL, and answer 202.
func TestMainAutomaticOAuthOpenWithRealLauncher(t *testing.T) {
	t.Parallel()
	// Short paths keep the unix sockets under the sun_path limit.
	dir, err := os.MkdirTemp("", "nbo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	openedFile := filepath.Join(dir, "opened")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + openedFile + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "xdg-open"), []byte(script), 0o700); err != nil { //nolint:gosec // G306: executable test opener
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "nic.sock")
	policy := filepath.Join(dir, "policy.json")
	oauthSock := filepath.Join(dir, "oauth.sock")
	logFile := filepath.Join(dir, "broker.log")
	if err := os.WriteFile(policy, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	statusR, statusW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = statusR.Close() })
	var childOut syncBuf
	cmd := exec.Command(os.Args[0], "-test.run=^TestNetbrokerMainHelperProcess$", "--", //nolint:gosec // G204: re-executes this test binary
		"-socket", sock,
		"-policy", policy,
		"-log", logFile,
		"-status-fd", "3",
		"-loopback-relay-port", fmt.Sprint(testLoopbackRelayPort),
		"-oauth-callback-port", fmt.Sprint(testOAuthCallbackPort),
		"-oauth-socket", oauthSock,
	)
	// The child sees only the temporary opener directory in PATH: the real
	// launcher must find xdg-open there and nowhere else.
	cmd.Env = []string{netbrokerMainHelperEnv + "=1", "PATH=" + binDir}
	cmd.Stdout, cmd.Stderr = &childOut, &childOut
	cmd.ExtraFiles = []*os.File{statusW}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start netbroker child: %v", err)
	}
	_ = statusW.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var exitOnce sync.Once
	stopChild := func() {
		exitOnce.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-exited:
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					t.Errorf("child did not exit cleanly: %v\n%s", err, childOut.String())
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				// Reap the killed child. Its exit error is the expected kill signal,
				// so it is logged, not asserted.
				t.Logf("killed child reaped: %v", <-exited)
				t.Error("child ignored SIGTERM and was killed")
			}
		})
	}
	t.Cleanup(stopChild)

	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(statusR)
		if sc.Scan() {
			ready <- sc.Text()
		} else {
			ready <- ""
		}
		for sc.Scan() {
		}
	}()
	select {
	case line := <-ready:
		if line != "ready" {
			t.Fatalf("child status = %q, want ready; output:\n%s", line, childOut.String())
		}
	case err := <-exited:
		exited <- err
		t.Fatalf("child exited before ready: %v\n%s", err, childOut.String())
	case <-time.After(15 * time.Second):
		t.Fatalf("child never became ready; output:\n%s", childOut.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g := fakeGuest(t, ctx, sock)

	// The guest serves the callback behind its loopback relay.
	cbPort := freePort(t)
	const cbPath = "/cb"
	guestURI := make(chan string, 1)
	startFakeGuestLoopbackRelay(t, g, map[uint16]func(net.Conn){
		cbPort: func(c net.Conn) {
			req, err := http.ReadRequest(bufio.NewReader(c))
			if err != nil {
				return
			}
			guestURI <- req.URL.RequestURI()
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		},
	})

	authURL := "https://claude.ai/oauth/authorize?code=true&client_id=abc&response_type=code&redirect_uri=" +
		url.QueryEscape(fmt.Sprintf("http://localhost:%d%s", cbPort, cbPath)) + "&state=" + pendingSecretMark
	payload, err := json.Marshal(map[string]any{"port": cbPort, "path": cbPath, "url": authURL})
	if err != nil {
		t.Fatal(err)
	}
	code, body := registerPayloadFromGuestBody(t, ctx, g, payload)
	if code != http.StatusAccepted || body != "" {
		t.Fatalf("register status = %d body = %q, want 202 and empty; child output:\n%s", code, body, childOut.String())
	}

	// The launcher waits for the opener to exit, so the file is complete.
	data, err := os.ReadFile(openedFile) //nolint:gosec // G304: test temp file
	if err != nil {
		t.Fatalf("the real launcher never ran the PATH opener: %v\nchild output:\n%s", err, childOut.String())
	}
	if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !slices.Equal(got, []string{authURL}) {
		t.Fatalf("opener received %q, want exactly the registered URL once", got)
	}

	// The link stays pending for the CLI until the callback claims it.
	resp, err := unixClient(oauthSock).Get("http://oauth/pending")
	if err != nil {
		t.Fatalf("GET /pending: %v", err)
	}
	var links []OAuthLink
	err = json.NewDecoder(resp.Body).Decode(&links)
	_ = resp.Body.Close()
	if err != nil || len(links) != 1 || links[0].URL != authURL {
		t.Fatalf("pending = %+v (%v), want the registered link", links, err)
	}

	// The callback forwards to the guest.
	cbResp, err := (&http.Client{Timeout: 10 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d%s?code=abc", cbPort, cbPath))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	_ = cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", cbResp.StatusCode)
	}
	select {
	case uri := <-guestURI:
		if uri != cbPath+"?code=abc" {
			t.Fatalf("guest received %q, want %q", uri, cbPath+"?code=abc")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback never reached the guest")
	}

	stopChild()
	logged, err := os.ReadFile(logFile) //nolint:gosec // G304: test temp file
	if err != nil {
		t.Fatalf("read broker log: %v", err)
	}
	for what, text := range map[string]string{"broker log": string(logged), "child output": childOut.String()} {
		assertNoSecret(t, what, text)
	}
}
