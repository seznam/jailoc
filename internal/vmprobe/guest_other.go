//go:build !linux

package vmprobe

import (
	"context"
	"net/netip"
)

func defaultGateways() []netip.Addr { return nil }

func ifaceChecks(*Plan) []Result {
	return []Result{{ID: "I-NIC", Status: StatusSkip, Detail: "interface checks require a Linux guest"}}
}

func vsockChecks(context.Context, *Plan) []Result {
	return []Result{{ID: "I-VSOCK", Status: StatusSkip, Detail: "vsock checks require a Linux guest"}}
}

func fsChecks(*Plan, string) []Result {
	return []Result{{ID: "F-ROOT", Status: StatusSkip, Detail: "filesystem checks require a Linux guest"}}
}
