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
	// snapshot time. What existed is written back, what did not is deleted. On
	// Linux ServerSet and OverridesSet mark values the snapshot actually read:
	// only those are written back, so a desktop whose proxy was not manual gets
	// its own host, port and exceptions back instead of ours (G09), and
	// ClearProxy, which read nothing, writes nothing but the mode.
	EnabledSet   bool
	ServerSet    bool
	OverridesSet bool
	// SecureServer is the https proxy as "host:port", which GNOME and KDE keep
	// apart from the http one. Linux only; empty means the same as Server.
	SecureServer string
	// Mode is the desktop's raw proxy mode as read from it — GNOME's
	// none/manual/auto, KDE's numeric ProxyType. Enabled only says whether a
	// manual proxy was set, so restoring from it alone would turn a desktop
	// configured for PAC (auto) into none and silently drop that setting.
	// Unused on Windows, where PAC lives in AutoConfigURL and is never touched.
	Mode string
}
