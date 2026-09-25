// Package secret seals small blobs — the subscription list — with a key the
// operating system keeps for the user: DPAPI on Windows, the Secret Service
// (GNOME Keyring, KWallet) on Linux. A copy of the file alone is then no use:
// it opens only for the same user, on the same machine.
package secret

import (
	"bytes"
	"errors"
)

// Sealer seals and opens blobs for this user.
type Sealer interface {
	// Seal returns data sealed, with the header Sealed recognises.
	Seal(data []byte) ([]byte, error)
	// Open reverses Seal.
	Open(blob []byte) ([]byte, error)
	// Name says where the key lives, for a message.
	Name() string
}

// ErrUnavailable wraps the reason there is no Sealer here: no Secret Service
// on a headless server, say. The caller keeps the data unsealed then.
var ErrUnavailable = errors.New("хранилище ключей ОС недоступно")

// HelperArg is the hidden first argument that turns the program into the key
// helper (HelperMain): on Linux a run under sudo asks the invoking user's
// Secret Service through it, since root is not that user and has no session
// bus of its own.
const HelperArg = "__secret-key-helper"

// header opens every sealed blob: a name, a version and the method, so a blob
// is never mistaken for plain text, nor opened by the wrong method.
const header = "xray-runner sealed v1 "

// Sealed reports whether data is a sealed blob rather than plain text.
func Sealed(data []byte) bool { return bytes.HasPrefix(data, []byte(header)) }

// wrap and unwrap put the header on and check it off.
func wrap(method string, payload []byte) []byte {
	out := make([]byte, 0, len(header)+len(method)+1+len(payload))
	out = append(out, header...)
	out = append(out, method...)
	out = append(out, '\n')
	return append(out, payload...)
}

func unwrap(method string, blob []byte) ([]byte, error) {
	prefix := header + method + "\n"
	if !bytes.HasPrefix(blob, []byte(prefix)) {
		if Sealed(blob) {
			return nil, errors.New("файл зашифрован другим способом: он создан на другой системе")
		}
		return nil, errors.New("файл не зашифрован этой программой")
	}
	return blob[len(prefix):], nil
}
