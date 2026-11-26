package guest

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// liveState applies Reload requests: the CA bundle, the SSH and gpg agent
// relays, the host port mirrors, the snapshot binds, the time zone and the
// /tmp expiry and the proxy relay allowlist.
type liveState struct {
	logger    *slog.Logger
	acct      account
	seq       int64
	applied   Reload
	stopAgent func()
	stopGPG   func()
	// hostPorts stop the loopback mirrors of host_ports, by port.
	hostPorts map[uint16]func()
	// snapshots are the bound snapshots, in bind order, and mounts the
	// descriptors of their mounts.
	snapshots []Snapshot
	mounts    []int
	// created holds the snapshot mount points the guest created.
	created map[string]bool
	// tmp expires /tmp entries; Reload.TmpTTL replaces its TTL.
	tmp *tmpCleaner
	// proxy is the proxy relay; Reload.ProxyPorts replaces its allowlist. It
	// is nil until the relay starts and when the guest runs none.
	proxy *proxyRelay
	// setCA overrides setCABundle in tests when non-nil.
	setCA func(string) error
	// setHosts overrides setHostPorts in tests when non-nil.
	setHosts func([]uint16)
}

func (l *liveState) apply(r Reload) error {
	if r.TmpTTL != nil && *r.TmpTTL < 0 {
		return fmt.Errorf("tmp: negative ttl %s", *r.TmpTTL)
	}
	if err := l.checkProxyPorts(r.ProxyPorts); err != nil {
		return err
	}
	setCA := l.setCA
	if setCA == nil {
		setCA = setCABundle
	}
	if err := setCA(r.CABundle); err != nil {
		return err
	}
	if !slices.Equal(r.Snapshots, l.snapshots) {
		if err := l.setSnapshots(r.Snapshots); err != nil {
			return err
		}
	}
	switch {
	case r.SSHAgent && l.stopAgent == nil:
		stop, err := startAgentRelay(l.logger, l.acct)
		if err != nil {
			return err
		}
		l.stopAgent = stop
	case !r.SSHAgent && l.stopAgent != nil:
		l.stopAgent()
		l.stopAgent = nil
	}
	switch {
	case r.GPGAgent && l.stopGPG == nil:
		stop, err := startGPGAgentRelay(l.logger, l.acct)
		if err != nil {
			return err
		}
		l.stopGPG = stop
	case !r.GPGAgent && l.stopGPG != nil:
		l.stopGPG()
		l.stopGPG = nil
	}
	// A guest port can move between a proxy route and host_ports in one
	// reload. The relay drops removed route ports before host port mirrors
	// start and allows added ones only after obsolete mirrors stop, so a
	// route never reaches a host service mirrored on its port. Both lists
	// are validated by checkProxyPorts, so they cannot fail here.
	proxyPorts := r.ProxyPorts != nil && l.proxy != nil
	if proxyPorts {
		if err := l.proxy.RetainPorts(*r.ProxyPorts); err != nil {
			return fmt.Errorf("proxy ports: %w", err)
		}
	}
	setHosts := l.setHosts
	if setHosts == nil {
		setHosts = l.setHostPorts
	}
	setHosts(r.HostPorts)
	if proxyPorts {
		if err := l.proxy.SetPorts(*r.ProxyPorts); err != nil {
			return fmt.Errorf("proxy ports: %w", err)
		}
	}
	// A wrong zone only shifts displayed times; it does not fail the reload.
	if r.TimeZone != nil {
		if err := setTimeZone("/", *r.TimeZone); err != nil {
			l.logger.Warn("set time zone", "error", err)
		}
	}
	if r.TmpTTL != nil {
		// Applied after the fallible reload steps succeed so a failed
		// reload never leaves a shorter TTL active. SetTTL returns once
		// any pass with the old TTL has stopped, before the reload is
		// acknowledged.
		l.tmp.SetTTL(*r.TmpTTL)
	}
	l.applied = r
	return nil
}

// checkProxyPorts rejects a Reload.ProxyPorts the guest cannot apply: ports
// without a running relay, or an invalid port.
func (l *liveState) checkProxyPorts(ports *[]uint16) error {
	if ports == nil {
		return nil
	}
	if _, err := proxyRelayAllowlist(*ports); err != nil {
		return err
	}
	if l.proxy == nil && len(*ports) > 0 {
		return errors.New("proxy ports: the guest runs no proxy relay")
	}
	return nil
}

// setSnapshots replaces the bound snapshots with next. Old binds are detached
// lazily through their mount descriptors, so open files keep their content
// and a destination replaced since the bind does not redirect the unmount;
// mount points the guest created for snapshots that are gone are removed.
func (l *liveState) setSnapshots(next []Snapshot) error {
	for i := len(l.snapshots) - 1; i >= 0; i-- {
		target := l.snapshots[i].Target
		if err := unmountSnapshot(l.mounts[i]); err != nil {
			return fmt.Errorf("snapshot %s: unmount: %w", target, err)
		}
		l.snapshots, l.mounts = l.snapshots[:i], l.mounts[:i]
		if l.created[target] && !slices.ContainsFunc(next, func(s Snapshot) bool { return s.Target == target }) {
			_ = os.Remove(target)
			delete(l.created, target)
		}
	}
	for _, s := range next {
		created, fd, err := bindSnapshot(s, l.acct)
		if created {
			l.created[s.Target] = true
		}
		if err != nil {
			return err
		}
		l.snapshots, l.mounts = append(l.snapshots, s), append(l.mounts, fd)
	}
	return nil
}

// poll applies a new reload request from the control share, if any.
func (l *liveState) poll() {
	data, err := os.ReadFile(filepath.Join(ControlDir, ReloadFile))
	if err != nil {
		return
	}
	var r Reload
	if err := json.Unmarshal(data, &r); err != nil || r.Seq == l.seq {
		return
	}
	l.seq = r.Seq
	done := ReloadDone{Seq: r.Seq}
	if err := l.apply(r); err != nil {
		l.logger.Error("reload", "error", err)
		done.Error = err.Error()
	} else {
		l.logger.Info("reload applied", "seq", r.Seq, "ca_bundle", r.CABundle != "", "ssh_agent", r.SSHAgent, "gpg_agent", r.GPGAgent, "host_ports", r.HostPorts, "snapshots", len(r.Snapshots), "time_zone", timeZoneName(r.TimeZone), "tmp_ttl", tmpTTLName(r.TmpTTL), "proxy_ports", proxyPortsName(r.ProxyPorts))
	}
	out, _ := json.Marshal(done)
	tmp := filepath.Join(ControlDir, ReloadDoneFile+".tmp")
	if err := os.WriteFile(tmp, out, 0o600); err == nil {
		_ = os.Rename(tmp, filepath.Join(ControlDir, ReloadDoneFile))
	}
}

// proxyPortsName describes a Reload.ProxyPorts for the log.
func proxyPortsName(ports *[]uint16) any {
	if ports == nil {
		return "unchanged"
	}
	return *ports
}

// tmpTTLName describes a Reload.TmpTTL for the log.
func tmpTTLName(ttl *time.Duration) string {
	if ttl == nil {
		return "unchanged"
	}
	return ttl.String()
}
