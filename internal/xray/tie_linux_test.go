package xray

// coreGone: no process, or a zombie waiting for whoever inherited it — either
// way, not running.
func coreGone(pid int) bool {
	state, ok := procState(pid)
	return !ok || state == "Z"
}
