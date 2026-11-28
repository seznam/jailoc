//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/seznam/jailoc/internal/vm"
)

// upstreamReloadTimeout bounds a broker reload that starts, reloads or
// stops the upstream proxy: the broker runs the credential helper (up to 10
// seconds), reads the password and fetches the proxy's CA before it reports
// the reload.
const upstreamReloadTimeout = 35 * time.Second

// brokerReloadTimeout is how long a reload from running to next waits for
// the broker's answer.
func brokerReloadTimeout(running, next vm.Spec) time.Duration {
	if running.UpstreamProxy || next.UpstreamProxy {
		return upstreamReloadTimeout
	}
	return reloadTimeout
}

// withUpstreamProxy adds proxy as the upstream_proxy of the broker policy
// JSON object policy; a nil proxy leaves the policy as it is.
func withUpstreamProxy(policy []byte, proxy *netbroker.UpstreamProxy) ([]byte, error) {
	if proxy == nil {
		return policy, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(policy, &fields); err != nil {
		return nil, fmt.Errorf("decode broker policy: %w", err)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(proxy)
	if err != nil {
		return nil, fmt.Errorf("encode upstream proxy policy: %w", err)
	}
	fields["upstream_proxy"] = raw
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode broker policy: %w", err)
	}
	return out, nil
}

// upstreamReloadWarning describes an upstream proxy the broker could not
// apply on a reload, or "" when it did. The broker then keeps the route and
// CA it last published (a proxy newly enabled blocks egress instead of going
// direct), while the rest of the reload applies. The helper's stderr is not
// shown: it may echo credentials.
func upstreamReloadWarning(ev brokerReload) string {
	if ev.UpstreamError == "" {
		return ""
	}
	msg := "upstream proxy not updated: " + escapeVisible(ev.UpstreamError)
	if ev.UpstreamGeneration != nil {
		msg += fmt.Sprintf("; the broker keeps the last working route (generation %d)", *ev.UpstreamGeneration)
	}
	return msg + "; `jailoc status` shows its state"
}

// printUpstreamStatus prints the upstream proxy state the broker of the VM
// in stateDir published. It shows the public status only: never header
// values or the helper's stderr.
func printUpstreamStatus(w io.Writer, stateDir string) {
	st, err := netbroker.ReadUpstreamStatus(vm.UpstreamStatusFile(stateDir))
	if err != nil {
		reason := "not published yet"
		if !errors.Is(err, os.ErrNotExist) {
			reason = escapeVisible(err.Error())
		}
		_, _ = fmt.Fprintf(w, "Upstream:  unknown (%s)\n", reason)
		return
	}
	label := escapeVisible(st.State)
	if st.URL != "" {
		label += " via " + escapeVisible(st.URL)
	}
	if st.Mode != "" {
		label += " (" + escapeVisible(st.Mode) + ")"
	}
	_, _ = fmt.Fprintf(w, "Upstream:  %s\n", label)
	if st.LastError != "" {
		_, _ = fmt.Fprintf(w, "           last error: %s\n", escapeVisible(st.LastError))
	}
}

// upstreamCAReloader is the vm.Spec.ReloadUpstreamCA of the workspace VM in
// stateDir: under the workspace's reload lock it composes the guest CA
// bundle from the current spec (the host bundle plus the CA the broker
// published) and makes the guest reload it with the settings it runs, so
// nothing else changes. Without the upstream proxy in the current spec it
// does nothing.
func upstreamCAReloader(stateDir, wsName string) func(context.Context) error {
	return func(ctx context.Context) error {
		return withReloadLock(ctx, wsName, func() error {
			spec, err := vm.ReadSpec(stateDir)
			if err != nil {
				return err
			}
			if !spec.UpstreamProxy || spec.Guest == nil {
				return nil
			}
			if err := vm.ComposeGuestCABundle(stateDir, spec.CABundle); err != nil {
				return fmt.Errorf("compose guest CA bundle: %w", err)
			}
			return reloadGuestContext(ctx, stateDir, *spec.Guest, nil, nil)
		})
	}
}
