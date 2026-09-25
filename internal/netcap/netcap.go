// Package netcap covers running the network side of a TUN session without
// root: the binary is given CAP_NET_ADMIN with setcap, and passes it on to the
// programs it starts to change the network — the core, which creates the tun
// device and marks its sockets, and ip, iptables and nft (H07).
//
// A capability from a file does not survive an exec on its own: it goes to a
// child only as an ambient capability, raised on the way. And the programs
// started with it are looked up where only root can put them, not on the
// caller's PATH — otherwise the binary would hand the capability to whatever
// its caller named ip.
package netcap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SystemDirs are where Delegated runs look the network tools up: directories
// only root can write, the ones sudo's secure_path names.
var SystemDirs = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// LookPath finds a program to run with the capability. A Delegated run looks
// only in SystemDirs; any other run takes PATH as it is — root's is its own
// choice, and a run without the capability has nothing to hand on.
func LookPath(name string) (string, error) {
	if !Delegated() {
		return exec.LookPath(name)
	}
	for _, dir := range SystemDirs {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: notInSystemDirs{}}
}

// notInSystemDirs is exec.ErrNotFound for a lookup confined to SystemDirs, so
// a caller that tells a missing tool apart keeps doing so.
type notInSystemDirs struct{}

func (notInSystemDirs) Error() string {
	return "нет ни в одной из системных папок " + strings.Join(SystemDirs, ", ")
}

func (notInSystemDirs) Is(target error) bool { return target == exec.ErrNotFound }
