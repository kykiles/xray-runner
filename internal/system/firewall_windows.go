//go:build windows && (amd64 || arm64)

package system

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The kill switch on Windows: a Windows Filtering Platform filter set (see
// killSwitchRules) in a dynamic session of this process's own. The netsh rules
// it once used blocked xray along with everything else — an explicit block
// beats an allow in Windows Firewall — while WFP lets our permits sit above
// our blocks in a sublayer of our own. Closing the session's handle drops the
// lot, and the handle closes with the process however it ends (H08).

var (
	ksMu sync.Mutex
	// ksEngine is the open session holding the filters; zero when the kill
	// switch is off.
	ksEngine windows.Handle
)

func EnableKillSwitch(cfg KillSwitchConfig) error {
	ksMu.Lock()
	defer ksMu.Unlock()
	slog.Info("enabling kill switch via WFP", "endpoints", len(cfg.Endpoints))

	// A second call replaces the set rather than stacking a second one on it.
	if err := closeKillSwitch(); err != nil {
		return err
	}

	var app *fwpByteBlob
	if cfg.Core != "" {
		var err error
		if app, err = wfpAppID(cfg.Core); err != nil {
			return fmt.Errorf("kill switch: %w", err)
		}
		defer wfpFree(app)
	}
	rules, err := killSwitchRules(cfg, app != nil)
	if err != nil {
		return err
	}

	engine, err := wfpOpenDynamic()
	if err != nil {
		return fmt.Errorf("kill switch: открыть WFP: %w", err)
	}
	if err := wfpInstall(engine, rules, app); err != nil {
		// The transaction is aborted; closing the session takes the sublayer
		// with it should anything have gone in outside it.
		_ = wfpClose(engine)
		return fmt.Errorf("kill switch: %w", err)
	}
	ksEngine = engine
	return nil
}

// DisableKillSwitch closes the session, which removes every filter in it.
// Nothing up is success.
func DisableKillSwitch() error {
	ksMu.Lock()
	defer ksMu.Unlock()
	return closeKillSwitch()
}

func closeKillSwitch() error {
	if ksEngine == 0 {
		return nil
	}
	slog.Info("disabling kill switch WFP filters")
	if err := wfpClose(ksEngine); err != nil {
		return fmt.Errorf("kill switch не снят: %w", err)
	}
	ksEngine = 0
	return nil
}

// --- fwpuclnt ----------------------------------------------------------------

var (
	fwpuclnt                      = windows.NewLazySystemDLL("fwpuclnt.dll")
	procFwpmEngineOpen0           = fwpuclnt.NewProc("FwpmEngineOpen0")
	procFwpmEngineClose0          = fwpuclnt.NewProc("FwpmEngineClose0")
	procFwpmTransactionBegin0     = fwpuclnt.NewProc("FwpmTransactionBegin0")
	procFwpmTransactionCommit0    = fwpuclnt.NewProc("FwpmTransactionCommit0")
	procFwpmTransactionAbort0     = fwpuclnt.NewProc("FwpmTransactionAbort0")
	procFwpmSubLayerAdd0          = fwpuclnt.NewProc("FwpmSubLayerAdd0")
	procFwpmFilterAdd0            = fwpuclnt.NewProc("FwpmFilterAdd0")
	procFwpmGetAppIdFromFileName0 = fwpuclnt.NewProc("FwpmGetAppIdFromFileName0")
	procFwpmFreeMemory0           = fwpuclnt.NewProc("FwpmFreeMemory0")
)

// The structures below mirror fwptypes.h and fwpmtypes.h for 64-bit Windows;
// the size checks after them keep the layout honest.

type fwpmDisplayData0 struct {
	name        *uint16
	description *uint16
}

type fwpmSession0 struct {
	sessionKey           windows.GUID
	displayData          fwpmDisplayData0
	flags                uint32
	txnWaitTimeoutInMSec uint32
	processID            uint32
	sid                  *windows.SID
	username             *uint16
	kernelMode           int32
}

type fwpByteBlob struct {
	size uint32
	data *uint8
}

type fwpmSublayer0 struct {
	subLayerKey  windows.GUID
	displayData  fwpmDisplayData0
	flags        uint32
	providerKey  *windows.GUID
	providerData fwpByteBlob
	weight       uint16
}

// fwpValue0 is FWP_VALUE0 and FWP_CONDITION_VALUE0 alike: a type tag and a
// union that holds a small integer in place or a pointer to anything else.
type fwpValue0 struct {
	typ   uint32
	value uintptr
}

type fwpmFilterCondition0 struct {
	fieldKey       windows.GUID
	matchType      uint32
	conditionValue fwpValue0
}

type fwpmAction0 struct {
	typ        uint32
	filterType windows.GUID
}

type fwpmFilter0 struct {
	filterKey           windows.GUID
	displayData         fwpmDisplayData0
	flags               uint32
	providerKey         *windows.GUID
	providerData        fwpByteBlob
	layerKey            windows.GUID
	subLayerKey         windows.GUID
	weight              fwpValue0
	numFilterConditions uint32
	filterCondition     *fwpmFilterCondition0
	action              fwpmAction0
	_                   [4]byte // the union after it holds a UINT64
	providerContextKey  windows.GUID
	reserved            *windows.GUID
	filterID            uint64
	effectiveWeight     fwpValue0
}

// Compile-time layout checks against the C sizes on 64-bit Windows.
var (
	_ [72 - unsafe.Sizeof(fwpmSession0{})]byte
	_ [unsafe.Sizeof(fwpmSession0{}) - 72]byte
	_ [72 - unsafe.Sizeof(fwpmSublayer0{})]byte
	_ [unsafe.Sizeof(fwpmSublayer0{}) - 72]byte
	_ [40 - unsafe.Sizeof(fwpmFilterCondition0{})]byte
	_ [unsafe.Sizeof(fwpmFilterCondition0{}) - 40]byte
	_ [200 - unsafe.Sizeof(fwpmFilter0{})]byte
	_ [unsafe.Sizeof(fwpmFilter0{}) - 200]byte
	_ [152 - unsafe.Offsetof(fwpmFilter0{}.providerContextKey)]byte
	_ [unsafe.Offsetof(fwpmFilter0{}.providerContextKey) - 152]byte
)

const (
	fwpmSessionFlagDynamic         = 0x1
	fwpmFilterFlagClearActionRight = 0x8
	fwpConditionFlagIsLoopback     = 0x1
	rpcCAuthnDefault               = 0xffffffff

	fwpActionBlock  = 0x1001
	fwpActionPermit = 0x1002

	fwpMatchEqual       = 0
	fwpMatchFlagsAllSet = 6

	fwpUint8          = 1
	fwpUint16         = 2
	fwpUint32         = 3
	fwpByteArray16    = 11
	fwpByteBlobType   = 12
	permitWeight      = 15
	blockWeight       = 0
	sublayerMaxWeight = 0xffff
)

var (
	layerGUIDs = map[wfpLayer]windows.GUID{
		// FWPM_LAYER_ALE_AUTH_CONNECT_V4/V6, FWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4/V6
		layerConnect4: {Data1: 0xc38d57d1, Data2: 0x05a7, Data3: 0x4c33, Data4: [8]byte{0x90, 0x4f, 0x7f, 0xbc, 0xee, 0xe6, 0x0e, 0x82}},
		layerConnect6: {Data1: 0x4a72393b, Data2: 0x319f, Data3: 0x44bc, Data4: [8]byte{0x84, 0xc3, 0xba, 0x54, 0xdc, 0xb3, 0xb6, 0xb4}},
		layerAccept4:  {Data1: 0xe1cd9fe7, Data2: 0xf4b5, Data3: 0x4273, Data4: [8]byte{0x96, 0xc0, 0x59, 0x2e, 0x48, 0x7b, 0x86, 0x50}},
		layerAccept6:  {Data1: 0xa3b42c97, Data2: 0x9f04, Data3: 0x4672, Data4: [8]byte{0xb8, 0x7e, 0xce, 0xe9, 0xc4, 0x83, 0x25, 0x7f}},
	}
	fieldGUIDs = map[wfpField]windows.GUID{
		// FWPM_CONDITION_FLAGS, _IP_LOCAL_ADDRESS, _IP_REMOTE_ADDRESS,
		// _IP_PROTOCOL, _IP_LOCAL_PORT (= _ICMP_TYPE), _IP_REMOTE_PORT, _ALE_APP_ID
		fieldLoopback:   {Data1: 0x632ce23b, Data2: 0x5167, Data3: 0x435c, Data4: [8]byte{0x86, 0xd7, 0xe9, 0x03, 0x68, 0x4a, 0xa8, 0x0c}},
		fieldLocalAddr:  {Data1: 0xd9ee00de, Data2: 0xc1ef, Data3: 0x4617, Data4: [8]byte{0xbf, 0xe3, 0xff, 0xd8, 0xf5, 0xa0, 0x89, 0x57}},
		fieldRemoteAddr: {Data1: 0xb235ae9a, Data2: 0x1d64, Data3: 0x49b8, Data4: [8]byte{0xa4, 0x4c, 0x5f, 0xf3, 0xd9, 0x09, 0x50, 0x45}},
		fieldProtocol:   {Data1: 0x3971ef2b, Data2: 0x623e, Data3: 0x4f9a, Data4: [8]byte{0x8c, 0xb1, 0x6e, 0x79, 0xb8, 0x06, 0xb9, 0xa7}},
		fieldLocalPort:  {Data1: 0x0c1ba1af, Data2: 0x5765, Data3: 0x453f, Data4: [8]byte{0xaf, 0x22, 0xa8, 0xf7, 0x91, 0xac, 0x77, 0x5b}},
		fieldRemotePort: {Data1: 0xc35a604d, Data2: 0xd22b, Data3: 0x4e1a, Data4: [8]byte{0x91, 0xb4, 0x68, 0xf6, 0x74, 0xee, 0x67, 0x4b}},
		fieldApp:        {Data1: 0xd78e1e87, Data2: 0x8644, Data3: 0x4ea5, Data4: [8]byte{0x94, 0x37, 0xd8, 0x09, 0xec, 0xef, 0xc9, 0x71}},
	}
)

// wfpDisplayName is what the filters are called in `netsh wfp show filters`.
const wfpDisplayName = "xray-runner kill switch"

func wfpErr(what string, r uintptr) error {
	if r == 0 {
		return nil
	}
	return fmt.Errorf("%s: 0x%08x: %w", what, uint32(r), windows.Errno(r))
}

func wfpOpenDynamic() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(wfpDisplayName)
	if err != nil {
		return 0, err
	}
	session := fwpmSession0{
		displayData: fwpmDisplayData0{name: name},
		flags:       fwpmSessionFlagDynamic,
	}
	var h windows.Handle
	r, _, _ := procFwpmEngineOpen0.Call(0, rpcCAuthnDefault, 0, uintptr(unsafe.Pointer(&session)), uintptr(unsafe.Pointer(&h))) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
	runtime.KeepAlive(name)
	if err := wfpErr("FwpmEngineOpen0", r); err != nil {
		return 0, err
	}
	return h, nil
}

func wfpClose(h windows.Handle) error {
	r, _, _ := procFwpmEngineClose0.Call(uintptr(h))
	return wfpErr("FwpmEngineClose0", r)
}

// wfpAppID asks WFP for the key its ALE layers know a program by: the file's
// NT device path. It is allocated by WFP and freed with wfpFree.
//
// The path is made long first. WFP turns the name it is given into the key
// as it stands, while the key a connection carries is always the long name, so
// a core under C:\Users\RUNNER~1\… — which is how %TEMP% often reads — would
// never match its own permit.
func wfpAppID(path string) (*fwpByteBlob, error) {
	long, err := longPath(path)
	if err != nil {
		return nil, fmt.Errorf("полный путь к %s: %w", path, err)
	}
	p, err := windows.UTF16PtrFromString(long)
	if err != nil {
		return nil, err
	}
	var blob *fwpByteBlob
	r, _, _ := procFwpmGetAppIdFromFileName0.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&blob))) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
	if err := wfpErr("FwpmGetAppIdFromFileName0 "+path, r); err != nil {
		return nil, err
	}
	return blob, nil
}

// wfpFree releases what WFP allocated. FwpmFreeMemory0 takes the address of
// the pointer, and clears it.
func longPath(path string) (string, error) {
	short, err := windows.UTF16FromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(&short[0], &buf[0], uint32(len(buf)))
	if err != nil {
		return "", err
	}
	if n > uint32(len(buf)) {
		return "", errors.New("путь слишком длинный")
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func wfpFree(p *fwpByteBlob) {
	_, _, _ = procFwpmFreeMemory0.Call(uintptr(unsafe.Pointer(&p))) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
}

// wfpInstall adds a sublayer and the rules in it in one transaction: the set
// goes in whole or not at all.
func wfpInstall(engine windows.Handle, rules []wfpRule, app *fwpByteBlob) (err error) {
	if r, _, _ := procFwpmTransactionBegin0.Call(uintptr(engine), 0); r != 0 {
		return wfpErr("FwpmTransactionBegin0", r)
	}
	defer func() {
		if err != nil {
			_, _, _ = procFwpmTransactionAbort0.Call(uintptr(engine))
		}
	}()

	name, err := windows.UTF16PtrFromString(wfpDisplayName)
	if err != nil {
		return err
	}
	key, err := windows.GenerateGUID()
	if err != nil {
		return err
	}
	sublayer := fwpmSublayer0{
		subLayerKey: key,
		displayData: fwpmDisplayData0{name: name},
		weight:      sublayerMaxWeight,
	}
	r, _, _ := procFwpmSubLayerAdd0.Call(uintptr(engine), uintptr(unsafe.Pointer(&sublayer)), 0) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
	if err := wfpErr("FwpmSubLayerAdd0", r); err != nil {
		return err
	}

	for _, rule := range rules {
		if err := wfpAddFilter(engine, key, name, rule, app); err != nil {
			return err
		}
	}
	r, _, _ = procFwpmTransactionCommit0.Call(uintptr(engine))
	runtime.KeepAlive(name)
	return wfpErr("FwpmTransactionCommit0", r)
}

func wfpAddFilter(engine windows.Handle, sublayer windows.GUID, name *uint16, rule wfpRule, app *fwpByteBlob) error {
	conds := make([]fwpmFilterCondition0, len(rule.conds))
	// The IPv6 addresses the conditions point at, kept alive until the call.
	addrs := make([][16]byte, len(rule.conds))
	for i, c := range rule.conds {
		fc := fwpmFilterCondition0{fieldKey: fieldGUIDs[c.field], matchType: fwpMatchEqual}
		switch c.field {
		case fieldLoopback:
			fc.matchType = fwpMatchFlagsAllSet
			fc.conditionValue = fwpValue0{typ: fwpUint32, value: fwpConditionFlagIsLoopback}
		case fieldLocalAddr, fieldRemoteAddr:
			if c.addr.Is4() {
				b := c.addr.As4()
				// FWP_UINT32 addresses are in host byte order.
				v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
				fc.conditionValue = fwpValue0{typ: fwpUint32, value: uintptr(v)}
			} else {
				addrs[i] = c.addr.As16()
				fc.conditionValue = fwpValue0{typ: fwpByteArray16, value: uintptr(unsafe.Pointer(&addrs[i]))} //nolint:gosec // G103: the union's pointer half; addrs is kept alive below
			}
		case fieldProtocol:
			fc.conditionValue = fwpValue0{typ: fwpUint8, value: uintptr(c.num)}
		case fieldLocalPort, fieldRemotePort:
			fc.conditionValue = fwpValue0{typ: fwpUint16, value: uintptr(c.num)}
		case fieldApp:
			if app == nil {
				return errors.New("правило для ядра без его App ID")
			}
			fc.conditionValue = fwpValue0{typ: fwpByteBlobType, value: uintptr(unsafe.Pointer(app))} //nolint:gosec // G103: the union's pointer half; WFP's memory, freed after the install
		default:
			return fmt.Errorf("неизвестное условие %d", c.field)
		}
		conds[i] = fc
	}

	f := fwpmFilter0{
		displayData: fwpmDisplayData0{name: name},
		layerKey:    layerGUIDs[rule.layer],
		subLayerKey: sublayer,
		weight:      fwpValue0{typ: fwpUint8, value: blockWeight},
		action:      fwpmAction0{typ: fwpActionBlock},
	}
	if rule.permit {
		// Cleared action right: a block in a sublayer below ours — Windows
		// Firewall's among them — cannot overturn what we let through.
		f.flags = fwpmFilterFlagClearActionRight
		f.weight.value = permitWeight
		f.action.typ = fwpActionPermit
	}
	if len(conds) > 0 {
		f.numFilterConditions = uint32(len(conds))
		f.filterCondition = &conds[0]
	}
	var id uint64
	r, _, _ := procFwpmFilterAdd0.Call(uintptr(engine), uintptr(unsafe.Pointer(&f)), 0, uintptr(unsafe.Pointer(&id))) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
	runtime.KeepAlive(conds)
	runtime.KeepAlive(addrs)
	return wfpErr(fmt.Sprintf("FwpmFilterAdd0 %s", rule.name), r)
}
