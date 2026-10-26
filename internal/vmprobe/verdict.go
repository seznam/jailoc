package vmprobe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Verdict is the merged outcome of an acceptance run.
type Verdict struct {
	Plan    *Plan
	Reports []*Report
	Results []Result
}

// Failed reports whether any result fails the acceptance run.
func (v *Verdict) Failed() bool {
	for _, r := range v.Results {
		if r.Status == StatusFail || r.Status == StatusMissing {
			return true
		}
	}
	return false
}

// BuildVerdict merges probe reports, sentinel hits and host-side fixture checks.
func BuildVerdict(p *Plan, reports []*Report, hits []Hit) (*Verdict, error) {
	v := &Verdict{Plan: p, Reports: reports}
	type rk struct{ key, ctx string }
	byKey := map[rk]int{}
	for _, rep := range reports {
		if rep.Nonce != p.Nonce {
			return nil, fmt.Errorf("report %s/%s has nonce %s, plan has %s", rep.Kind, rep.Context, rep.Nonce, p.Nonce)
		}
		for _, r := range rep.Results {
			r.Context = rep.Context
			v.Results = append(v.Results, r)
			if r.Key != "" {
				byKey[rk{r.Key, r.Context}] = len(v.Results) - 1
			}
		}
	}
	markDomainTargets(v.Results, reports)


	for _, h := range hits {
		nonce, key, ctxName, ok := ParsePayload(h.Payload)
		where := fmt.Sprintf("sentinel %s %s %s <- %s", h.Sentinel, h.Proto, h.Local, h.Remote)
		switch {
		case !ok:
			v.Results = append(v.Results, Result{ID: "S-UNATTRIBUTED", Target: h.Local, Proto: h.Proto, Status: StatusReview, Detail: where + fmt.Sprintf(" payload %q", h.Payload)})
			continue
		case nonce != p.Nonce:
			continue
		}
		if strings.HasPrefix(key, "L-FAILCLOSED#") {
			v.Results = append(v.Results, Result{ID: "L-FAILCLOSED-SENTINEL", Context: ctxName, Key: key, Status: StatusFail, Detail: where})
			continue
		}
		idx, found := byKey[rk{key, ctxName}]
		if !found {
			v.Results = append(v.Results, Result{ID: "S-UNKNOWN-KEY", Context: ctxName, Key: key, Status: StatusReview, Detail: where})
			continue
		}
		r := &v.Results[idx]
		if r.Expect == ExpectDeny {
			r.Status = StatusFail
			r.Observation = ObsReached
			r.Detail += "; " + where
		}
	}

	v.Results = append(v.Results, hostFixtureChecks(p, reports)...)
	v.Results = append(v.Results, coverage(reports)...)
	return v, nil
}

// markDomainTargets turns deny failures whose target is assigned inside the
// workspace domain (any guest context) into SKIP: such a probe reaches the
// guest or one of its containers, not the host. Sentinel hits applied later
// still fail the result, because a sentinel only runs on real hosts.
func markDomainTargets(results []Result, reports []*Report) {
	domain := map[netip.Addr]bool{}
	for _, rep := range reports {
		if rep.Kind != "guest" {
			continue
		}
		for _, s := range rep.LocalAddrs {
			if a, err := netip.ParseAddr(s); err == nil {
				domain[a.WithZone("").Unmap()] = true
			}
		}
	}
	for i := range results {
		r := &results[i]
		if r.Expect != ExpectDeny || r.Status != StatusFail {
			continue
		}
		host, _, err := net.SplitHostPort(r.Target)
		if err != nil {
			continue
		}
		if a, err := netip.ParseAddr(host); err == nil && domain[a.WithZone("").Unmap()] {
			r.Status = StatusSkip
			r.Detail += "; target is assigned inside the workspace domain, not a host destination"
		}
	}
}

func coverage(reports []*Report) []Result {
	has := func(kind, ctxName string) bool {
		return slices.ContainsFunc(reports, func(r *Report) bool {
			return r.Kind == kind && (ctxName == "" || r.Context == ctxName)
		})
	}
	var res []Result
	for _, c := range RequiredGuestContexts {
		if !has("guest", c) {
			res = append(res, Result{ID: "C-COVERAGE", Context: c, Status: StatusMissing, Detail: "guest report missing for required context"})
		}
	}
	if !has("selftest", "") {
		res = append(res, Result{ID: "C-COVERAGE", Context: "host", Status: StatusMissing, Detail: "sentinel selftest report missing; deny checks are not conclusive"})
	}
	if !has("ingress", "") {
		res = append(res, Result{ID: "C-COVERAGE", Context: "host", Status: StatusMissing, Detail: "ingress report missing"})
	}
	if !has("watch", "") {
		res = append(res, Result{ID: "C-COVERAGE", Context: "watch", Status: StatusMissing, Detail: "watch report missing"})
	}
	return res
}

func sha(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixture path from the plan
	if err != nil {
		return ""
	}
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func hostFixtureChecks(p *Plan, reports []*Report) []Result {
	var res []Result
	for _, e := range p.Exports {
		if !e.Fixture {
			continue
		}
		if e.Mode == ModeRO {
			for _, c := range e.Canaries {
				r := Result{ID: "F-HOST-RO-CANARY", Context: "host", Target: filepath.Join(e.HostPath, c.Name), Status: StatusPass, Detail: "unchanged"}
				if sha(r.Target) != c.SHA256 {
					r.Status, r.Detail = StatusFail, "ro canary modified or removed"
				}
				res = append(res, r)
			}
			res = append(res, listingCheck("F-HOST-RO-LISTING", e.HostPath, e.Listing))
		}
	}
	oc := filepath.Join(p.OutsideHostPath, p.OutsideCanary.Name)
	out := Result{ID: "F-HOST-OUTSIDE", Context: "host", Target: oc, Status: StatusPass, Detail: "unchanged"}
	if sha(oc) != p.OutsideCanary.SHA256 {
		out.Status, out.Detail = StatusFail, "outside canary modified or removed"
	}
	res = append(res, out, listingCheck("F-HOST-OUTSIDE-LISTING", p.OutsideHostPath, []string{p.OutsideCanary.Name}))

	rwDir := ""
	for _, e := range p.Exports {
		if e.Fixture && e.Mode == ModeRW {
			rwDir = e.HostPath
		}
	}
	if rwDir == "" {
		return res
	}
	for _, rep := range reports {
		if rep.Kind != "guest" || !slices.ContainsFunc(rep.Results, func(r Result) bool { return r.ID == "F-RW-WRITE" }) {
			continue
		}
		res = append(res, rwHostChecks(p, rwDir, "jlp-"+rep.Context+"-", rep.Context)...)
	}
	return res
}

func listingCheck(id, dir string, want []string) Result {
	r := Result{ID: id, Context: "host", Target: dir, Status: StatusPass, Detail: "listing unchanged"}
	got, err := ListDir(dir)
	if err != nil {
		r.Status, r.Detail = StatusFail, err.Error()
		return r
	}
	if !slices.Equal(got, want) {
		r.Status, r.Detail = StatusFail, fmt.Sprintf("listing changed: got %v, want %v", got, want)
	}
	return r
}

func rwHostChecks(p *Plan, dir, prefix, ctxName string) []Result {
	var res []Result
	wf := filepath.Join(dir, prefix+"write")
	data, err := os.ReadFile(wf) //nolint:gosec // G304: fixture path
	w := Result{ID: "F-HOST-RW-WRITE", Context: ctxName, Target: wf, Status: StatusPass, Detail: "guest write visible on host"}
	if err != nil || strings.TrimSpace(string(data)) != p.Nonce {
		w.Status, w.Detail = StatusFail, "guest write not visible in the host rw fixture (export mapped elsewhere?)"
	}
	res = append(res, w)

	uid := os.Getuid()
	sf := filepath.Join(dir, prefix+"suid")
	s := Result{ID: "F-HOST-SUID", Context: ctxName, Target: sf}
	if fi, err := os.Lstat(sf); err != nil {
		s.Status, s.Detail = StatusPass, "file absent: "+err.Error()
	} else {
		owner := -1
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			owner = int(st.Uid)
		}
		s.Detail = fmt.Sprintf("owner uid %d, mode %s, host user %d", owner, fi.Mode(), uid)
		switch {
		case owner == 0 && uid != 0:
			s.Status = StatusFail
			s.Detail += "; guest created a root-owned host file"
		case fi.Mode()&fs.ModeSetuid != 0 && owner != uid:
			s.Status = StatusFail
			s.Detail += "; setuid file owned by another host user"
		default:
			s.Status = StatusPass
		}
	}
	res = append(res, s)

	df := filepath.Join(dir, prefix+"dev")
	d := Result{ID: "F-HOST-MKNOD", Context: ctxName, Target: df, Status: StatusPass}
	if fi, err := os.Lstat(df); err != nil {
		d.Detail = "absent"
	} else {
		d.Detail = "mode " + fi.Mode().String()
		if fi.Mode()&(fs.ModeDevice|fs.ModeCharDevice) != 0 {
			d.Status = StatusFail
			d.Detail += "; guest created a host device node"
		}
	}
	return append(res, d)
}

// Markdown renders the verdict as a summary plus every non-PASS row.
func (v *Verdict) Markdown() string {
	var b strings.Builder
	counts := map[Status]int{}
	type agg struct {
		id, ctxName string
		n           map[Status]int
	}
	var order []string
	groups := map[string]*agg{}
	for _, r := range v.Results {
		counts[r.Status]++
		k := r.ID + "\x00" + r.Context
		g, ok := groups[k]
		if !ok {
			g = &agg{id: r.ID, ctxName: r.Context, n: map[Status]int{}}
			groups[k] = g
			order = append(order, k)
		}
		g.n[r.Status]++
	}
	slices.Sort(order)
	verdict := "PASS"
	if v.Failed() {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "# VM acceptance verdict: %s\n\n", verdict)
	fmt.Fprintf(&b, "- Spec: `%s`\n- Candidate: `%s`\n- Plan nonce: `%s`\n- IPv6 mode: `%s`\n", v.Plan.SpecVersion, v.Plan.Candidate, v.Plan.Nonce, v.Plan.IPv6Mode)
	all := []Status{StatusPass, StatusFail, StatusMissing, StatusWarn, StatusReview, StatusSkip}
	parts := make([]string, 0, len(all))
	for _, s := range all {
		parts = append(parts, fmt.Sprintf("%s %d", s, counts[s]))
	}
	fmt.Fprintf(&b, "- Totals: %s\n\n", strings.Join(parts, ", "))

	b.WriteString("## Summary\n\n| Check | Context | PASS | FAIL | MISSING | WARN | REVIEW | SKIP |\n|---|---|---|---|---|---|---|---|\n")
	for _, k := range order {
		g := groups[k]
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %d | %d |\n", g.id, g.ctxName,
			g.n[StatusPass], g.n[StatusFail], g.n[StatusMissing], g.n[StatusWarn], g.n[StatusReview], g.n[StatusSkip])
	}

	b.WriteString("\n## Non-passing results\n\n| Status | Check | Context | Target | Proto | Expect | Observation | Detail |\n|---|---|---|---|---|---|---|---|\n")
	rows := slices.Clone(v.Results)
	slices.SortStableFunc(rows, func(a, c Result) int {
		return strings.Compare(string(a.Status)+a.ID+a.Context, string(c.Status)+c.ID+c.Context)
	})
	for _, r := range rows {
		if r.Status == StatusPass {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", r.Status, r.ID, r.Context,
			mdCell(r.Target), r.Proto, r.Expect, r.Observation, mdCell(r.Detail))
	}
	return b.String()
}

func mdCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
