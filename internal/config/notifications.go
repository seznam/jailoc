package config

import "fmt"

// Values of the notifications setting: how an attached session tells the
// user that the agent finished or needs input while its terminal is not
// focused. NotificationsAuto uses native desktop notifications when they are
// usable outside SSH, otherwise NotificationsOSC.
const (
	NotificationsAuto   = "auto"
	NotificationsNative = "native"
	NotificationsOSC    = "osc"
	NotificationsOff    = "off"
)

// validateNotifications checks a notifications value; empty inherits.
func validateNotifications(v string) error {
	switch v {
	case "", NotificationsAuto, NotificationsNative, NotificationsOSC, NotificationsOff:
		return nil
	}
	return fmt.Errorf("invalid notifications %q: must be %q, %q, %q or %q", v, NotificationsAuto, NotificationsNative, NotificationsOSC, NotificationsOff)
}
