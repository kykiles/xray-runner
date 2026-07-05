package system

import (
	"fmt"
	"os/exec"
	"strings"
)

const regKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

type ProxyState struct {
	Enabled   bool
	Server    string
	Overrides string
}

func ReadProxyState() ProxyState {
	var s ProxyState
	out, err := exec.Command("reg", "query", regKey, "/v", "ProxyEnable").Output()
	if err != nil {
		return s
	}
	s.Enabled = strings.Contains(string(out), "0x1")
	s.Server = queryRegString("ProxyServer")
	s.Overrides = queryRegString("ProxyOverride")
	return s
}

func queryRegString(name string) string {
	out, err := exec.Command("reg", "query", regKey, "/v", name).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "REG_SZ") {
			parts := strings.SplitN(line, "REG_SZ", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func WriteProxyState(s ProxyState) error {
	if s.Enabled {
		if err := execReg("add", regKey, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "1", "/f"); err != nil {
			return err
		}
		if s.Server != "" {
			if err := execReg("add", regKey, "/v", "ProxyServer", "/t", "REG_SZ", "/d", s.Server, "/f"); err != nil {
				return err
			}
		}
		if s.Overrides != "" {
			if err := execReg("add", regKey, "/v", "ProxyOverride", "/t", "REG_SZ", "/d", s.Overrides, "/f"); err != nil {
				return err
			}
		}
	} else {
		if err := execReg("add", regKey, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f"); err != nil {
			return err
		}
	}
	return nil
}

func execReg(args ...string) error {
	if out, err := exec.Command("reg", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("reg %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
