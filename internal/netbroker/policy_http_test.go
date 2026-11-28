package netbroker

import (
	"crypto/x509"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParsePolicyHTTP(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "ca")
	other := newTestCA(t, "other")
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	garbage := write("garbage.pem", "not pem")
	leafOnly := write("leaf.pem", string(pemCert(ca.serverCert(t, "example.com").Certificate[0])))
	inspect := func(cert, key string) string {
		return fmt.Sprintf(`"inspect":{"ca_cert":%q,"ca_key":%q}`, cert, key)
	}
	good := inspect(ca.certPath, ca.keyPath)
	httpAllow := `{"allow":[{"names":["example.com"],"http":{"methods":["GET"],"paths":["/a/**"]}}],`
	tests := []struct {
		name string
		json string
		want string // error substring; empty means valid
	}{
		{"http allow with inspect", httpAllow + good + `}`, ""},
		{"empty http table", `{"allow":[{"names":["example.com"],"http":{}}],` + good + `}`, ""},
		{"http deny with inspect", `{"allow":[{"names":["*"]}],"deny":[{"names":["example.com"],"http":{"methods":["DELETE"]}}],` + good + `}`, ""},
		{"inspect without http rules", `{"allow":[{"names":["example.com"]}],` + good + `}`, ""},
		{"http without inspect", httpAllow[:len(httpAllow)-1] + `}`, "inspect"},
		{"http deny without inspect", `{"deny":[{"names":["example.com"],"http":{}}]}`, "inspect"},
		{"missing ca cert file", httpAllow + inspect(filepath.Join(dir, "none.pem"), ca.keyPath) + `}`, "ca_cert"},
		{"missing ca key file", httpAllow + inspect(ca.certPath, filepath.Join(dir, "none.key")) + `}`, "ca_key"},
		{"empty paths", httpAllow + `"inspect":{}}`, "required"},
		{"garbage ca key", httpAllow + inspect(ca.certPath, garbage) + `}`, "PRIVATE KEY"},
		{"garbage ca cert", httpAllow + inspect(garbage, ca.keyPath) + `}`, "CERTIFICATE"},
		{"key of another ca", httpAllow + inspect(ca.certPath, other.keyPath) + `}`, "does not match"},
		{"ca cert is not a ca", httpAllow + inspect(leafOnly, ca.keyPath) + `}`, "not a CA"},
		{"upstream ca without certificates", httpAllow + `"inspect":{"ca_cert":` + fmt.Sprintf("%q,\"ca_key\":%q,\"upstream_ca\":%q}}", ca.certPath, ca.keyPath, garbage), "no certificates"},
		{"upstream ca missing", httpAllow + `"inspect":{"ca_cert":` + fmt.Sprintf("%q,\"ca_key\":%q,\"upstream_ca\":%q}}", ca.certPath, ca.keyPath, filepath.Join(dir, "none")), "upstream_ca"},
		{"http with udp", `{"allow":[{"names":["example.com"],"proto":"udp","http":{}}],` + good + `}`, "http requires proto tcp"},
		{"http with any", `{"allow":[{"names":["example.com"],"proto":"any","http":{}}],` + good + `}`, "http requires proto tcp"},
		{"bad path pattern", `{"allow":[{"names":["example.com"],"http":{"paths":["/a/**/b"]}}],` + good + `}`, "http path"},
		{"path without slash", `{"allow":[{"names":["example.com"],"http":{"paths":["a"]}}],` + good + `}`, "http path"},
		{"bad method", `{"allow":[{"names":["example.com"],"http":{"methods":["GE T"]}}],` + good + `}`, "http method"},
		{"unknown http field", `{"allow":[{"names":["example.com"],"http":{"hosts":["x"]}}],` + good + `}`, "unknown field"},
		{"unknown inspect field", `{"allow":[{"names":["example.com"]}],"inspect":{"ca":"x"}}`, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParsePolicy([]byte(tt.json), "test")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("ParsePolicy: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("ParsePolicy error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPolicyVersion(t *testing.T) {
	t.Parallel()
	if PolicyVersion != 4 {
		t.Fatalf("PolicyVersion = %d, want 4", PolicyVersion)
	}
}

func httpTestPolicy(t *testing.T, rules string) *Policy {
	t.Helper()
	ca := newTestCA(t, "ca")
	p, err := ParsePolicy([]byte(rules[:len(rules)-1]+`,"inspect":`+inspectSection(t, ca, nil)+`}`), "http-test")
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	return p
}

func TestHTTPRulesAtL4(t *testing.T) {
	t.Parallel()
	env := Env{
		Gateway: netip.MustParseAddr("192.168.127.1"),
		Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
		Names:   []string{"example.com"},
	}
	dst := netip.MustParseAddrPort(l7IP + ":443")

	t.Run("http deny rule never denies a flow or blocks DNS", func(t *testing.T) {
		t.Parallel()
		p := httpTestPolicy(t, `{"allow":[{"name":"web","names":["*"]}],"deny":[{"name":"no-delete","names":["example.com"],"http":{"methods":["DELETE"]}}]}`)
		if d := Decide(p, env, ProtoTCP, dst); !d.Allow {
			t.Fatalf("Decide = %+v, want allow", d)
		}
		if denied, _ := p.denied("example.com"); denied {
			t.Fatal("http deny rule blocks DNS")
		}
		if blocked, _ := p.blocked("example.com"); blocked {
			t.Fatal("http deny rule blocks DNS in rules mode")
		}
		if !p.needsNameInspection(dst, false) {
			t.Fatal("http deny rule does not make the flow inspected")
		}
	})
	t.Run("the same plain deny rule does", func(t *testing.T) {
		t.Parallel()
		p, err := ParsePolicy([]byte(`{"allow":[{"name":"web","names":["*"]}],"deny":[{"name":"no-delete","names":["example.com"]}]}`), "plain")
		if err != nil {
			t.Fatal(err)
		}
		if d := Decide(p, env, ProtoTCP, dst); d.Allow || d.Rule != "no-delete" {
			t.Fatalf("Decide = %+v, want denied by no-delete", d)
		}
		if denied, _ := p.denied("example.com"); !denied {
			t.Fatal("plain deny rule does not block DNS")
		}
	})
	t.Run("http deny rule is skipped for ipv6 and by address", func(t *testing.T) {
		t.Parallel()
		p := httpTestPolicy(t, `{"ipv6":true,"allow":[{"names":["*"]}],"deny":[{"name":"d","cidrs":["2606:4700::/32","93.184.216.0/24"],"http":{}}]}`)
		for _, a := range []string{"[2606:4700:4700::1111]:443", l7IP + ":443"} {
			if d := Decide(p, Env{Gateway: env.Gateway, Subnet: env.Subnet}, ProtoTCP, netip.MustParseAddrPort(a)); !d.Allow {
				t.Fatalf("Decide(%s) = %+v, want allow", a, d)
			}
		}
	})
	t.Run("http allow rule opens the flow and DNS, but not the plain view", func(t *testing.T) {
		t.Parallel()
		p := httpTestPolicy(t, `{"allow":[{"name":"api","names":["example.com"],"http":{"methods":["GET"]}}]}`)
		if d := Decide(p, env, ProtoTCP, dst); !d.Allow || d.Rule != "api" {
			t.Fatalf("Decide = %+v, want allow by api", d)
		}
		if blocked, _ := p.blocked("example.com"); blocked {
			t.Fatal("http allow rule does not open DNS")
		}
		if d := Decide(p.plain(), env, ProtoTCP, dst); d.Allow {
			t.Fatalf("plain view Decide = %+v, want deny", d)
		}
		if p.plain() == p || p.plain().inspect != nil {
			t.Fatal("plain view is not a separate policy without inspection")
		}
	})
	t.Run("plain view of a policy without http allow rules is the policy", func(t *testing.T) {
		t.Parallel()
		p := httpTestPolicy(t, `{"allow":[{"names":["*"]}],"deny":[{"names":["example.com"],"http":{}}]}`)
		if p.plain() != p {
			t.Fatal("plain view differs from the policy")
		}
	})
}

func TestNeedsNameInspectionHTTP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rules string
		port  uint16
		want  bool
	}{
		{"no http rules, plain allow", `{"allow":[{"names":["*"]}]}`, 443, false},
		{"http allow without ports", `{"allow":[{"names":["example.com"],"http":{}}]}`, 22, true},
		{"http allow on its port", `{"allow":[{"names":["example.com"],"ports":["8443"],"http":{}}]}`, 8443, true},
		{"http allow on another port", `{"allow":[{"names":["example.com"],"ports":["8443"],"http":{}}]}`, 443, false},
		{"http deny on its port", `{"allow":[{"names":["*"]}],"deny":[{"names":["example.com"],"ports":["443"],"http":{}}]}`, 443, true},
		{"http deny on another port", `{"allow":[{"names":["*"]}],"deny":[{"names":["example.com"],"ports":["443"],"http":{}}]}`, 80, false},
		{"plain deny by name still counts", `{"allow":[{"names":["*"]}],"deny":[{"names":["example.com"]}]}`, 443, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var p *Policy
			if strings.Contains(tt.rules, `"http"`) {
				p = httpTestPolicy(t, tt.rules)
			} else {
				var err error
				if p, err = ParsePolicy([]byte(tt.rules), "t"); err != nil {
					t.Fatal(err)
				}
			}
			if got := p.needsNameInspection(netip.AddrPortFrom(netip.MustParseAddr(l7IP), tt.port), false); got != tt.want {
				t.Fatalf("needsNameInspection = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPScopeAndEvaluate(t *testing.T) {
	t.Parallel()
	p := httpTestPolicy(t, `{"allow":[
		{"name":"web","names":["*"]},
		{"name":"read","names":["*.example.com"],"http":{"methods":["GET"],"paths":["/v1/**"]}},
		{"name":"write","names":["api.example.com"],"http":{"methods":["POST"],"paths":["/v1/items"]}},
		{"name":"by-ip","cidrs":["93.184.216.0/24"],"ports":["8080"],"http":{"methods":["GET"]}}
	],"deny":[
		{"name":"secrets","names":["api.example.com"],"http":{"paths":["/v1/secrets/**"]}}
	]}`)
	dst := netip.MustParseAddrPort
	names := func(rs []*Rule) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Name)
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name      string
		peer      string
		dst       string
		wantAllow string
		wantDeny  string
	}{
		{"wildcard name and exact name", "api.example.com", l7IP + ":443", "read,write", "secrets"},
		{"wildcard name only", "www.example.com", l7IP + ":443", "read", ""},
		{"other host has no scope", "example.org", l7IP + ":443", "", ""},
		{"apex is not a wildcard match", "example.com", l7IP + ":443", "", ""},
		{"address rule on its port", "example.org", l7IP + ":8080", "by-ip", ""},
		{"address rule on another port", "example.org", l7IP + ":443", "", ""},
		{"name and address", "api.example.com", l7IP + ":8080", "read,write,by-ip", "secrets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := p.httpScope(tt.peer, dst(tt.dst))
			if got := names(s.allow); got != tt.wantAllow {
				t.Fatalf("allow scope = %q, want %q", got, tt.wantAllow)
			}
			if got := names(s.deny); got != tt.wantDeny {
				t.Fatalf("deny scope = %q, want %q", got, tt.wantDeny)
			}
			if s.empty() != (tt.wantAllow == "" && tt.wantDeny == "") {
				t.Fatalf("empty() = %v", s.empty())
			}
		})
	}

	api := p.httpScope("api.example.com", dst(l7IP+":443"))
	www := p.httpScope("www.example.com", dst(l7IP+":443"))
	verdicts := []struct {
		name   string
		scope  httpScope
		method string
		path   string
		want   httpVerdict
	}{
		{"read allowed", api, "GET", "/v1/items", httpVerdict{allow: true, rule: "read"}},
		{"write allowed", api, "POST", "/v1/items", httpVerdict{allow: true, rule: "write"}},
		{"write path only", api, "POST", "/v1/other", httpVerdict{reason: "http-no-match"}},
		{"delete matches nothing", api, "DELETE", "/v1/items", httpVerdict{reason: "http-no-match"}},
		{"deny rule wins over allow", api, "GET", "/v1/secrets/a", httpVerdict{rule: "secrets", reason: "rule secrets"}},
		{"deny rule matches any method", api, "POST", "/v1/secrets", httpVerdict{rule: "secrets", reason: "rule secrets"}},
		{"deny rule does not reach another host", www, "GET", "/v1/secrets/a", httpVerdict{allow: true, rule: "read"}},
		{"no allow rules means the plain rule allowed", httpScope{deny: api.deny}, "GET", "/anything", httpVerdict{allow: true, rule: "flow-rule"}},
		{"no allow rules, deny matches", httpScope{deny: api.deny}, "GET", "/v1/secrets", httpVerdict{rule: "secrets", reason: "rule secrets"}},
	}
	for _, tt := range verdicts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.scope.evaluate(tt.method, tt.path, "flow-rule", false); got != tt.want {
				t.Fatalf("evaluate(%s %s) = %+v, want %+v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestHTTPEvaluatePaths(t *testing.T) {
	t.Parallel()
	const npm = "/%40tarquinen%2Fopencode-dcp/latest"
	ok := func(rule string) httpVerdict { return httpVerdict{allow: true, rule: rule} }
	badPath := httpVerdict{reason: "bad-path"}
	noMatch := httpVerdict{reason: "http-no-match"}
	denied := httpVerdict{rule: "d", reason: "rule d"}
	tests := []struct {
		name   string
		rules  string
		method string
		path   string
		want   httpVerdict
	}{
		// No path pattern in play: the path is not evaluated.
		{"methods-only rule allows the npm path", `{"allow":[{"name":"m","names":["example.com"],"http":{"methods":["*"]}}]}`, "GET", npm, ok("m")},
		{"methods-only rule, double slash", `{"allow":[{"name":"m","names":["example.com"],"http":{"methods":["*"]}}]}`, "GET", "//x", ok("m")},
		{"methods-only rule, encoded backslash", `{"allow":[{"name":"m","names":["example.com"],"http":{"methods":["*"]}}]}`, "GET", "/a%5Cb", ok("m")},
		{"methods-only rule, dot dot", `{"allow":[{"name":"m","names":["example.com"],"http":{"methods":["*"]}}]}`, "GET", "/..", ok("m")},
		{"methods-only rule, nul", `{"allow":[{"name":"m","names":["example.com"],"http":{}}]}`, "GET", "/a%00", ok("m")},
		{"methods-only deny rule leaves the path alone", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"methods":["DELETE"]}}]}`, "GET", "//x", ok("flow-rule")},
		{"methods-only deny rule still denies", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"methods":["DELETE"]}}]}`, "DELETE", "//x", denied},
		{"paths rule for another method does not canonicalize", `{"allow":[{"name":"a","names":["example.com"],"http":{"methods":["GET"],"paths":["/ok"]}}]}`, "POST", "//x", noMatch},
		{"deny paths for POST only: GET not canonicalized", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"methods":["POST"],"paths":["/a"]}}]}`, "GET", "//x", ok("flow-rule")},
		{"deny paths for POST only: POST canonicalized", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"methods":["POST"],"paths":["/a"]}}]}`, "POST", "//x", badPath},
		{"deny paths fold the method", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"methods":["POST"],"paths":["/a"]}}]}`, "post", "//x", badPath},
		{"allow paths do not fold the method", `{"allow":[{"name":"a","names":["example.com"],"http":{"methods":["POST"],"paths":["/a"]}}]}`, "post", "//x", noMatch},
		// A path pattern applies: the path is canonicalized.
		{"paths rule allows the npm path", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/*/latest"]}}]}`, "GET", npm, ok("a")},
		{"paths rule, star is one segment", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/*/latest"]}}]}`, "GET", "/a%2Fb/c/latest", noMatch},
		{"paths rule, doublestar takes the rest", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/**"]}}]}`, "GET", "/a%2Fb/c", ok("a")},
		{"paths rule, literal never matches an encoded slash", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/a/b"]}}]}`, "GET", "/a%2Fb", noMatch},
		{"paths rule, traversal through encoded slashes", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/**"]}}]}`, "GET", "/x%2F..%2Fadmin", badPath},
		{"paths rule, encoded dot dot", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/**"]}}]}`, "GET", "/%2E%2E/a", badPath},
		{"paths rule, leading encoded slash", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/**"]}}]}`, "GET", "/%2Fa", badPath},
		{"paths rule, double slash", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/**"]}}]}`, "GET", "//x", badPath},
		{"paths rule, encoded backslash", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/**"]}}]}`, "GET", "/a%5Cb", badPath},
		// Deny patterns also see the path split on decoded slashes.
		{"deny pattern vs encoded slash", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"paths":["/a/b"]}}]}`, "GET", "/a%2Fb", denied},
		{"deny pattern vs encoded slash, lowercase", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"paths":["/a/b"]}}]}`, "GET", "/a%2fb", denied},
		{"deny pattern vs plain path", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"paths":["/a/b"]}}]}`, "GET", "/a/b", denied},
		{"deny pattern, other encoded path passes", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"paths":["/a/b"]}}]}`, "GET", "/a%2Fc", ok("flow-rule")},
		{"deny prefix pattern vs encoded slash", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"paths":["/secrets/**"]}}]}`, "GET", "/secrets%2Fkey", denied},
		{"deny pattern vs npm path", `{"allow":[{"name":"w","names":["*"]}],"deny":[{"name":"d","names":["example.com"],"http":{"paths":["/@tarquinen/*/latest"]}}]}`, "GET", npm, denied},
		{"allow pattern uses raw segments only", `{"allow":[{"name":"a","names":["example.com"],"http":{"paths":["/a/b"]}}]}`, "GET", "/a%2Fb", noMatch},
		{"deny path rule canonicalizes with a methods-only allow rule", `{"allow":[{"name":"m","names":["example.com"],"http":{"methods":["*"]}}],"deny":[{"name":"d","names":["example.com"],"http":{"paths":["/a/b"]}}]}`, "GET", "/a%2Fb", denied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := httpTestPolicy(t, tt.rules)
			s := p.httpScope("example.com", netip.MustParseAddrPort(l7IP+":443"))
			if s.empty() {
				t.Fatal("scope is empty")
			}
			if got := s.evaluate(tt.method, tt.path, "flow-rule", false); got != tt.want {
				t.Fatalf("evaluate(%s %s) = %+v, want %+v", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestInspectorLeaf(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "jailoc inspection CA")
	insp, err := loadInspector(&inspectFile{CACert: ca.certPath, CAKey: ca.keyPath})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	insp.now = func() time.Time { return now }

	cachedNotAfter := func(t *testing.T, name string) {
		t.Helper()
		insp.mu.Lock()
		defer insp.mu.Unlock()
		el, ok := insp.leaves[name]
		if !ok {
			t.Fatalf("no cached leaf for %q", name)
		}
		if got := el.Value.(*leafEntry).notAfter; got != got.Round(0) {
			t.Fatalf("cached notAfter %v has a monotonic reading", got)
		}
	}

	verify := func(t *testing.T, name string, certDER []byte) *x509.Certificate {
		t.Helper()
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cert.Verify(x509.VerifyOptions{DNSName: name, Roots: ca.pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			t.Fatalf("leaf for %q does not verify against the CA: %v", name, err)
		}
		return cert
	}

	first, err := insp.leaf("api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	cert := verify(t, "api.github.com", first.Certificate[0])
	if got := cert.NotBefore; !got.Equal(now.Add(-time.Hour).Truncate(time.Second)) {
		t.Fatalf("NotBefore = %s, want now-1h", got)
	}
	wantNotAfter := now.Add(30 * 24 * time.Hour)
	if wantNotAfter.After(ca.cert.NotAfter) {
		wantNotAfter = ca.cert.NotAfter
	}
	if got := cert.NotAfter; !got.Equal(wantNotAfter.Truncate(time.Second)) {
		t.Fatalf("NotAfter = %s, want min(now+30d, CA expiry) = %s", got, wantNotAfter)
	}
	if cert.IsCA || cert.KeyUsage != x509.KeyUsageDigitalSignature || len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("leaf usage = ca:%v ku:%v eku:%v", cert.IsCA, cert.KeyUsage, cert.ExtKeyUsage)
	}
	if again, _ := insp.leaf("api.github.com"); again != first {
		t.Fatal("second lookup did not reuse the cached certificate")
	}
	cachedNotAfter(t, "api.github.com")

	t.Run("reissued with less than an hour left", func(t *testing.T) {
		insp.now = func() time.Time { return now.Add(30*24*time.Hour - 30*time.Minute) }
		reissued, err := insp.leaf("api.github.com")
		if err != nil {
			t.Fatal(err)
		}
		if reissued == first {
			t.Fatal("certificate near expiry was reused")
		}
		cachedNotAfter(t, "api.github.com")
		insp.now = func() time.Time { return now }
	})

	t.Run("capped at the CA expiry", func(t *testing.T) {
		late := ca.cert.NotAfter.Add(-2 * time.Hour)
		insp.now = func() time.Time { return late }
		defer func() { insp.now = func() time.Time { return now } }()
		capped, err := insp.leaf("capped.example.com")
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(capped.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		if !c.NotAfter.Equal(ca.cert.NotAfter) {
			t.Fatalf("NotAfter = %s, want the CA expiry %s", c.NotAfter, ca.cert.NotAfter)
		}
	})

	t.Run("ip literal gets an IP SAN", func(t *testing.T) {
		c, err := insp.leaf("93.184.216.34")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.IPAddresses) != 1 || len(parsed.DNSNames) != 0 {
			t.Fatalf("SANs = %v %v, want one IP", parsed.IPAddresses, parsed.DNSNames)
		}
	})

	t.Run("cache is bounded and evicts the least recently used", func(t *testing.T) {
		ins, err := loadInspector(&inspectFile{CACert: ca.certPath, CAKey: ca.keyPath})
		if err != nil {
			t.Fatal(err)
		}
		keep, _ := ins.leaf("keep.example.com")
		for i := range 2 * leafCacheSize {
			if _, err := ins.leaf(fmt.Sprintf("h%d.example.com", i)); err != nil {
				t.Fatal(err)
			}
			if i%10 == 0 {
				_, _ = ins.leaf("keep.example.com") // keep it recent
			}
		}
		if n := ins.lru.Len(); n != leafCacheSize || len(ins.leaves) != leafCacheSize {
			t.Fatalf("cache holds %d/%d entries, want %d", n, len(ins.leaves), leafCacheSize)
		}
		if again, _ := ins.leaf("keep.example.com"); again != keep {
			t.Fatal("recently used certificate was evicted")
		}
		if _, ok := ins.leaves["h0.example.com"]; ok {
			t.Fatal("least recently used certificate was kept")
		}
	})

	t.Run("concurrent issuance for one name yields one certificate", func(t *testing.T) {
		ins, err := loadInspector(&inspectFile{CACert: ca.certPath, CAKey: ca.keyPath})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		got := make([]any, 16)
		for i := range got {
			wg.Go(func() {
				c, err := ins.leaf("same.example.com")
				if err != nil {
					t.Error(err)
				}
				got[i] = c
			})
		}
		wg.Wait()
		for _, c := range got {
			if c != got[0] {
				t.Fatal("concurrent lookups returned different certificates")
			}
		}
	})
}

func TestHTTPCapable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info map[string]any
		want bool
	}{
		{"nil", nil, false},
		{"plain http", map[string]any{"proto": "http"}, true},
		{"tls without alpn", map[string]any{"proto": "tls", "sni": "a"}, true},
		{"tls h2", map[string]any{"proto": "tls", "alpn": []string{"h2"}}, true},
		{"tls http/1.1 among others", map[string]any{"proto": "tls", "alpn": []string{"foo", "http/1.1"}}, true},
		{"tls without http alpn", map[string]any{"proto": "tls", "alpn": []string{"foo", "imap"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := httpCapable(tt.info); got != tt.want {
				t.Fatalf("httpCapable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPNameRequirementSources(t *testing.T) {
	t.Parallel()
	dst := netip.MustParseAddrPort(l7IP + ":443")
	tests := []struct {
		name      string
		rules     string
		byName    bool
		wantRules bool // name rules need a name
		wantHTTP  bool // http rules need a name
		wantScope bool // an http rule covers the address
	}{
		{"name http rule only", `{"allow":[{"names":["api.github.com"],"http":{}}]}`, false, false, true, false},
		{"cidr http rule", `{"allow":[{"cidrs":["` + l7IP + `"],"http":{}}]}`, false, false, true, true},
		{"wildcard http rule", `{"allow":[{"names":["*"],"http":{}}]}`, false, false, true, true},
		{"http deny by name", `{"allow":[{"cidrs":["0.0.0.0/0"]}],"deny":[{"names":["example.com"],"http":{}}]}`, false, false, true, false},
		{"flow allowed by name", `{"allow":[{"names":["example.com"]},{"names":["api.github.com"],"http":{}}]}`, true, true, true, false},
		{"plain name deny rule", `{"allow":[{"cidrs":["0.0.0.0/0"]}],"deny":[{"names":["evil.test"]}]}`, false, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := httpTestPolicy(t, tt.rules)
			if got := p.needsNameForRules(dst, tt.byName); got != tt.wantRules {
				t.Fatalf("needsNameForRules = %v, want %v", got, tt.wantRules)
			}
			if got := p.needsNameForHTTP(dst); got != tt.wantHTTP {
				t.Fatalf("needsNameForHTTP = %v, want %v", got, tt.wantHTTP)
			}
			if got := !p.httpScope("", dst).empty(); got != tt.wantScope {
				t.Fatalf("address scope non-empty = %v, want %v", got, tt.wantScope)
			}
		})
	}
}
