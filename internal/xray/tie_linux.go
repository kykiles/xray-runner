package xray

import (
	"os"
	"os/exec"
	"syscall"
)

// prepareChild has the kernel kill the core when the thread that started it
// exits — with this process, since the Go runtime never retires a thread that
// a goroutine is not locked to, and nothing starting a core locks one. SIGKILL:
// the core has nothing to save, and the kernel drops a TUN interface whose
// owner is gone.
func prepareChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func adoptChild(*os.Process) {}
