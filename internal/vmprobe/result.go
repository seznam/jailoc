package vmprobe

import (
	"os"
	"runtime"
	"time"
)

// Status is the outcome of a single check.
type Status string

// Check statuses. FAIL and MISSING fail the acceptance run; REVIEW requires a
// human decision recorded in the candidate report.
const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusWarn    Status = "WARN"
	StatusReview  Status = "REVIEW"
	StatusSkip    Status = "SKIP"
	StatusMissing Status = "MISSING"
)

// Expectations of a network check.
const (
	ExpectAllow = "allow"
	ExpectDeny  = "deny"
	ExpectInfo  = "info"
)

// Result is one executed check.
type Result struct {
	ID          string `json:"id"`
	Key         string `json:"key,omitempty"` // unique probe key carried in sentinel payloads
	Context     string `json:"context"`
	Target      string `json:"target,omitempty"`
	Proto       string `json:"proto,omitempty"`
	Expect      string `json:"expect,omitempty"`
	Observation string `json:"observation,omitempty"`
	Status      Status `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Millis      int64  `json:"ms,omitempty"`
}

// Report is the output of one probe invocation.
type Report struct {
	SpecVersion string    `json:"spec_version"`
	Nonce       string    `json:"nonce"`
	Kind        string    `json:"kind"` // guest, ingress, watch
	Context     string    `json:"context"`
	Hostname    string    `json:"hostname"`
	GOOS        string    `json:"goos"`
	GOARCH      string    `json:"goarch"`
	EUID        int       `json:"euid"`
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished"`
	Results     []Result  `json:"results"`
	// LocalAddrs lists the addresses assigned in the network namespace the
	// probe ran in. The verdict uses them to recognise targets inside the
	// workspace domain rather than on the host.
	LocalAddrs []string `json:"local_addrs,omitempty"`
}

// NewReport starts a report for the given plan, kind and context.
func NewReport(p *Plan, kind, ctxName string) *Report {
	host, _ := os.Hostname()
	return &Report{
		SpecVersion: SpecVersion,
		Nonce:       p.Nonce,
		Kind:        kind,
		Context:     ctxName,
		Hostname:    host,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		EUID:        os.Geteuid(),
		Started:     time.Now().UTC(),
	}
}

// Add appends results, stamping the report context on each.
func (r *Report) Add(results ...Result) {
	for _, res := range results {
		res.Context = r.Context
		r.Results = append(r.Results, res)
	}
}

// Contexts the acceptance run requires for guest network checks.
const (
	CtxGuestRoot    = "guest-root"
	CtxGuestUser    = "guest-user"
	CtxContainer    = "container-bridge"
	CtxContainerNet = "container-hostnet-privileged"
)

// RequiredGuestContexts lists contexts whose guest report must be present.
var RequiredGuestContexts = []string{CtxGuestRoot, CtxGuestUser, CtxContainer, CtxContainerNet}
