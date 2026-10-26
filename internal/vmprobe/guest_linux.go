//go:build linux

package vmprobe

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// defaultGateways returns next hops of IPv4 and IPv6 default routes.
func defaultGateways() []netip.Addr {
	var out []netip.Addr
	if f, err := os.Open("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 3 || fl[1] != "00000000" {
				continue
			}
			b, err := hex.DecodeString(fl[2])
			if err != nil || len(b) != 4 || fl[2] == "00000000" {
				continue
			}
			// /proc/net/route stores addresses in host (little-endian) byte order.
			out = append(out, netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]}))
		}
		_ = f.Close()
	}
	for _, r := range ipv6DefaultRoutes() {
		if r.nextHop.IsValid() && !r.nextHop.IsUnspecified() {
			out = append(out, r.nextHop.WithZone(r.iface))
		}
	}
	return out
}

type v6route struct {
	nextHop netip.Addr
	iface   string
}

func ipv6DefaultRoutes() []v6route {
	f, err := os.Open("/proc/net/ipv6_route")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []v6route
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 10 || fl[0] != strings.Repeat("0", 32) || fl[1] != "00" || fl[9] == "lo" {
			continue
		}
		b, err := hex.DecodeString(fl[4])
		if err != nil || len(b) != 16 {
			continue
		}
		out = append(out, v6route{nextHop: netip.AddrFrom16([16]byte(b)), iface: fl[9]})
	}
	return out
}

func ifaceChecks(p *Plan) []Result {
	var res []Result
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return []Result{{ID: "I-NIC", Status: StatusFail, Detail: "read /sys/class/net: " + err.Error()}}
	}
	var physical, virtual []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join("/sys/class/net", e.Name(), "device")); err == nil {
			physical = append(physical, e.Name())
		} else if e.Name() != "lo" {
			virtual = append(virtual, e.Name())
		}
	}
	nic := Result{ID: "I-NIC", Detail: fmt.Sprintf("device-backed: %v; virtual: %v", physical, virtual)}
	switch len(physical) {
	case 1:
		nic.Status = StatusPass
	case 0:
		nic.Status = StatusFail
		nic.Detail += "; no NIC (check for libkrun TSI or another socket-level egress path)"
	default:
		nic.Status = StatusFail
		nic.Detail += "; more than one NIC"
	}
	res = append(res, nic)

	var v4defaults []string
	if f, err := os.Open("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) >= 2 && fl[1] == "00000000" {
				v4defaults = append(v4defaults, fl[0])
			}
		}
		_ = f.Close()
	}
	route := Result{ID: "I-ROUTE-V4", Status: StatusPass, Detail: fmt.Sprintf("default routes via %v", v4defaults)}
	for _, ifc := range v4defaults {
		if !slices.Contains(physical, ifc) {
			route.Status = StatusFail
			route.Detail += "; default route not via the single NIC"
		}
	}
	res = append(res, route)

	var global []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if ok && ip.Is6() && !ip.Is4In6() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				global = append(global, ifc.Name+":"+ip.String())
			}
		}
	}
	routes6 := ipv6DefaultRoutes()
	v6 := Result{ID: "I-V6", Detail: fmt.Sprintf("mode %s; non-link-local v6 addrs %v; v6 default routes %d", p.IPv6Mode, global, len(routes6))}
	switch {
	case p.IPv6Mode == IPv6Disabled && (len(global) > 0 || len(routes6) > 0):
		v6.Status = StatusFail
	case p.IPv6Mode == IPv6Disabled:
		v6.Status = StatusPass
	default:
		v6.Status = StatusReview
	}
	return append(res, v6)
}

func vsockConnect(port uint32, timeout time.Duration) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.AF_VSOCK, unix.SO_VM_SOCKETS_CONNECT_TIMEOUT, &tv)
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return nil
}

func vsockChecks(_ context.Context, p *Plan) []Result {
	ports, err := ParsePortRanges(p.VsockPorts)
	if err != nil {
		return []Result{{ID: "I-VSOCK", Status: StatusFail, Detail: err.Error()}}
	}
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return []Result{{ID: "I-VSOCK", Status: StatusPass, Detail: "no AF_VSOCK transport: " + err.Error()}}
	}
	_ = unix.Close(fd)

	var mu sync.Mutex
	var open []int
	errCounts := map[string]int{}
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	for _, port := range ports {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := vsockConnect(uint32(port), 300*time.Millisecond) //nolint:gosec // G115: ParsePortRanges bounds ports to uint32
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				open = append(open, port)
				return
			}
			errCounts[errDetail(errors.Unwrap(err))]++
		}()
	}
	wg.Wait()
	slices.Sort(open)
	r := Result{ID: "I-VSOCK", Target: "cid 2 ports " + p.VsockPorts, Detail: fmt.Sprintf("open host ports %v; errors %v", open, errCounts)}
	if len(open) == 0 {
		r.Status = StatusPass
	} else {
		r.Status = StatusReview
		r.Detail += "; every open port must be a documented, non-control service"
	}
	return []Result{r}
}

func fileSHA(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: probe intentionally reads attacker-reachable paths
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func okIfErr(id, target string, err error, extra string) Result {
	r := Result{ID: id, Target: target}
	if err != nil {
		r.Status, r.Detail = StatusPass, "denied: "+err.Error()
	} else {
		r.Status, r.Detail = StatusFail, "operation succeeded"
	}
	if extra != "" {
		r.Detail += "; " + extra
	}
	return r
}

func fsChecks(p *Plan, ctxName string) []Result {
	if os.Geteuid() != 0 {
		return []Result{{ID: "F-ROOT", Status: StatusSkip, Detail: "filesystem checks require guest root"}}
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return []Result{{ID: "F-MNT-INV", Status: StatusFail, Detail: err.Error()}}
	}
	mounts := ParseMountInfo(string(data))
	var res []Result
	res = append(res, mountInventory(p, mounts)...)
	prefix := "jlp-" + ctxName + "-"
	outsideCanary := filepath.Join(p.OutsideHostPath, p.OutsideCanary.Name)

	res = append(res, readEscape("F-OUTSIDE-DIRECT", outsideCanary, p.OutsideCanary.SHA256))
	for _, e := range p.Exports {
		if !e.Fixture {
			continue
		}
		gp := e.GuestPath
		switch e.Mode {
		case ModeRW:
			res = append(res, rwChecks(p, gp, prefix)...)
		case ModeRO:
			res = append(res, roChecks(e, gp, prefix, mounts)...)
		}
	}
	for _, e := range p.Exports {
		if e.Fixture {
			res = append(res, shareScope(p, e, mounts))
		}
	}
	res = append(res, walkShared(p, mounts)...)
	return res
}

func mountInventory(p *Plan, mounts []MountInfo) []Result {
	declared := map[string]bool{}
	for _, e := range p.Exports {
		declared[filepath.Clean(e.GuestPath)] = false
	}
	var undeclared []string
	for _, m := range mounts {
		if !SharedFSType(m.FSType) {
			continue
		}
		mp := filepath.Clean(m.MountPoint)
		if _, ok := declared[mp]; ok {
			declared[mp] = true
			continue
		}
		undeclared = append(undeclared, fmt.Sprintf("%s (%s %s root=%s)", m.MountPoint, m.FSType, m.Source, m.Root))
	}
	inv := Result{ID: "F-MNT-INV", Status: StatusPass, Detail: fmt.Sprintf("undeclared shared mounts: %v", undeclared)}
	if len(undeclared) > 0 {
		inv.Status = StatusFail
	}
	var missing []string
	for gp, seen := range declared {
		if !seen {
			missing = append(missing, gp)
		}
	}
	slices.Sort(missing)
	miss := Result{ID: "F-MNT-DECLARED", Status: StatusPass, Detail: fmt.Sprintf("declared exports not found as shared mounts: %v", missing)}
	if len(missing) > 0 {
		miss.Status = StatusWarn
	}
	return []Result{inv, miss}
}

func readEscape(id, path, sha string) Result {
	got := fileSHA(path)
	if got != "" && got == sha {
		return Result{ID: id, Target: path, Status: StatusFail, Detail: "outside canary readable"}
	}
	return Result{ID: id, Target: path, Status: StatusPass, Detail: "outside canary not readable"}
}

func rwChecks(p *Plan, gp, prefix string) []Result {
	var res []Result
	wf := filepath.Join(gp, prefix+"write")
	err := os.WriteFile(wf, []byte(p.Nonce+"\n"), 0o644) //nolint:gosec // G306: fixture file
	w := Result{ID: "F-RW-WRITE", Target: wf, Status: StatusPass, Detail: "written"}
	if err != nil {
		w.Status, w.Detail = StatusFail, err.Error()
	}
	res = append(res, w)

	sf := filepath.Join(gp, prefix+"suid")
	var ops []string
	if err := os.WriteFile(sf, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // G306: fixture file
		ops = append(ops, "create: "+err.Error())
	}
	ops = append(ops, fmt.Sprintf("chown 0:0: %v", os.Chown(sf, 0, 0)))
	ops = append(ops, fmt.Sprintf("chmod 4755: %v", os.Chmod(sf, 0o755|fs.ModeSetuid)))
	res = append(res, Result{ID: "F-RW-SUID", Target: sf, Status: StatusReview, Detail: strings.Join(ops, "; ") + "; host verdict checks ownership"})

	df := filepath.Join(gp, prefix+"dev")
	err = unix.Mknod(df, unix.S_IFCHR|0o666, int(unix.Mkdev(1, 3))) //nolint:gosec // G115: device number fits int
	res = append(res, Result{ID: "F-RW-MKNOD", Target: df, Status: StatusReview, Detail: fmt.Sprintf("mknod c 1 3: %v; host verdict checks node type", err)})

	outsideCanary := filepath.Join(p.OutsideHostPath, p.OutsideCanary.Name)
	rel, relErr := filepath.Rel(gp, outsideCanary)
	links := []struct{ id, target string }{{"F-RW-SYMLINK-ABS", outsideCanary}}
	if relErr == nil {
		links = append(links, struct{ id, target string }{"F-RW-SYMLINK-REL", rel})
	}
	for i, l := range links {
		lp := filepath.Join(gp, fmt.Sprintf("%slink%d", prefix, i))
		_ = os.Remove(lp)
		if err := os.Symlink(l.target, lp); err != nil {
			res = append(res, Result{ID: l.id, Target: lp, Status: StatusReview, Detail: "symlink: " + err.Error()})
			continue
		}
		res = append(res, readEscape(l.id, lp, p.OutsideCanary.SHA256))
	}
	if relErr == nil {
		res = append(res, readEscape("F-RW-DOTDOT", gp+"/"+rel, p.OutsideCanary.SHA256))
	}
	return res
}

func findMount(mounts []MountInfo, path string) (MountInfo, bool) {
	var best MountInfo
	found := false
	for _, m := range mounts {
		mp := filepath.Clean(m.MountPoint)
		if (path == mp || strings.HasPrefix(path, mp+"/") || mp == "/") && len(mp) >= len(best.MountPoint) {
			best, found = m, true
		}
	}
	return best, found
}

func roChecks(e Export, gp, prefix string, mounts []MountInfo) []Result {
	var res []Result
	if len(e.Canaries) == 0 {
		return []Result{{ID: "F-RO", Target: gp, Status: StatusFail, Detail: "ro fixture has no canary"}}
	}
	c := filepath.Join(gp, e.Canaries[0].Name)
	if fileSHA(c) != e.Canaries[0].SHA256 {
		res = append(res, Result{ID: "F-RO-READ", Target: c, Status: StatusFail, Detail: "ro canary not readable or content differs"})
	} else {
		res = append(res, Result{ID: "F-RO-READ", Target: c, Status: StatusPass, Detail: "ro canary readable"})
	}
	now := time.Now()
	tryWrite := func() error {
		return os.WriteFile(filepath.Join(gp, prefix+"new"), []byte("x"), 0o644) //nolint:gosec // G306: fixture file
	}
	appendErr := func() error {
		f, err := os.OpenFile(c, os.O_WRONLY|os.O_APPEND, 0) //nolint:gosec // G304: fixture path
		if err != nil {
			return fmt.Errorf("open: %w", err)
		}
		_, werr := f.WriteString("x")
		cerr := f.Close()
		return errors.Join(werr, cerr)
	}
	res = append(res,
		okIfErr("F-RO-CREATE", gp, tryWrite(), ""),
		okIfErr("F-RO-WRITE", c, appendErr(), ""),
		okIfErr("F-RO-TRUNCATE", c, os.Truncate(c, 0), ""),
		okIfErr("F-RO-CHMOD", c, os.Chmod(c, 0o777), ""), //nolint:gosec // G302: probing that a ro export rejects chmod
		okIfErr("F-RO-CHOWN", c, os.Chown(c, 12345, 12345), ""),
		okIfErr("F-RO-UTIMES", c, os.Chtimes(c, now, now), ""),
		okIfErr("F-RO-XATTR", c, unix.Setxattr(c, "user.jlp", []byte("x"), 0), ""),
		okIfErr("F-RO-MKDIR", gp, os.Mkdir(filepath.Join(gp, prefix+"dir"), 0o755), ""), //nolint:gosec // G301: probing that a ro export rejects mkdir
		okIfErr("F-RO-SYMLINK", gp, os.Symlink("x", filepath.Join(gp, prefix+"sym")), ""),
		okIfErr("F-RO-RENAME", c, os.Rename(c, c+".renamed"), ""),
		okIfErr("F-RO-UNLINK", c, os.Remove(c), ""),
	)

	m, ok := findMount(mounts, filepath.Clean(gp))
	bindErr := unix.Mount("", gp, "", unix.MS_REMOUNT|unix.MS_BIND, "")
	sbErr := unix.Mount("", gp, "", unix.MS_REMOUNT, cleanSuperOpts(m.SuperOpts))
	res = append(res, okIfErr("F-RO-REMOUNT", gp, tryWrite(), fmt.Sprintf("remount bind rw: %v; remount sb rw: %v", bindErr, sbErr)))
	_ = unix.Mount("", gp, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, "")

	if !ok || !SharedFSType(m.FSType) {
		res = append(res, Result{ID: "F-RO-FRESHMOUNT", Target: gp, Status: StatusReview, Detail: fmt.Sprintf("ro export is not a shared mount (fstype %q); inspect how it is provided", m.FSType)})
		return res
	}
	tmp, err := os.MkdirTemp("", "jlp-mnt-")
	if err != nil {
		return append(res, Result{ID: "F-RO-FRESHMOUNT", Target: gp, Status: StatusFail, Detail: err.Error()})
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := unix.Mount(m.Source, tmp, m.FSType, 0, cleanSuperOpts(m.SuperOpts)); err != nil {
		return append(res, Result{ID: "F-RO-FRESHMOUNT", Target: m.Source, Status: StatusPass, Detail: fmt.Sprintf("fresh rw mount of %s (%s) refused: %v", m.Source, m.FSType, err)})
	}
	defer func() { _ = unix.Unmount(tmp, unix.MNT_DETACH) }()
	target := filepath.Join(tmp, m.Root, strings.TrimPrefix(filepath.Clean(gp), filepath.Clean(m.MountPoint)), prefix+"fresh")
	werr := os.WriteFile(target, []byte("x"), 0o644) //nolint:gosec // G306: fixture file
	return append(res, okIfErr("F-RO-FRESHMOUNT", m.Source, werr, "fresh mount succeeded; write via "+target))
}

// shareScope mounts the export's share source afresh and tries to read the
// outside canary through it, proving whether the host share is wider than the
// declared export.
func shareScope(p *Plan, e Export, mounts []MountInfo) Result {
	gp := filepath.Clean(e.GuestPath)
	m, ok := findMount(mounts, gp)
	if !ok || !SharedFSType(m.FSType) {
		return Result{ID: "F-SHARE-SCOPE", Target: gp, Status: StatusReview, Detail: fmt.Sprintf("export is not a shared mount (fstype %q)", m.FSType)}
	}
	rel, err := filepath.Rel(e.HostPath, filepath.Join(p.OutsideHostPath, p.OutsideCanary.Name))
	if err != nil {
		return Result{ID: "F-SHARE-SCOPE", Target: gp, Status: StatusFail, Detail: err.Error()}
	}
	tmp, err := os.MkdirTemp("", "jlp-scope-")
	if err != nil {
		return Result{ID: "F-SHARE-SCOPE", Target: gp, Status: StatusFail, Detail: err.Error()}
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := unix.Mount(m.Source, tmp, m.FSType, unix.MS_RDONLY, cleanSuperOpts(m.SuperOpts)); err != nil {
		return Result{ID: "F-SHARE-SCOPE", Target: m.Source, Status: StatusPass, Detail: fmt.Sprintf("fresh mount refused: %v; export root in share %q", err, m.Root)}
	}
	defer func() { _ = unix.Unmount(tmp, unix.MNT_DETACH) }()
	exportInShare := filepath.Join(tmp, m.Root, strings.TrimPrefix(gp, filepath.Clean(m.MountPoint)))
	r := readEscape("F-SHARE-SCOPE", filepath.Join(exportInShare, rel), p.OutsideCanary.SHA256)
	r.Detail += fmt.Sprintf("; share %s (%s) exposes export at root %q", m.Source, m.FSType, m.Root)
	return r
}

func walkShared(p *Plan, mounts []MountInfo) []Result {
	const limit = 50000
	var sockets, found []string
	seen, truncated := 0, false
	for _, m := range mounts {
		if !SharedFSType(m.FSType) || truncated {
			continue
		}
		_ = filepath.WalkDir(m.MountPoint, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // unreadable entries are skipped, not fatal
			}
			seen++
			if seen > limit {
				truncated = true
				return filepath.SkipAll
			}
			if d.Type()&fs.ModeSocket != 0 && !slices.Contains(p.AllowedSockets, path) {
				sockets = append(sockets, path)
			}
			if d.Name() == p.OutsideCanary.Name {
				found = append(found, path)
			}
			return nil
		})
	}
	sock := Result{ID: "F-SOCK", Status: StatusPass, Detail: fmt.Sprintf("undeclared sockets on shared mounts: %v (walked %d entries)", sockets, seen)}
	if len(sockets) > 0 {
		sock.Status = StatusFail
	}
	out := Result{ID: "F-OUTSIDE-WALK", Status: StatusPass, Detail: fmt.Sprintf("outside canary found at: %v", found)}
	if len(found) > 0 {
		out.Status = StatusFail
	}
	res := []Result{sock, out}
	if truncated {
		res = append(res, Result{ID: "F-WALK-LIMIT", Status: StatusWarn, Detail: fmt.Sprintf("walk stopped after %d entries", limit)})
	}
	return res
}
