package config

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const hwidFile = "hwid.txt"

func GetOrCreateHWID(override string) string {
	if override != "" {
		return override
	}

	// Backward compatibility: an existing ./hwid.txt from older versions wins,
	// so a device keeps its identity and the panel doesn't see it as new.
	// Trimmed because the HWID goes into the x-hwid request header, and a stray
	// newline makes net/http reject the whole request — every subscription then
	// fails with an error naming neither this file nor the newline.
	if hwid := readHWID(hwidFile); hwid != "" {
		return hwid
	}

	path := hwidPath()
	if hwid := readHWID(path); hwid != "" {
		return hwid
	}

	hwid := generateHWID()
	if err := writeHWID(path, hwid); err != nil {
		// R-6: a swallowed write error means a new HWID every launch, which the
		// panel counts as a new device and may exhaust the subscription limit.
		slog.Warn("failed to persist HWID, a new one will be generated next launch", "path", path, "error", err)
	}
	return hwid
}

// readHWID returns the trimmed HWID stored at path, empty when there is none.
func readHWID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// hwidPath returns the preferred HWID location in the data dir, falling back to
// CWD when it is unavailable. Under sudo the data dir belongs to the invoking
// user, so a TUN run and a proxy run report the same device to the panel.
func hwidPath() string {
	dir := DataDir()
	if dir == "" {
		return hwidFile
	}
	return filepath.Join(dir, "hwid")
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
