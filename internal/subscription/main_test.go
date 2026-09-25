package subscription

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"xray-runner/internal/secret"
)

// TestMain keeps the OS key store out of the tests: by default there is none,
// so the list stays plain as the older tests expect, and a run on a desktop
// never touches the real keyring. The sealing tests put a fake in.
func TestMain(m *testing.M) {
	sealer = noSealer
	os.Exit(m.Run())
}

func noSealer() (secret.Sealer, error) {
	return nil, fmt.Errorf("%w: test", secret.ErrUnavailable)
}

// xorSealer seals by XOR, behind the real header, so a sealed file is told
// from a plain one and opened only by the same key.
type xorSealer struct{ key byte }

func (x xorSealer) Name() string { return "test" }

func (x xorSealer) Seal(data []byte) ([]byte, error) {
	return append([]byte("xray-runner sealed v1 xor\n"), x.xor(data)...), nil
}

func (x xorSealer) Open(blob []byte) ([]byte, error) {
	const h = "xray-runner sealed v1 xor\n"
	if len(blob) < len(h) || string(blob[:len(h)]) != h {
		return nil, errors.New("not sealed")
	}
	return x.xor(blob[len(h):]), nil
}

func (x xorSealer) xor(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = c ^ x.key
	}
	return out
}
