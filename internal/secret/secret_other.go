//go:build !windows && !linux

package secret

import "fmt"

// Default: no key store is wired up on this system.
func Default() (Sealer, error) {
	return nil, fmt.Errorf("%w: не поддерживается на этой системе", ErrUnavailable)
}

// HelperMain has nothing to do here.
func HelperMain() int { return 2 }
