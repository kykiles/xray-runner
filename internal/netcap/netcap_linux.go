//go:build linux

package netcap

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// capNetAdmin is CAP_NET_ADMIN's number.
const capNetAdmin = 12

var (
	// statusPath and geteuid are overridable in tests.
	statusPath = "/proc/self/status"
	geteuid    = os.Geteuid
	held       = sync.OnceValue(func() bool { return permitted(statusPath) })
)

// permitted reports whether CAP_NET_ADMIN is in the permitted set the status
// file lists. Permitted, not effective: a child gets it as ambient from the
// permitted set, whether setcap was given +ep or +p.
func permitted(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "CapPrm:"); ok {
			mask, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return err == nil && mask&(1<<capNetAdmin) != 0
		}
	}
	return false
}

// Delegated reports a run that is not root but holds CAP_NET_ADMIN — from a
// file capability set with setcap. It may change the network, and passes the
// right on to the programs it starts for that.
func Delegated() bool { return geteuid() != 0 && held() }

// Ambient is the capability set a program that changes the network is started
// with: CAP_NET_ADMIN for a Delegated run, nothing otherwise — root's children
// have every capability anyway, and a run without it has none to give.
func Ambient() []uintptr {
	if Delegated() {
		return []uintptr{capNetAdmin}
	}
	return nil
}

// Prepare has cmd started with Ambient, for a program that changes the network.
func Prepare(cmd *exec.Cmd) {
	caps := Ambient()
	if caps == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AmbientCaps = caps
}
