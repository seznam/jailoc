//go:build linux && vmintegration

package guest

import "time"

func init() { tmpCleanInterval = 5 * time.Second }
