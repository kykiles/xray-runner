// Package e2e drives the built xray-runner end to end on a real machine: the
// real core, the real routing table and system proxy, a local VLESS server
// standing in for the VPN. The tests carry the e2e build tag and run in their
// own CI job (.github/workflows/e2e-windows.yml); `go test ./...` skips them.
//
// Locally, on an elevated Windows shell, with the core bundle unpacked:
//
//	$env:E2E_CORE_DIR = "C:\path\to\Xray-windows-64"
//	go test -tags e2e -v -count=1 ./e2e/
//
// They change the machine's routes and proxy settings while they run and put
// both back; run them on a machine you can afford to lose the network on for a
// minute, not on one you are working on remotely.
package e2e
