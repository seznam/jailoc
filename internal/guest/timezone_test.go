package guest

import "testing"

func TestValidTimeZoneName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"Europe/Prague":                  true,
		"America/Argentina/Buenos_Aires": true,
		"Etc/GMT+1":                      true,
		"UTC":                            true,
		"":                               false,
		"/etc/passwd":                    false,
		"../passwd":                      false,
		"Europe/../../passwd":            false,
		"Europe/":                        false,
		"Europe Prague":                  false,
		"CET-1CEST,M3.5.0,M10.5.0/3":     false,
	} {
		if got := ValidTimeZoneName(name); got != want {
			t.Errorf("ValidTimeZoneName(%q) = %v, want %v", name, got, want)
		}
	}
}
