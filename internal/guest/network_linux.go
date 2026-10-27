//go:build linux

package guest

import (
	"fmt"
	"net"

	"github.com/stacklok/go-microvm/guest/harden"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/seznam/jailoc/internal/vmnet"
)

func configureIPv6() error {
	link, err := netlink.LinkByName("eth0")
	if err != nil {
		return fmt.Errorf("find IPv6 interface: %w", err)
	}
	for _, setting := range []struct{ name, value string }{
		{"disable_ipv6", "0"},
		{"accept_ra", "0"},
		{"autoconf", "0"},
		{"use_tempaddr", "0"},
		{"accept_dad", "0"},
	} {
		if err := harden.Set("net.ipv6.conf.eth0."+setting.name, setting.value); err != nil {
			return fmt.Errorf("configure IPv6 %s: %w", setting.name, err)
		}
	}
	address := &netlink.Addr{
		IPNet: &net.IPNet{IP: net.ParseIP(vmnet.Guest6), Mask: net.CIDRMask(64, 128)},
		Flags: unix.IFA_F_NODAD,
	}
	if err := netlink.AddrReplace(link, address); err != nil {
		return fmt.Errorf("configure IPv6 address: %w", err)
	}
	if err := netlink.RouteReplace(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP(vmnet.LinkLocal6),
	}); err != nil {
		return fmt.Errorf("configure IPv6 default route: %w", err)
	}
	return nil
}
