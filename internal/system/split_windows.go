//go:build windows

package system

import (
	"errors"
	"sort"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SplitOverTUN says how per-process routing is built here. On Windows the
// tunnel takes everything and xray decides per connection, matching the owning
// process itself (ADR-0003) — so the split mode is the TUN plumbing plus a
// routing rule, and this package has nothing to install or tear down.
const SplitOverTUN = true

// ListProcesses returns the distinct names of the running processes, sorted, as
// the picker shows them — with the .exe suffix, which is what xray matches
// against.
func ListProcesses() ([]Process, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	counts := map[string]int{}
	for err = windows.Process32First(snapshot, &e); err == nil; err = windows.Process32Next(snapshot, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		if name == "" {
			continue
		}
		counts[name]++
	}
	// ERROR_NO_MORE_FILES is how the walk ends; anything else lost us entries.
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}

	out := make([]Process, 0, len(counts))
	for name, n := range counts {
		out = append(out, Process{Name: name, PIDs: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// EnableSplit/RefreshSplit/DisableSplit have nothing to do here: the tunnel
// carries the traffic and xray matches the process, so there is no ruleset to
// install, rescan or tear down (ADR-0003).
func EnableSplit(names []string, tcpPort, dnsPort int) ([]string, error) { return nil, nil }

func RefreshSplit(names []string) ([]string, error) { return nil, nil }

func DisableSplit() error { return nil }
