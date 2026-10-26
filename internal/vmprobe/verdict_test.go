package vmprobe

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testPlan(t *testing.T) *Plan {
	t.Helper()
	p, err := NewPlan(PlanOptions{Candidate: "test", Dir: t.TempDir(), PrimaryPort: 47801, AltPort: 47802, IPv6Mode: IPv6Disabled})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p
}

func fullReports(p *Plan) []*Report {
	var reps []*Report
	for _, c := range RequiredGuestContexts {
		reps = append(reps, &Report{Nonce: p.Nonce, Kind: "guest", Context: c, Results: []Result{
			{ID: "N-HOST-TCP", Key: "N-HOST-TCP#0", Expect: ExpectDeny, Observation: ObsBlocked, Status: StatusPass},
			{ID: "N-PUB-TLS-IP", Key: "N-PUB-TLS-IP#1", Expect: ExpectAllow, Observation: ObsReached, Status: StatusPass},
		}})
	}
	reps = append(reps,
		&Report{Nonce: p.Nonce, Kind: "selftest", Context: "host"},
		&Report{Nonce: p.Nonce, Kind: "ingress", Context: "host"},
		&Report{Nonce: p.Nonce, Kind: "watch", Context: CtxGuestRoot},
	)
	return reps
}

func statusOf(v *Verdict, id, ctxName string) []Status {
	var out []Status
	for _, r := range v.Results {
		if r.ID == id && (ctxName == "" || r.Context == ctxName) {
			out = append(out, r.Status)
		}
	}
	return out
}

func TestVerdictPassesCleanRun(t *testing.T) {
	t.Parallel()
	p := testPlan(t)
	v, err := BuildVerdict(p, fullReports(p), nil)
	if err != nil {
		t.Fatalf("BuildVerdict: %v", err)
	}
	if v.Failed() {
		t.Fatalf("clean run failed:\n%s", v.Markdown())
	}
	if !strings.Contains(v.Markdown(), "verdict: PASS") {
		t.Fatalf("markdown missing PASS header:\n%s", v.Markdown())
	}
}

func TestVerdictSentinelHitFailsDenyCheck(t *testing.T) {
	t.Parallel()
	p := testPlan(t)
	hits := []Hit{
		{Sentinel: "host", Proto: "tcp", Local: "192.168.1.2:47801", Remote: "192.168.1.9:5555", Payload: "JLP1 " + p.Nonce + " N-HOST-TCP#0 container-bridge"},
		{Sentinel: "host", Proto: "tcp", Local: "1.2.3.4:47801", Payload: "JLP1 " + p.Nonce + " N-PUB-TLS-IP#1 guest-root"},
		{Sentinel: "host", Proto: "udp", Payload: "JLP1 othernonce N-HOST-TCP#0 guest-root"},
	}
	v, err := BuildVerdict(p, fullReports(p), hits)
	if err != nil {
		t.Fatalf("BuildVerdict: %v", err)
	}
	if got := statusOf(v, "N-HOST-TCP", CtxContainer); !slices.Equal(got, []Status{StatusFail}) {
		t.Fatalf("container-bridge N-HOST-TCP = %v, want [FAIL]", got)
	}
	if got := statusOf(v, "N-HOST-TCP", CtxGuestRoot); !slices.Equal(got, []Status{StatusPass}) {
		t.Fatalf("guest-root N-HOST-TCP = %v, want [PASS] (foreign nonce must be ignored)", got)
	}
	if got := statusOf(v, "N-PUB-TLS-IP", CtxGuestRoot); !slices.Equal(got, []Status{StatusPass}) {
		t.Fatalf("allow check hit must not fail: %v", got)
	}
	if !v.Failed() {
		t.Fatal("verdict should fail")
	}
}

func TestVerdictWatchHitAndUnattributed(t *testing.T) {
	t.Parallel()
	p := testPlan(t)
	hits := []Hit{
		{Payload: "JLP1 " + p.Nonce + " L-FAILCLOSED#7 guest-root"},
		{Payload: "SSH-2.0-scanner"},
	}
	v, err := BuildVerdict(p, fullReports(p), hits)
	if err != nil {
		t.Fatalf("BuildVerdict: %v", err)
	}
	if got := statusOf(v, "L-FAILCLOSED-SENTINEL", ""); !slices.Equal(got, []Status{StatusFail}) {
		t.Fatalf("L-FAILCLOSED-SENTINEL = %v", got)
	}
	if got := statusOf(v, "S-UNATTRIBUTED", ""); !slices.Equal(got, []Status{StatusReview}) {
		t.Fatalf("S-UNATTRIBUTED = %v", got)
	}
}

func TestVerdictCoverageMissing(t *testing.T) {
	t.Parallel()
	p := testPlan(t)
	v, err := BuildVerdict(p, fullReports(p)[:1], nil)
	if err != nil {
		t.Fatalf("BuildVerdict: %v", err)
	}
	if got := len(statusOf(v, "C-COVERAGE", "")); got != 6 {
		t.Fatalf("C-COVERAGE rows = %d, want 6", got)
	}
	if !v.Failed() {
		t.Fatal("missing coverage must fail")
	}
}

func TestVerdictRejectsForeignReport(t *testing.T) {
	t.Parallel()
	p := testPlan(t)
	if _, err := BuildVerdict(p, []*Report{{Nonce: "other"}}, nil); err == nil {
		t.Fatal("BuildVerdict accepted a report from another plan")
	}
}

func TestVerdictHostFixtureChecks(t *testing.T) {
	t.Parallel()
	p := testPlan(t)
	var ro, rw Export
	for _, e := range p.Exports {
		switch e.Mode {
		case ModeRO:
			ro = e
		case ModeRW:
			rw = e
		}
	}
	if err := os.WriteFile(filepath.Join(ro.HostPath, "planted"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ro.HostPath, ro.Canaries[0].Name), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rw.HostPath, "jlp-guest-root-write"), []byte(p.Nonce+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reps := fullReports(p)
	reps[0].Results = append(reps[0].Results, Result{ID: "F-RW-WRITE", Status: StatusPass})
	v, err := BuildVerdict(p, reps, nil)
	if err != nil {
		t.Fatalf("BuildVerdict: %v", err)
	}
	checks := map[string]Status{
		"F-HOST-RO-CANARY":       StatusFail,
		"F-HOST-RO-LISTING":      StatusFail,
		"F-HOST-OUTSIDE":         StatusPass,
		"F-HOST-OUTSIDE-LISTING": StatusPass,
		"F-HOST-RW-WRITE":        StatusPass,
		"F-HOST-SUID":            StatusPass,
		"F-HOST-MKNOD":           StatusPass,
	}
	for id, want := range checks {
		if got := statusOf(v, id, ""); !slices.Equal(got, []Status{want}) {
			t.Errorf("%s = %v, want [%s]", id, got, want)
		}
	}
}

func TestVerdictSkipsDomainTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		hit    bool
		target string
		want   Status
	}{
		{name: "guest-internal target is skipped", target: "172.17.0.1:22", want: StatusSkip},
		{name: "host target still fails", target: "172.16.0.1:22", want: StatusFail},
		{name: "sentinel hit overrides skip", target: "172.17.0.1:47801", hit: true, want: StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := testPlan(t)
			reps := fullReports(p)
			reps[0].LocalAddrs = []string{"127.0.0.1", "172.16.0.2", "172.17.0.1"}
			reps[0].Results = append(reps[0].Results, Result{ID: "N-LANGW-TCP", Key: "N-LANGW-TCP#9", Target: tt.target, Expect: ExpectDeny, Observation: ObsReached, Status: StatusFail})
			var hits []Hit
			if tt.hit {
				hits = []Hit{{Sentinel: "host", Proto: "tcp", Payload: "JLP1 " + p.Nonce + " N-LANGW-TCP#9 " + reps[0].Context}}
			}
			v, err := BuildVerdict(p, reps, hits)
			if err != nil {
				t.Fatalf("BuildVerdict: %v", err)
			}
			if got := statusOf(v, "N-LANGW-TCP", reps[0].Context); !slices.Equal(got, []Status{tt.want}) {
				t.Fatalf("N-LANGW-TCP = %v, want %v", got, tt.want)
			}
		})
	}
}
