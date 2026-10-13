package config

import (
	"fmt"
	"strconv"
	"strings"
)

type caBundleMode uint8

const (
	caBundleAutomatic caBundleMode = iota
	caBundleDisabled
	caBundleCustom
)

// CABundle is the typed policy for forwarding a host CA bundle.
// Its zero value selects automatic discovery.
type CABundle struct {
	mode caBundleMode
	path string
}

func AutomaticCABundle() CABundle {
	return CABundle{mode: caBundleAutomatic}
}

func DisabledCABundle() CABundle {
	return CABundle{mode: caBundleDisabled}
}

func CustomCABundle(path string) CABundle {
	return CABundle{mode: caBundleCustom, path: path}
}

func (b CABundle) Automatic() bool {
	return b.mode == caBundleAutomatic
}

func (b CABundle) Enabled() bool {
	return b.mode != caBundleDisabled
}

func (b CABundle) Path() (string, bool) {
	return b.path, b.mode == caBundleCustom
}

func (b *CABundle) UnmarshalTOML(value any) error {
	switch value := value.(type) {
	case bool:
		if value {
			*b = AutomaticCABundle()
		} else {
			*b = DisabledCABundle()
		}
		return nil
	case string:
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("ca_bundle: custom path must not be empty")
		}
		*b = CustomCABundle(value)
		return nil
	default:
		return fmt.Errorf("ca_bundle: expected a boolean or non-empty string, got %T", value)
	}
}

func (b CABundle) MarshalTOML() ([]byte, error) {
	switch b.mode {
	case caBundleAutomatic:
		return []byte("true"), nil
	case caBundleDisabled:
		return []byte("false"), nil
	case caBundleCustom:
		if strings.TrimSpace(b.path) == "" {
			return nil, fmt.Errorf("marshal ca_bundle: custom path must not be empty")
		}
		return []byte(strconv.Quote(b.path)), nil
	default:
		return nil, fmt.Errorf("marshal ca_bundle: unsupported mode %d", b.mode)
	}
}
