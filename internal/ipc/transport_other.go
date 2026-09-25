//go:build !linux && !windows

package ipc

import (
	"context"
	"errors"
	"io"
)

var errNoService = errors.New("служба не поддерживается на этой системе")

func dial(context.Context) (io.ReadWriteCloser, error) { return nil, errNoService }

// Listen has nothing to listen on here.
func Listen() (Listener, error) { return nil, errNoService }
