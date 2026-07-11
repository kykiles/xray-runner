package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
)

const hwidFile = "hwid.txt"

func GetOrCreateHWID(override string) string {
	if override != "" {
		return override
	}

	data, err := os.ReadFile(hwidFile)
	if err == nil && len(data) > 0 {
		return string(data)
	}

	hwid := generateHWID()
	_ = os.WriteFile(hwidFile, []byte(hwid), 0644)
	return hwid
}

func generateHWID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
