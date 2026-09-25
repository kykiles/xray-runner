package system

import (
	"os/exec"

	"xray-runner/internal/netcap"
)

// commander abstracts system command execution so firewall and routing logic
// can be unit-tested without root privileges or real network changes.
type commander interface {
	lookPath(bin string) error
	run(bin string, args ...string) ([]byte, error)
}

// execCommander runs the external tools that are left: PowerShell, route and
// netsh on Windows; on Linux ss for the split and iptables for the one-off
// cleanup of a kill switch from before H09 — routes and netfilter are spoken
// to over netlink. A run given CAP_NET_ADMIN with setcap finds them in the
// system directories only and hands them the capability; see netcap (H07).
type execCommander struct{}

func (execCommander) lookPath(bin string) error {
	_, err := netcap.LookPath(bin)
	return err
}

func (execCommander) run(bin string, args ...string) ([]byte, error) {
	path, err := netcap.LookPath(bin)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...) //nolint:gosec // G204: argument vector, no shell: a system tool, resolved above
	netcap.Prepare(cmd)
	return cmd.CombinedOutput()
}
