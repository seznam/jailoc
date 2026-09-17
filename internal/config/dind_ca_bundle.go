package config

import (
	"fmt"
	"strconv"
	"strings"
)

type dindCABundleMode uint8

const (
	dindCABundleAutomatic dindCABundleMode = iota
	dindCABundleDisabled
	dindCABundleCustom
)

// DindCABundle is the typed policy for forwarding a host CA bundle to DinD.
// Its zero value selects automatic discovery.
type DindCABundle struct {
	mode dindCABundleMode
	path string
}

func AutomaticDindCABundle() DindCABundle {
	return DindCABundle{mode: dindCABundleAutomatic}
}

func DisabledDindCABundle() DindCABundle {
	return DindCABundle{mode: dindCABundleDisabled}
}

func CustomDindCABundle(path string) DindCABundle {
	return DindCABundle{mode: dindCABundleCustom, path: path}
}

func (b DindCABundle) Automatic() bool {
	return b.mode == dindCABundleAutomatic
}

func (b DindCABundle) Enabled() bool {
	return b.mode != dindCABundleDisabled
}

func (b DindCABundle) Path() (string, bool) {
	return b.path, b.mode == dindCABundleCustom
}

func (b *DindCABundle) UnmarshalTOML(value any) error {
	switch value := value.(type) {
	case bool:
		if value {
			*b = AutomaticDindCABundle()
		} else {
			*b = DisabledDindCABundle()
		}
		return nil
	case string:
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("dind_ca_bundle: custom path must not be empty")
		}
		*b = CustomDindCABundle(value)
		return nil
	default:
		return fmt.Errorf("dind_ca_bundle: expected a boolean or non-empty string, got %T", value)
	}
}

func (b DindCABundle) MarshalTOML() ([]byte, error) {
	switch b.mode {
	case dindCABundleAutomatic:
		return []byte("true"), nil
	case dindCABundleDisabled:
		return []byte("false"), nil
	case dindCABundleCustom:
		if strings.TrimSpace(b.path) == "" {
			return nil, fmt.Errorf("marshal dind_ca_bundle: custom path must not be empty")
		}
		return []byte(strconv.Quote(b.path)), nil
	default:
		return nil, fmt.Errorf("marshal dind_ca_bundle: unsupported mode %d", b.mode)
	}
}
