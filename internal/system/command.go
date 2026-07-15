package system

import "os/exec"

// commander abstracts system command execution so firewall and routing logic
// can be unit-tested without root privileges or real network changes.
type commander interface {
	lookPath(bin string) error
	run(bin string, args ...string) ([]byte, error)
}

type execCommander struct{}

func (execCommander) lookPath(bin string) error {
	_, err := exec.LookPath(bin)
	return err
}

func (execCommander) run(bin string, args ...string) ([]byte, error) {
	return exec.Command(bin, args...).CombinedOutput()
}
