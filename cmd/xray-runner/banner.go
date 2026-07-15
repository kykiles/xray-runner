package main

import (
	"fmt"
	"runtime"
	"strings"

	"xray-runner/internal/xray"
)

// banner is the only thing printed before the first TUI screen: the runner
// version, the core version and the platform. Everything else goes to the log.
func banner() string {
	return fmt.Sprintf("  xray-runner %s · %s · %s/%s\n",
		Version, coreVersion(), runtime.GOOS, runtime.GOARCH)
}

// coreVersion reports the Xray-core version, or why it is unavailable — a
// missing core is worth seeing at startup rather than at the first connect.
func coreVersion() string {
	bin, err := xray.FindBinary()
	if err != nil {
		return "Xray-core не найден"
	}
	v, err := xray.Version(bin)
	if err != nil {
		return "Xray-core ?"
	}
	return "Xray-core " + parseCoreVersion(v)
}

// parseCoreVersion pulls the number out of `xray version`'s first line, which
// looks like "Xray 25.9.11 (Xray, Penetrates Everything.) Custom (go1.24.7 ...)".
func parseCoreVersion(line string) string {
	fields := strings.Fields(line)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "xray") {
		return fields[1]
	}
	return line
}
