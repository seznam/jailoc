package netbroker

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func TestDecideQuiet(t *testing.T) {
	t.Parallel()
	p, err := ParsePolicy([]byte(`{
		"deny": [
			{"name":"quiet-deny","names":["telemetry.example.com"],"quiet":true},
			{"name":"loud-deny","names":["ads.example.com"]}
		],
		"allow": [
			{"name":"quiet-allow","names":["registry.example.com"],"quiet":true},
			{"name":"quiet-cidr","cidrs":["198.51.100.0/24"],"quiet":true},
			{"name":"quiet-zone","names":["gitlab.example.com"],"quiet":true},
			{"name":"loud-allow","names":["*.example.org"]}
		]
	}`), "quiet")
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	env := func(names ...string) Env {
		return Env{
			Gateway: netip.MustParseAddr("192.168.127.1"),
			Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
			Names:   names,
		}
	}
	tests := []struct {
		name      string
		env       Env
		dst       string
		allow     bool
		wantQuiet bool
	}{
		{"allowed by quiet name rule", env("registry.example.com"), "93.184.216.34:443", true, true},
		{"allowed by quiet cidr rule", env(), "198.51.100.7:443", true, true},
		{"allowed by loud rule", env("www.example.org"), "93.184.216.34:443", true, false},
		{"denied by quiet deny rule", env("telemetry.example.com"), "93.184.216.34:443", false, true},
		{"quiet deny rule wins over quiet allow", env("telemetry.example.com", "registry.example.com"), "93.184.216.34:443", false, true},
		{"denied by loud deny rule", env("ads.example.com"), "93.184.216.34:443", false, false},
		{"default deny is never quiet", env("other.net"), "93.184.216.34:443", false, false},
		{"not-internal-zone is never quiet", env("gitlab.example.com"), "10.20.5.11:443", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := Decide(p, tt.env, ProtoTCP, netip.MustParseAddrPort(tt.dst))
			if d.Allow != tt.allow || d.Quiet != tt.wantQuiet {
				t.Fatalf("Decide = %+v, want allow=%v quiet=%v", d, tt.allow, tt.wantQuiet)
			}
		})
	}
}

func TestParsePolicyQuietField(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rule  string
		quiet bool
	}{
		{"quiet true", `{"name":"r","names":["example.com"],"quiet":true}`, true},
		{"quiet false", `{"name":"r","names":["example.com"],"quiet":false}`, false},
		{"quiet missing", `{"name":"r","names":["example.com"]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := ParsePolicy([]byte(`{"allow":[`+tt.rule+`]}`), "quiet")
			if err != nil {
				t.Fatalf("ParsePolicy: %v", err)
			}
			if got := p.allow[0].Quiet; got != tt.quiet {
				t.Fatalf("Quiet = %v, want %v", got, tt.quiet)
			}
		})
	}
}

func TestHTTPEvaluateQuiet(t *testing.T) {
	t.Parallel()
	withAllow := httpTestPolicy(t, `{"allow":[
		{"name":"web","names":["*"]},
		{"name":"read","names":["example.com"],"quiet":true,"http":{"methods":["GET"],"paths":["/v1/**"]}},
		{"name":"write","names":["example.com"],"http":{"methods":["POST"],"paths":["/v1/items"]}}
	],"deny":[
		{"name":"quiet-secrets","names":["example.com"],"quiet":true,"http":{"paths":["/v1/secrets/**"]}},
		{"name":"loud-admin","names":["example.com"],"http":{"paths":["/v1/admin/**"]}}
	]}`)
	denyOnly := httpTestPolicy(t, `{"allow":[{"name":"web","names":["*"]}],"deny":[
		{"name":"quiet-delete","names":["example.com"],"quiet":true,"http":{"methods":["DELETE"]}},
		{"name":"loud-put","names":["example.com"],"http":{"methods":["PUT"]}}
	]}`)
	dst := netip.MustParseAddrPort(l7IP + ":443")
	allowScope := withAllow.httpScope("example.com", dst)
	denyScope := denyOnly.httpScope("example.com", dst)

	tests := []struct {
		name      string
		scope     httpScope
		method    string
		path      string
		flowQuiet bool
		want      httpVerdict
	}{
		{"allowed by quiet http rule", allowScope, "GET", "/v1/items", false, httpVerdict{allow: true, rule: "read", quiet: true}},
		{"allowed by loud http rule under a quiet flow", allowScope, "POST", "/v1/items", true, httpVerdict{allow: true, rule: "write"}},
		{"denied by quiet http deny rule", allowScope, "GET", "/v1/secrets/a", false, httpVerdict{rule: "quiet-secrets", reason: "rule quiet-secrets", quiet: true}},
		{"denied by loud http deny rule under a quiet flow", allowScope, "GET", "/v1/admin/a", true, httpVerdict{rule: "loud-admin", reason: "rule loud-admin"}},
		{"http-no-match is never quiet", allowScope, "DELETE", "/v1/items", true, httpVerdict{reason: "http-no-match"}},
		{"bad-path is never quiet", allowScope, "GET", "/v1/a//b", true, httpVerdict{reason: "bad-path"}},
		{"no allow rules: request inherits a quiet flow", denyScope, "GET", "/x", true, httpVerdict{allow: true, rule: "web", quiet: true}},
		{"no allow rules: request inherits a loud flow", denyScope, "GET", "/x", false, httpVerdict{allow: true, rule: "web"}},
		{"no allow rules: quiet deny rule", denyScope, "DELETE", "/x", false, httpVerdict{rule: "quiet-delete", reason: "rule quiet-delete", quiet: true}},
		{"no allow rules: loud deny rule under a quiet flow", denyScope, "PUT", "/x", true, httpVerdict{rule: "loud-put", reason: "rule loud-put"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.scope.evaluate(tt.method, tt.path, "web", tt.flowQuiet); got != tt.want {
				t.Fatalf("evaluate(%s %s) = %+v, want %+v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

const (
	ruleWebQuiet      = `{"name":"web","names":["*"],"quiet":true}`
	ruleAPIReadQuiet  = `{"name":"api-read","names":["example.com"],"quiet":true,"http":{"methods":["GET","HEAD"],"paths":["/ok/**"]}}`
	ruleNoDeleteQuiet = `{"name":"no-delete","names":["example.com"],"quiet":true,"http":{"methods":["DELETE"]}}`
)

// eventQuiet reports the quiet flag of the single event matching match.
func eventQuiet(t *testing.T, e *l7Env, event string, match map[string]any) bool {
	t.Helper()
	evs := e.find(event, match)
	if len(evs) != 1 {
		t.Fatalf("%s events matching %v = %v\n%s", event, match, evs, e.log.String())
	}
	quiet, _ := evs[0]["quiet"].(bool)
	return quiet
}

func quietRequest(t *testing.T, e *l7Env, client *http.Client, method, path string, wantStatus int) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "https://example.com"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_ = readBody(t, resp)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d\n%s", method, path, resp.StatusCode, wantStatus, e.log.String())
	}
}

func TestInspectEventsQuiet(t *testing.T) {
	t.Parallel()
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })

	t.Run("flow and request allowed through a quiet flow rule", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(upstream)
		e.start(ruleWebQuiet, ruleNoDelete, true)
		quietRequest(t, e, e.client(false, 443), "GET", "/x", 200)
		if !eventQuiet(t, e, "flow", map[string]any{"allow": true, "rule": "web"}) {
			t.Error("flow allowed by a quiet rule is not quiet")
		}
		if !eventQuiet(t, e, "l7", map[string]any{"proto": "tls", "sni": "example.com"}) {
			t.Error("tls sniff event of a quiet flow is not quiet")
		}
		if !eventQuiet(t, e, "l7", map[string]any{"method": "GET", "path": "/x", "status": float64(200)}) {
			t.Error("request allowed through a quiet flow rule is not quiet")
		}
	})

	t.Run("loud flow rule keeps events loud", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(upstream)
		e.start(ruleWeb, ruleNoDelete, true)
		quietRequest(t, e, e.client(false, 443), "GET", "/x", 200)
		for _, ev := range e.events() {
			if ev["quiet"] != nil {
				t.Fatalf("event %v is quiet without a quiet rule", ev)
			}
		}
	})

	t.Run("request allowed and denied by a quiet http rule", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(upstream)
		e.start(ruleWeb+","+ruleAPIReadQuiet, "", true)
		client := e.client(false, 443)
		quietRequest(t, e, client, "GET", "/ok/a", 200)
		quietRequest(t, e, client, "POST", "/ok/a", 403)
		if !eventQuiet(t, e, "l7", map[string]any{"method": "GET", "path": "/ok/a", "status": float64(200)}) {
			t.Error("request allowed by a quiet http rule is not quiet")
		}
		if eventQuiet(t, e, "l7", map[string]any{"method": "POST", "status": float64(403), "reason": "http-no-match"}) {
			t.Error("http-no-match is quiet")
		}
		if eventQuiet(t, e, "flow", map[string]any{"allow": true, "rule": "web"}) {
			t.Error("flow allowed by a loud rule is quiet")
		}
	})

	t.Run("request denied by a quiet http deny rule", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(upstream)
		e.start(ruleWeb, ruleNoDeleteQuiet, true)
		quietRequest(t, e, e.client(false, 443), "DELETE", "/x", 403)
		if !eventQuiet(t, e, "l7", map[string]any{"method": "DELETE", "status": float64(403), "denied": true, "reason": "rule no-delete"}) {
			t.Error("request denied by a quiet http deny rule is not quiet")
		}
	})

	t.Run("denials by other reasons stay loud under a quiet flow", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(upstream)
		e.start(ruleWebQuiet+","+ruleAPIRead, "", true)
		client := e.client(false, 443)
		quietRequest(t, e, client, "GET", "/ok/a", 200)
		quietRequest(t, e, client, "POST", "/ok/a", 403)
		if !eventQuiet(t, e, "flow", map[string]any{"allow": true, "rule": "web"}) {
			t.Fatal("flow allowed by a quiet rule is not quiet")
		}
		if eventQuiet(t, e, "l7", map[string]any{"method": "POST", "status": float64(403), "reason": "http-no-match"}) {
			t.Error("http-no-match under a quiet flow is quiet")
		}
		if eventQuiet(t, e, "l7", map[string]any{"method": "GET", "path": "/ok/a", "status": float64(200)}) {
			t.Error("request allowed by a loud http rule under a quiet flow is quiet")
		}
	})

	t.Run("inspect-error stays loud under a quiet flow", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(upstream)
		e.start(ruleWebQuiet+","+ruleAPIRead, "", true)
		conn, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		other := newTestCA(t, "someone else")
		tc := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: other.pool, MinVersion: tls.VersionTLS12})
		if err := tc.Handshake(); err == nil {
			t.Fatal("handshake with an untrusted CA succeeded")
		}
		ev := e.waitEvent("inspect-error", map[string]any{"name": "example.com"})
		if ev["quiet"] != nil {
			t.Fatalf("inspect-error = %v, want not quiet", ev)
		}
	})

	t.Run("flow denied by a quiet deny rule", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(upstream)
		e.start(ruleWeb, `{"name":"no-example","names":["example.com"],"quiet":true}`, true)
		req, err := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := e.client(false, 443).Do(req); err == nil {
			_ = resp.Body.Close()
			t.Fatal("request to a denied host succeeded")
		}
		ev := e.waitEvent("flow", map[string]any{"allow": false, "rule": "no-example"})
		if ev["quiet"] != true {
			t.Fatalf("flow = %v, want quiet", ev)
		}
	})
}
