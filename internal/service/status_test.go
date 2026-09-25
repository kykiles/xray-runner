package service

import (
	"errors"
	"fmt"
	"testing"

	"xray-runner/internal/ipc"
)

// `service status` names why the service did not answer. ipc.Connect wraps the
// reason with two %w, and errors.Unwrap made every one of them "<nil>".
func TestUnreachableNamesTheReason(t *testing.T) {
	why := errors.New("dial unix /run/xray-runner/control.sock: connect: permission denied")
	if got := unreachable(fmt.Errorf("%w: %w", ipc.ErrUnavailable, why)); got != why.Error() {
		t.Errorf("unreachable = %q, want %q", got, why)
	}
	if got := unreachable(why); got != why.Error() {
		t.Errorf("unreachable = %q, want the error as it is", got)
	}
}
