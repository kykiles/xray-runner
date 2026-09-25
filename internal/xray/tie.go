package xray

// The core has to go when this program does, however it goes (G07). Our own
// teardown stops it on every orderly exit, but a kill -9, a crash or a panic
// runs no teardown, and a core left behind keeps the local ports or the TUN
// interface: the next start then refuses as if a second instance were running,
// while the lock says there is none. prepareChild and adoptChild ask the OS
// to end the core with us — per platform, in tie_*.go.
