package xray

import (
	"os"
	"os/exec"
	"syscall"

	"xray-runner/internal/netcap"
)

// prepareChild has the kernel kill the core when the thread that started it
// exits — with this process, since the Go runtime never retires a thread that
// a goroutine is not locked to, and nothing starting a core locks one. SIGKILL:
// the core has nothing to save, and the kernel drops a TUN interface whose
// owner is gone.
//
// A run given CAP_NET_ADMIN with setcap hands it to the core, which creates the
// tun device and marks the sockets of its direct outbounds (H07). Handed down
// as an ambient capability, it keeps the parent-death signal: the core's
// permitted set does not grow past ours.
func prepareChild(cmd *exec.Cmd) {
	netcap.Prepare(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func adoptChild(*os.Process) {}
