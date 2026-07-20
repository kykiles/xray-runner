package system

type ProxyState struct {
	Enabled   bool
	Server    string
	Overrides string
	// Mode is the desktop's raw proxy mode as read from it — GNOME's
	// none/manual/auto, KDE's numeric ProxyType. Enabled only says whether a
	// manual proxy was set, so restoring from it alone would turn a desktop
	// configured for PAC (auto) into none and silently drop that setting.
	// Unused on Windows, where PAC lives in AutoConfigURL and is never touched.
	Mode string
}
