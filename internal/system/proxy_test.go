//go:build windows

package system

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// fakeRegistry replaces the HKCU seam with an in-memory key for the duration
// of the test. Values it does not hold report registry.ErrNotExist, exactly as
// the real API does.
type fakeRegistry struct {
	ints    map[string]uint64
	strs    map[string]string
	deleted []string
	readErr error
}

func useFakeRegistry(t *testing.T, f *fakeRegistry) *fakeRegistry {
	t.Helper()
	if f.ints == nil {
		f.ints = map[string]uint64{}
	}
	if f.strs == nil {
		f.strs = map[string]string{}
	}

	oldGetInt, oldGetString := regGetInt, regGetString
	oldSetInt, oldSetString, oldDelete := regSetInt, regSetString, regDelete
	t.Cleanup(func() {
		regGetInt, regGetString = oldGetInt, oldGetString
		regSetInt, regSetString, regDelete = oldSetInt, oldSetString, oldDelete
	})

	regGetInt = func(name string) (uint64, error) {
		if f.readErr != nil {
			return 0, f.readErr
		}
		v, ok := f.ints[name]
		if !ok {
			return 0, registry.ErrNotExist
		}
		return v, nil
	}
	regGetString = func(name string) (string, error) {
		if f.readErr != nil {
			return "", f.readErr
		}
		v, ok := f.strs[name]
		if !ok {
			return "", registry.ErrNotExist
		}
		return v, nil
	}
	regSetInt = func(name string, v uint32) error {
		f.ints[name] = uint64(v)
		return nil
	}
	regSetString = func(name, v string) error {
		f.strs[name] = v
		return nil
	}
	regDelete = func(name string) error {
		_, isInt := f.ints[name]
		_, isStr := f.strs[name]
		if !isInt && !isStr {
			return registry.ErrNotExist
		}
		delete(f.ints, name)
		delete(f.strs, name)
		f.deleted = append(f.deleted, name)
		return nil
	}
	return f
}

func TestReadProxyStateDisabled(t *testing.T) {
	useFakeRegistry(t, &fakeRegistry{ints: map[string]uint64{"ProxyEnable": 0}})

	s := ReadProxyState()
	if s.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	if !s.Read {
		t.Errorf("Read = false, want true: the key was readable")
	}
}

func TestReadProxyStateEnabled(t *testing.T) {
	useFakeRegistry(t, &fakeRegistry{
		ints: map[string]uint64{"ProxyEnable": 1},
		strs: map[string]string{
			"ProxyServer":   "127.0.0.1:8888",
			"ProxyOverride": "<-loopback>;*.local",
		},
	})

	s := ReadProxyState()
	if !s.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if s.Server != "127.0.0.1:8888" {
		t.Errorf("Server = %q, want %q", s.Server, "127.0.0.1:8888")
	}
	if s.Overrides != "<-loopback>;*.local" {
		t.Errorf("Overrides = %q, want %q", s.Overrides, "<-loopback>;*.local")
	}
	if !s.ServerSet || !s.OverridesSet || !s.EnabledSet {
		t.Errorf("want every value marked as present, got %+v", s)
	}
}

// TestReadProxyStateMarksMissingValues is the A12 core: a value that is absent
// must be remembered as absent, so that restore deletes ours instead of
// leaving 127.0.0.1:10809 behind.
func TestReadProxyStateMarksMissingValues(t *testing.T) {
	useFakeRegistry(t, &fakeRegistry{ints: map[string]uint64{"ProxyEnable": 0}})

	s := ReadProxyState()
	if !s.Read {
		t.Fatalf("Read = false, want true")
	}
	if s.ServerSet || s.OverridesSet {
		t.Errorf("ServerSet=%v OverridesSet=%v, want both false", s.ServerSet, s.OverridesSet)
	}
}

// TestUnreadableKeyIsNotTreatedAsEmpty: a read failure must not look like "the
// proxy was off and everything was empty", which would authorise deletion.
func TestUnreadableKeyIsNotTreatedAsEmpty(t *testing.T) {
	useFakeRegistry(t, &fakeRegistry{readErr: errors.New("access denied")})

	s := ReadProxyState()
	if s.Read {
		t.Errorf("Read = true after a failed read, want false")
	}
	if err := WriteProxyState(s); err == nil {
		t.Errorf("WriteProxyState accepted an unread state, want refusal")
	}
}

// TestDwordIsParsedNotSubstringMatched: the old code did
// strings.Contains(out, "0x1"), so 0x10 and 0x12 read as "enabled".
func TestDwordIsParsedNotSubstringMatched(t *testing.T) {
	for _, v := range []uint64{0x10, 0x12, 0x21} {
		useFakeRegistry(t, &fakeRegistry{ints: map[string]uint64{"ProxyEnable": v}})
		if s := ReadProxyState(); !s.Enabled {
			t.Errorf("ProxyEnable=%#x: Enabled = false, want true", v)
		}
	}
	useFakeRegistry(t, &fakeRegistry{ints: map[string]uint64{"ProxyEnable": 0}})
	if s := ReadProxyState(); s.Enabled {
		t.Errorf("ProxyEnable=0: Enabled = true, want false")
	}
}
