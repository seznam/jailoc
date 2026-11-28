package netbroker

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UpstreamStatus is the public, credential-free broker/helper state. Generation
// identifies the last successfully published route, including after a failure.
type UpstreamStatus struct {
	Version     int       `json:"version"`
	State       string    `json:"state"`
	Mode        string    `json:"mode,omitempty"`
	URL         string    `json:"url,omitempty"`
	Generation  uint64    `json:"generation"`
	CAFile      string    `json:"ca_file,omitempty"`
	HeaderNames []string  `json:"header_names,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastStderr  string    `json:"last_stderr,omitempty"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
	Updated     time.Time `json:"updated"`
}

// ReadUpstreamStatus reads a broker-written status snapshot, never headers.
func ReadUpstreamStatus(path string) (UpstreamStatus, error) {
	f, err := os.Open(path) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return UpstreamStatus{}, fmt.Errorf("open upstream status: %w", err)
	}
	defer func() { _ = f.Close() }()
	var s UpstreamStatus
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&s); err != nil {
		return s, fmt.Errorf("read upstream status: %w", err)
	}
	if s.Version != 1 {
		return s, errors.New("upstream status: unsupported version")
	}
	return s, nil
}

type upstreamManager struct {
	b          *Broker
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	config     *UpstreamProxy
	status     UpstreamStatus
	recovering bool
	backoff    time.Duration
	backoffMin time.Duration
}

func (b *Broker) startUpstream(ctx context.Context) error {
	m := b.upstreamState
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.config = b.policy.Load().upstreamProxy
	if m.config == nil {
		return m.publish(nil)
	}
	m.status.Mode = "static"
	if m.config.Command != "" {
		m.status.Mode = "command"
	} else {
		m.status.URL = m.config.URL
	}
	r, stderr, err := m.build(m.ctx, m.config, "start")
	if err != nil {
		m.failure("failed", err, stderr)
		return fmt.Errorf("start upstream: %w; stderr: %s", err, stderr)
	}
	if err := m.publish(r); err != nil {
		m.failure("failed", err, stderr)
		return fmt.Errorf("publish upstream: %w", err)
	}
	return nil
}

func (m *upstreamManager) build(ctx context.Context, u *UpstreamProxy, event string) (*upstreamRoute, string, error) {
	r := &upstreamRoute{workspace: u.Workspace, url: u.URL, mode: "static", headers: make(http.Header)}
	caPaths := []string{u.CA}
	direct := append([]string{}, u.Direct...)
	stderr := ""
	if u.Command != "" {
		r.mode = "command"
		out, runErr := runUpstreamHelper(ctx, u.Command, u.Workspace, event, upstreamHelperTimeout)
		var result *upstreamResult
		var parseErr error
		if !out.stdoutOverflow {
			result, parseErr = parseUpstreamResult(out.stdout)
		}
		known := make(http.Header)
		if result != nil {
			for key, value := range result.Headers {
				known.Add(key, value)
			}
		}
		old := m.b.upstream.Load()
		switch {
		case out.stdoutOverflow && out.stderr != "":
			stderr = "[stderr omitted: helper stdout exceeded 64 KiB]"
		case out.stderrOverflow:
			stderr = "[stderr omitted: helper stderr exceeded 16 KiB]"
		case result != nil && old != nil:
			stderr = redactUpstream(out.stderr, known, old.headers)
		case result != nil:
			stderr = redactUpstream(out.stderr, known)
		case len(bytes.TrimSpace(out.stdout)) == 0 && old != nil:
			stderr = redactUpstream(out.stderr, old.headers)
		case len(bytes.TrimSpace(out.stdout)) == 0:
			stderr = redactUpstream(out.stderr)
		case out.stderr != "":
			stderr = "[stderr omitted: helper stdout was not valid JSON]"
		}
		if runErr != nil {
			return nil, stderr, runErr
		}
		if parseErr != nil {
			return nil, stderr, parseErr
		}
		r.url = result.URL
		r.headers, parseErr = validateUpstreamHeaders(result.Headers)
		if parseErr != nil {
			return nil, stderr, parseErr
		}
		caPaths = append(caPaths, result.CA)
		direct = append(direct, result.Direct...)
	}
	if u.Auth != nil {
		if m.b.cfg.ResolveUpstreamAuth == nil {
			return nil, "", errors.New("upstream auth configured but no ResolveUpstreamAuth resolver was provided")
		}
		value, err := m.b.cfg.ResolveUpstreamAuth(ctx, u.Workspace, *u.Auth)
		if err != nil {
			return nil, "", errors.New("upstream auth resolution failed (check the configured secret source)")
		}
		value = bytes.TrimRight(value, "\r\n")
		if len(value) == 0 || bytes.ContainsAny(value, "\r\n\x00") {
			return nil, "", errors.New("upstream auth password must be a nonempty single line")
		}
		r.headers.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString(append([]byte(u.Auth.Username+":"), value...)))
	}
	var err error
	r.endpoint, err = parseUpstreamEndpoint(r.url)
	if err != nil {
		return nil, stderr, err
	}
	r.direct, err = compileUpstreamDirect(direct)
	if err != nil {
		return nil, stderr, err
	}
	r.ca, err = readUpstreamCAs(caPaths)
	if err != nil {
		return nil, stderr, err
	}
	return r, stderr, nil
}

func readUpstreamCAs(paths []string) ([]byte, error) {
	out := []byte{}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		f, err := openUpstreamCAFile(path)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("read upstream ca: %w", err)
		}
		if len(data) > 1<<20 {
			return nil, errors.New("upstream ca exceeds 1 MiB")
		}
		count := 0
		for len(bytes.TrimSpace(data)) > 0 {
			data = bytes.TrimSpace(data)
			if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
				return nil, errors.New("upstream ca must contain only PEM certificates")
			}
			block, rest := pem.Decode(data)
			if block == nil || block.Type != "CERTIFICATE" {
				return nil, errors.New("upstream ca: invalid PEM certificate")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, errors.New("upstream ca: invalid certificate")
			}
			if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
				return nil, errors.New("upstream ca: not a CA certificate")
			}
			if !seen[string(block.Bytes)] {
				out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})...)
				seen[string(block.Bytes)] = true
			}
			count++
			data = rest
		}
		if count == 0 {
			return nil, errors.New("upstream ca: no certificates")
		}
	}
	return out, nil
}

func writeUpstreamFile(path string, data []byte) error {
	if path == "" {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".upstream-*")
	if err != nil {
		return fmt.Errorf("create upstream snapshot: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("chmod upstream snapshot: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write upstream snapshot: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync upstream snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close upstream snapshot: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("publish upstream snapshot: %w", err)
	}
	return nil
}

func (m *upstreamManager) writeStatus(s UpstreamStatus) error {
	s.Version = 1
	s.Updated = time.Now().UTC()
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode upstream status: %w", err)
	}
	if err := writeUpstreamFile(m.b.cfg.UpstreamState, data); err != nil {
		return err
	}
	m.status = s
	return nil
}

func (m *upstreamManager) publish(r *upstreamRoute) error {
	return m.publishWithPolicy(m.b.policy.Load(), r, false)
}

func (m *upstreamManager) publishWithPolicy(p *Policy, r *upstreamRoute, recordPolicy bool) error {
	old := m.b.upstream.Load()
	wasPublished := m.status.Version != 0
	oldCA := []byte{}
	if old != nil {
		oldCA = old.ca
	}
	ca := []byte{}
	s := UpstreamStatus{State: "off", Generation: m.status.Generation + 1}
	if r != nil {
		ca = r.ca
		s.State, s.Mode, s.URL = "ready", r.mode, r.url
		s.HeaderNames = sortedHeaderNames(r.headers)
		if len(ca) > 0 {
			s.CAFile = m.b.cfg.UpstreamCA
		}
		r.generation = s.Generation
	}
	if err := writeUpstreamFile(m.b.cfg.UpstreamCA, ca); err != nil {
		return err
	}
	if err := m.writeStatus(s); err != nil {
		if rollbackErr := writeUpstreamFile(m.b.cfg.UpstreamCA, oldCA); rollbackErr != nil {
			m.b.record(map[string]any{"event": "upstream-state-error", "error": rollbackErr.Error()})
		}
		return err
	}
	m.b.setSnapshot(p, r)
	if recordPolicy {
		m.b.record(map[string]any{"event": "policy", "source": p.source, "deny_all": p.denyAll})
	}
	if wasPublished && !bytes.Equal(oldCA, ca) {
		m.b.status("upstream ca changed")
	}
	return nil
}

func (m *upstreamManager) failure(state string, err error, stderr string) {
	s := m.status
	message := err.Error()
	if route := m.b.upstream.Load(); route != nil {
		message = redactUpstream(message, route.headers)
		stderr = redactUpstream(stderr, route.headers)
	}
	s.State, s.LastError, s.LastStderr = state, message, stderr
	if e := m.writeStatus(s); e != nil {
		m.b.record(map[string]any{"event": "upstream-state-error", "error": e.Error()})
	}
}

func (b *Broker) reloadUpstream(ctx context.Context, p *Policy) map[string]any {
	m := b.upstreamState
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil || m.ctx.Err() != nil || p.denyAll {
		b.setSnapshot(p, b.upstream.Load())
		b.record(map[string]any{"event": "policy", "source": p.source, "deny_all": p.denyAll})
		ev := map[string]any{"upstream_generation": m.status.Generation}
		if m.status.LastError != "" {
			ev["upstream_error"] = m.status.LastError
		}
		if m.status.LastStderr != "" {
			ev["upstream_stderr"] = m.status.LastStderr
		}
		return ev
	}
	u := p.upstreamProxy
	oldConfig := m.config
	oldRoute := b.upstream.Load()
	event := map[string]any{}
	var r *upstreamRoute
	var stderr string
	var err error
	if u != nil {
		action := "reload"
		if oldConfig == nil || oldConfig.Command != u.Command {
			action = "start"
		}
		r, stderr, err = m.build(ctx, u, action)
	}
	if err == nil {
		err = m.publishWithPolicy(p, r, true)
	}
	if err != nil {
		m.failure("degraded", err, stderr)
		event["upstream_error"], event["upstream_stderr"] = m.status.LastError, m.status.LastStderr
		nextRoute := oldRoute
		// An upstream newly enabled on reload must not silently route directly.
		if nextRoute == nil && u != nil {
			nextRoute = &upstreamRoute{workspace: u.Workspace, mode: "blocked", headers: make(http.Header)}
			m.config = u
		}
		b.setSnapshot(p, nextRoute)
		b.record(map[string]any{"event": "policy", "source": p.source, "deny_all": p.denyAll})
	} else {
		m.config = u
		if oldConfig != nil && oldConfig.Command != "" && (u == nil || oldConfig.Command != u.Command) {
			m.stopHelper(oldConfig, oldRoute)
		}
	}
	event["upstream_generation"] = m.status.Generation
	return event
}

func (m *upstreamManager) stopHelper(u *UpstreamProxy, old *upstreamRoute) {
	out, err := runUpstreamHelper(context.Background(), u.Command, u.Workspace, "stop", 2*time.Second)
	stderr := out.stderr
	switch {
	case out.stdoutOverflow && stderr != "":
		stderr = "[stderr omitted: helper stdout exceeded 64 KiB]"
	case out.stderrOverflow:
		stderr = "[stderr omitted: helper stderr exceeded 16 KiB]"
	default:
		if r := m.b.upstream.Load(); r != nil {
			stderr = redactUpstream(stderr, r.headers)
		}
		if old != nil {
			stderr = redactUpstream(stderr, old.headers)
		}
	}
	if err != nil {
		m.b.record(map[string]any{"event": "upstream-stop-error", "error": err.Error(), "upstream_stderr": stderr})
	}
}

func (b *Broker) stopUpstream() {
	m := b.upstreamState
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.config != nil && m.config.Command != "" {
		m.stopHelper(m.config, b.upstream.Load())
	}
	if m.config != nil && m.status.State != "failed" {
		s := m.status
		s.State = "stopped"
		s.NextAttempt = time.Time{}
		if err := m.writeStatus(s); err != nil {
			b.record(map[string]any{"event": "upstream-state-error", "error": err.Error()})
		}
	}
}

func (b *Broker) upstreamFailed(r *upstreamRoute) {
	m := b.upstreamState
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.upstream.Load() != r || m.ctx == nil || m.ctx.Err() != nil {
		return
	}
	if r.mode != "command" && (r.mode != "blocked" || m.config == nil || m.config.Command == "") {
		if m.status.State != "unreachable" && r.mode != "blocked" {
			m.failure("unreachable", errUpstreamUnreachable, "")
		}
		return
	}
	if m.recovering {
		return
	}
	m.recovering = true
	m.wg.Add(1)
	go m.recover(r)
}

func (m *upstreamManager) recover(failed *upstreamRoute) {
	defer m.wg.Done()
	defer func() { m.mu.Lock(); m.recovering = false; m.mu.Unlock() }()
	for {
		m.mu.Lock()
		if m.ctx.Err() != nil || m.b.upstream.Load() != failed {
			m.mu.Unlock()
			return
		}
		minimum := m.backoffMin
		if minimum == 0 {
			minimum = time.Second
		}
		if m.backoff == 0 {
			m.backoff = minimum
		}
		delay := m.backoff
		m.backoff = min(m.backoff*2, 30*time.Second)
		s := m.status
		s.State = "recovering"
		if s.LastError == "" {
			s.LastError = errUpstreamUnreachable.Error()
		}
		s.NextAttempt = time.Now().Add(delay).UTC()
		if err := m.writeStatus(s); err != nil {
			m.b.record(map[string]any{"event": "upstream-state-error", "error": err.Error()})
		}
		m.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-m.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		m.mu.Lock()
		if m.ctx.Err() != nil || m.b.upstream.Load() != failed {
			m.mu.Unlock()
			return
		}
		r, stderr, err := m.build(m.ctx, m.config, "start")
		if err == nil {
			err = m.publish(r)
		}
		ev := map[string]any{"event": "upstream-recovery", "upstream_generation": m.status.Generation}
		if err != nil {
			m.failure("recovering", err, stderr)
			ev["upstream_error"], ev["upstream_stderr"] = err.Error(), stderr
		}
		m.b.record(ev)
		m.mu.Unlock()
		if err == nil {
			return
		}
	}
}

func (b *Broker) upstreamConnected(r *upstreamRoute) {
	m := b.upstreamState
	if !m.mu.TryLock() {
		return
	}
	defer m.mu.Unlock()
	if b.upstream.Load() != r {
		return
	}
	m.backoff = 0
	if m.status.State == "unreachable" {
		s := m.status
		s.State = "ready"
		s.LastError = ""
		s.NextAttempt = time.Time{}
		if err := m.writeStatus(s); err != nil {
			b.record(map[string]any{"event": "upstream-state-error", "error": err.Error()})
		}
	}
}
