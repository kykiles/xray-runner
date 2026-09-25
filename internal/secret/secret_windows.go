//go:build windows

package secret

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Default is DPAPI in the user's scope: the key is derived from the user's
// logon secret, so the blob opens for the same user on the same machine and
// for nobody else — an administrator reading the file included. There is
// nothing to be unavailable.
func Default() (Sealer, error) { return dpapi{}, nil }

type dpapi struct{}

func (dpapi) Name() string { return "DPAPI" }

// entropy ties the blob to this program: another program running as the user
// cannot open it with a bare CryptUnprotectData.
var entropy = []byte("xray-runner subscriptions")

// dpapiVersion goes in front of the data before it is sealed. DPAPI refuses an
// empty input ("the parameter is incorrect"), and an empty list — the last
// subscription removed — is a list like any other.
const dpapiVersion = 1

func (dpapi) Seal(data []byte) ([]byte, error) {
	out, err := dpapiCall(append([]byte{dpapiVersion}, data...), true)
	if err != nil {
		return nil, fmt.Errorf("DPAPI: зашифровать: %w", err)
	}
	return wrap("dpapi", out), nil
}

func (dpapi) Open(blob []byte) ([]byte, error) {
	payload, err := unwrap("dpapi", blob)
	if err != nil {
		return nil, err
	}
	out, err := dpapiCall(payload, false)
	if err != nil {
		return nil, fmt.Errorf("DPAPI: расшифровать (файл создан другим пользователем Windows или на другом компьютере?): %w", err)
	}
	if len(out) == 0 || out[0] != dpapiVersion {
		return nil, errors.New("DPAPI: неизвестный формат файла")
	}
	return out[1:], nil
}

func dpapiCall(in []byte, protect bool) ([]byte, error) {
	var inBlob, outBlob windows.DataBlob
	if len(in) > 0 {
		inBlob = windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}
	}
	ent := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var err error
	if protect {
		err = windows.CryptProtectData(&inBlob, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob)
	} else {
		err = windows.CryptUnprotectData(&inBlob, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data))) }() //nolint:gosec // G103: DPAPI hands back LocalAlloc'd memory
	return append([]byte(nil), unsafe.Slice(outBlob.Data, outBlob.Size)...), nil              //nolint:gosec // G103: the output blob DPAPI filled in
}

// HelperMain has no use here: an elevated process on Windows runs as the same
// user, and DPAPI answers it directly.
func HelperMain() int { return 2 }
