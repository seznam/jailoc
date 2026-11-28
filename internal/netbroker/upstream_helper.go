package netbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const upstreamHelperTimeout = 10 * time.Second

type upstreamResult struct {
	Version int             `json:"version"`
	URL     string          `json:"url"`
	CA      string          `json:"ca,omitempty"`
	Direct  []string        `json:"direct,omitempty"`
	Headers upstreamHeaders `json:"headers,omitempty"`
}

type upstreamHeaders map[string]string

func (h *upstreamHeaders) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil {
		return errors.New("invalid headers")
	}
	if token == nil {
		*h = upstreamHeaders{}
		return nil
	}
	if token != json.Delim('{') {
		return errors.New("headers must be an object")
	}
	*h = upstreamHeaders{}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return errors.New("invalid header name")
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("invalid header name")
		}
		canonical := http.CanonicalHeaderKey(name)
		if seen[canonical] {
			return errors.New("duplicate header name")
		}
		seen[canonical] = true
		var value string
		if err := dec.Decode(&value); err != nil {
			return errors.New("header values must be strings")
		}
		(*h)[name] = value
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("invalid headers object")
	}
	return nil
}

func parseUpstreamResult(data []byte) (*upstreamResult, error) {
	var r upstreamResult
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&r); err != nil {
		return nil, errors.New("upstream helper: invalid JSON output")
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("upstream helper: trailing output")
	}
	if r.Version != 1 {
		return &r, errors.New("upstream helper: unsupported or missing version (want 1)")
	}
	if _, err := parseUpstreamEndpoint(r.URL); err != nil {
		return &r, err
	}
	if r.CA != "" && !filepath.IsAbs(r.CA) {
		return &r, errors.New("upstream helper: ca path must be absolute")
	}
	if _, err := compileUpstreamDirect(r.Direct); err != nil {
		return &r, err
	}
	if _, err := validateUpstreamHeaders(r.Headers); err != nil {
		return &r, err
	}
	return &r, nil
}

func validateUpstreamHeaders(input map[string]string) (http.Header, error) {
	h := make(http.Header, len(input))
	size := 0
	for key, value := range input {
		if key == "" {
			return nil, errors.New("upstream helper: empty header name")
		}
		for i := range len(key) {
			if !isTchar(key[i]) {
				return nil, errors.New("upstream helper: invalid HTTP header name")
			}
		}
		name := http.CanonicalHeaderKey(key)
		if strings.HasPrefix(strings.ToLower(key), "x-jailoc-") {
			return nil, errors.New("upstream helper: reserved X-Jailoc-* header")
		}
		switch name {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Proxy-Connection", "Trailer", "Upgrade":
			return nil, errors.New("upstream helper: framing header overrides are forbidden")
		}
		if _, ok := h[name]; ok {
			return nil, errors.New("upstream helper: duplicate case-insensitive header")
		}
		if strings.ContainsFunc(value, func(c rune) bool { return c < ' ' || c == 127 }) {
			return nil, errors.New("upstream helper: invalid header value")
		}
		size += len(key) + len(value) + 4
		if size > 48<<10 {
			return nil, errors.New("upstream helper: headers too large")
		}
		h.Set(name, value)
	}
	return h, nil
}

func sortedHeaderNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for key := range h {
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}

type cappedHelperOutput struct {
	data     []byte
	limit    int
	overflow bool
}

func (w *cappedHelperOutput) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, w.limit-len(w.data))
	w.data = append(w.data, p[:keep]...)
	w.overflow = w.overflow || keep < n
	return n, nil
}

type helperRunOutput struct {
	stdout         []byte
	stderr         string
	stdoutOverflow bool
	stderrOverflow bool
}

func runUpstreamHelper(ctx context.Context, command, workspace, event string, timeout time.Duration) (helperRunOutput, error) {
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(hctx, "/bin/sh", "-c", command) //nolint:gosec // G204: host-configured upstream helper command
	cmd.Dir = "/"
	cmd.Env = make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "JAILOC_WORKSPACE", "JAILOC_UPSTREAM_EVENT", "JAILOC_UPSTREAM_VERSION", "PWD":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "JAILOC_WORKSPACE="+workspace, "JAILOC_UPSTREAM_EVENT="+event, "JAILOC_UPSTREAM_VERSION=1", "PWD=/")
	stdout := &cappedHelperOutput{limit: 64 << 10}
	stderr := &cappedHelperOutput{limit: 16 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 200 * time.Millisecond
	if err := detachUpstreamHelper(cmd); err != nil {
		return helperRunOutput{}, err
	}
	err := cmd.Run()
	res := helperRunOutput{
		stdout:         stdout.data,
		stderr:         string(stderr.data),
		stdoutOverflow: stdout.overflow,
		stderrOverflow: stderr.overflow,
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		if killErr := cmd.Cancel(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return res, fmt.Errorf("stop pipe-holding upstream helper: %w", killErr)
		}
	}
	if hctx.Err() != nil {
		return res, fmt.Errorf("upstream helper %s timed out or cancelled: %w", event, hctx.Err())
	}
	if stdout.overflow && event != "stop" {
		return res, errors.New("upstream helper stdout exceeds 64 KiB")
	}
	if err != nil {
		return res, fmt.Errorf("upstream helper %s: %w", event, err)
	}
	return res, nil
}

func redactUpstream(text string, headers ...http.Header) string {
	secrets := []string{}
	for _, h := range headers {
		for _, values := range h {
			for _, v := range values {
				if encoded, ok := strings.CutPrefix(v, "Basic "); ok {
					if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
						if _, password, ok := strings.Cut(string(decoded), ":"); ok && password != "" {
							secrets = append(secrets, password)
						}
					}
				}
				if v != "" {
					secrets = append(secrets, v)
				}
				for _, part := range strings.Fields(v) {
					if len(part) >= 8 {
						secrets = append(secrets, part)
					}
				}
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return truncate(text, 16<<10)
}
