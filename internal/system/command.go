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

// execCommander runs ip, iptables, nft and ss. A run given CAP_NET_ADMIN with
// setcap finds them in the system directories only and hands them the
// capability; see netcap (H07).
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
	cmd := exec.Command(path, args...) //nolint:gosec // G204: argument vector, no shell: ip, iptables, nft or ss, resolved above
	netcap.Prepare(cmd)
	return cmd.CombinedOutput()
}
