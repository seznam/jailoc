//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"golang.org/x/crypto/ssh"
)

const (
	// tmpStatusTimeout bounds the whole tmp-status query of `jailoc status`:
	// the SSH dial and handshake, the session and the command.
	tmpStatusTimeout = 5 * time.Second
	// tmpStatusMaxOutput bounds the tmp-status output the host reads; the
	// real output is one short JSON line.
	tmpStatusMaxOutput = 4 << 10
	// tmpWarnRatio is the fill level of /tmp from which status warns.
	tmpWarnRatio = 0.9
	// tmpGuestProtocol is the first guest protocol that mounts /tmp from its
	// configuration, applies Reload.TmpTTL and answers tmp-status.
	tmpGuestProtocol = 4
	// legacyTmpSize is the /tmp size of guests that predate tmp_size.
	legacyTmpSize = 1 << 30
)

// workspaceTmp is the guest /tmp configuration of the resolved workspace.
func workspaceTmp(ws *workspace.Resolved) guest.TmpConfig {
	return guest.TmpConfig{Storage: ws.TmpStorage, SizeBytes: ws.TmpSizeBytes, TTL: ws.TmpTTL}
}

// effectiveTmp is the /tmp configuration the spec's guest applies. Specs
// written before tmp_* settings carry a zero TmpConfig and a 1 GiB tmpfs
// without cleanup.
func effectiveTmp(spec vm.Spec) guest.TmpConfig {
	var t guest.TmpConfig
	if spec.Guest != nil {
		t = spec.Guest.Tmp
	}
	return normalizeTmp(t)
}

// normalizeTmp fills the legacy values of a zero TmpConfig, the way the guest
// reads it.
func normalizeTmp(t guest.TmpConfig) guest.TmpConfig {
	if t.Storage == "" {
		t.Storage = config.TmpStorageRAM
	}
	if t.SizeBytes == 0 {
		t.SizeBytes = legacyTmpSize
	}
	return t
}

// tmpRestartChanges names the /tmp settings that differ between the running
// and the wanted configuration and need a restart: where /tmp lives and its
// size. tmp_ttl is not one of them; reload applies it.
func tmpRestartChanges(running, want guest.TmpConfig) []string {
	running, want = normalizeTmp(running), normalizeTmp(want)
	var out []string
	if running.Storage != want.Storage {
		out = append(out, "tmp_storage")
	}
	if running.SizeBytes != want.SizeBytes {
		out = append(out, "tmp_size")
	}
	return out
}

// checkTmpReload refuses, before a reload changes anything, a tmp_ttl change
// the running guest cannot apply: guests before protocol 4 ignore it.
func checkTmpReload(ws *workspace.Resolved, running vm.Spec) error {
	if running.GuestProtocol >= tmpGuestProtocol || effectiveTmp(running).TTL == ws.TmpTTL {
		return nil
	}
	return fmt.Errorf("workspace %q runs a jailoc-guest that cannot change tmp_ttl; run `jailoc restart %s` to update it (this ends its running sessions)", ws.Name, ws.Name)
}

// reloadedTmp is the /tmp configuration of the running VM after a reload: the
// running one with the wanted tmp_ttl. Guests before protocol 4 do not apply
// it, so their spec stays as it is.
func reloadedTmp(running vm.Spec, want guest.TmpConfig) guest.TmpConfig {
	var t guest.TmpConfig
	if running.Guest != nil {
		t = running.Guest.Tmp
	}
	if running.GuestProtocol >= tmpGuestProtocol {
		t.TTL = want.TTL
	}
	return t
}

// tmpLabel describes the /tmp of a running VM from its applied spec, with
// the usage the guest reported. A missing or failed usage reads as unknown,
// never as an empty /tmp.
func tmpLabel(spec vm.Spec, usage *guest.TmpUsage, usageErr error) string {
	t := effectiveTmp(spec)
	cleanup := "no cleanup"
	if t.TTL > 0 {
		cleanup = "cleanup after " + tmpDuration(t.TTL)
	}
	label := fmt.Sprintf("%s, %s, %s; ", t.Storage, tmpBytes(uint64(t.SizeBytes)), cleanup) //nolint:gosec // G115: SizeBytes is positive after normalizeTmp or validated config
	switch {
	case usageErr != nil:
		return label + fmt.Sprintf("usage unknown (%s)", escapeVisible(usageErr.Error()))
	case usage == nil:
		return label + "usage unknown"
	case usage.CapacityBytes == 0:
		return label + "usage unknown (the guest reported no capacity)"
	case usage.UsedBytes > usage.CapacityBytes:
		return label + "usage unknown (the guest reported more used than capacity)"
	case usage.AvailableBytes > usage.CapacityBytes:
		return label + "usage unknown (the guest reported more available than capacity)"
	}
	ratio := float64(usage.UsedBytes) / float64(usage.CapacityBytes)
	label += fmt.Sprintf("%s of %s used (%d%%), %s free", tmpBytes(usage.UsedBytes), tmpBytes(usage.CapacityBytes), int(ratio*100), tmpBytes(usage.AvailableBytes))
	if ratio >= tmpWarnRatio {
		label += " - WARNING: almost full"
	}
	return label
}

// tmpBytes formats a byte count, exactly below 1 KiB.
func tmpBytes(n uint64) string {
	if n < 1<<10 {
		return fmt.Sprintf("%d B", n)
	}
	return formatBytes(int64(min(n, 1<<62))) //nolint:gosec // G115: capped below int64's maximum
}

// tmpReloadTTL is the tmp_ttl a reload sends to the running guest, nil when
// the guest predates protocol 4 and ignores it (checkTmpReload has refused a
// change it could not apply).
func tmpReloadTTL(running vm.Spec, want guest.TmpConfig) *time.Duration {
	if running.GuestProtocol < tmpGuestProtocol {
		return nil
	}
	return &want.TTL
}

// tmpStatus is the Tmp line of `jailoc status` for a running VM: its applied
// /tmp settings and the usage its guest reports, "unknown" when it cannot.
func tmpStatus(ctx context.Context, stateDir string, spec vm.Spec) string {
	if spec.GuestProtocol < tmpGuestProtocol {
		return tmpLabel(spec, nil, errors.New("the running guest predates it; `jailoc restart` updates it"))
	}
	usage, err := vmTmpUsage(ctx, stateDir)
	if err != nil {
		return tmpLabel(spec, nil, err)
	}
	return tmpLabel(spec, &usage, nil)
}

// vmTmpUsage asks the running workspace VM for the usage of its /tmp.
// The whole query, including a hung connected session, ends at
// tmpStatusTimeout.
func vmTmpUsage(ctx context.Context, stateDir string) (guest.TmpUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, tmpStatusTimeout)
	defer cancel()
	client, err := vm.DialSSH(ctx, stateDir)
	if err != nil {
		return guest.TmpUsage{}, err
	}
	defer func() { _ = client.Close() }()
	out, err := sshOutputBounded(ctx, client, []string{guest.BinaryPath, "tmp-status"}, tmpStatusMaxOutput)
	if err != nil {
		return guest.TmpUsage{}, err
	}
	return parseTmpUsage(out)
}

// parseTmpUsage decodes the output of `jailoc-guest tmp-status`. The guest
// is untrusted: the output must be exactly one JSON object with all three
// sizes, and impossible sizes are rejected rather than shown as usage.
func parseTmpUsage(out []byte) (guest.TmpUsage, error) {
	var raw struct {
		Capacity  *uint64 `json:"capacity_bytes"`
		Used      *uint64 `json:"used_bytes"`
		Available *uint64 `json:"available_bytes"`
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(&raw); err != nil {
		return guest.TmpUsage{}, fmt.Errorf("parse tmp usage: %w", err)
	}
	// The document ends after the object: a second decode must report EOF,
	// not another value, trailing garbage or a stray closing delimiter.
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return guest.TmpUsage{}, errors.New("parse tmp usage: unexpected data after the usage")
	}
	switch {
	case raw.Capacity == nil || raw.Used == nil || raw.Available == nil:
		return guest.TmpUsage{}, errors.New("parse tmp usage: missing capacity_bytes, used_bytes or available_bytes")
	case *raw.Capacity == 0:
		return guest.TmpUsage{}, errors.New("parse tmp usage: zero capacity")
	case *raw.Used > *raw.Capacity:
		return guest.TmpUsage{}, errors.New("parse tmp usage: more used than capacity")
	case *raw.Available > *raw.Capacity:
		return guest.TmpUsage{}, errors.New("parse tmp usage: more available than capacity")
	}
	return guest.TmpUsage{CapacityBytes: *raw.Capacity, UsedBytes: *raw.Used, AvailableBytes: *raw.Available}, nil
}

// sshOutputBounded runs cmdline over client and returns its stdout. The
// session ends with ctx: when ctx is done the connection closes, so a command
// that never returns cannot hold the caller. Output beyond limit bytes is an
// error; it is read and dropped meanwhile, so the command is never blocked on
// a full pipe.
func sshOutputBounded(ctx context.Context, client *ssh.Client, cmdline []string, limit int) ([]byte, error) {
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open ssh session: %w", ctxOr(ctx, err))
	}
	defer func() { _ = sess.Close() }()
	stdout := &limitedBuffer{limit: limit}
	sess.Stdout = stdout
	sess.Stderr = io.Discard
	if err := sess.Run(shellJoin(cmdline)); err != nil {
		return nil, fmt.Errorf("run %s: %w", cmdline[len(cmdline)-1], ctxOr(ctx, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("run %s: %w", cmdline[len(cmdline)-1], err)
	}
	if stdout.over {
		return nil, fmt.Errorf("run %s: output of more than %d bytes", cmdline[len(cmdline)-1], limit)
	}
	return stdout.buf.Bytes(), nil
}

// ctxOr returns ctx's error when ctx is done, which is what closed the
// connection, and err otherwise.
func ctxOr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// limitedBuffer keeps the first limit bytes written to it and drops the rest.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := max(b.limit-b.buf.Len(), 0); n > room {
		b.over = true
		p = p[:room]
	}
	b.buf.Write(p) // bytes.Buffer.Write never fails
	return n, nil
}
