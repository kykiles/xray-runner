package system

type ProxyState struct {
	Enabled   bool
	Server    string
	Overrides string
	// Read reports that the snapshot really came off the machine. Windows
	// restores exactly, so it must delete values that did not exist before —
	// and a zero ProxyState is indistinguishable from "everything was empty".
	// Restoring from an unread snapshot would wipe the user's real settings,
	// so it is refused instead. Unused on Linux.
	Read bool
	// EnabledSet, ServerSet and OverridesSet record which values existed at
	// snapshot time. What existed is written back, what did not is deleted.
	// Unused on Linux.
	EnabledSet   bool
	ServerSet    bool
	OverridesSet bool
	// Mode is the desktop's raw proxy mode as read from it — GNOME's
	// none/manual/auto, KDE's numeric ProxyType. Enabled only says whether a
	// manual proxy was set, so restoring from it alone would turn a desktop
	// configured for PAC (auto) into none and silently drop that setting.
	// Unused on Windows, where PAC lives in AutoConfigURL and is never touched.
	Mode string
}
