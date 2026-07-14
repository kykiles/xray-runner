package config

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
)

const hwidFile = "hwid.txt"

func GetOrCreateHWID(override string) string {
	if override != "" {
		return override
	}

	// Backward compatibility: an existing ./hwid.txt from older versions wins,
	// so a device keeps its identity and the panel doesn't see it as new.
	if data, err := os.ReadFile(hwidFile); err == nil && len(data) > 0 {
		return string(data)
	}

	path := hwidPath()
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return string(data)
	}

	hwid := generateHWID()
	if err := writeHWID(path, hwid); err != nil {
		// R-6: a swallowed write error means a new HWID every launch, which the
		// panel counts as a new device and may exhaust the subscription limit.
		slog.Warn("failed to persist HWID, a new one will be generated next launch", "path", path, "error", err)
	}
	return hwid
}

// hwidPath returns the preferred HWID location under the user config dir,
// falling back to CWD when it is unavailable.
func hwidPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return hwidFile
	}
	return filepath.Join(dir, "xray-runner", "hwid")
}

func writeHWID(path, hwid string) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(hwid), 0600)
}

func generateHWID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
