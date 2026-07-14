package app

// Persisted "last used server" state for --last / non-interactive runs (U-2).

import (
	"encoding/json"
	"fmt"
	"os"
)

const stateFile = "last_server.json"

type LastState struct {
	SubscriptionURL string `json:"subscription_url"`
	ServerRemarks   string `json:"server_remarks"`
	ServerAddress   string `json:"server_address"`
	ServerPort      int    `json:"server_port"`
}

func loadLastState() (*LastState, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}
	var s LastState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state file %s corrupted: %w", stateFile, err)
	}
	return &s, nil
}

// saveLastState is best-effort: a failure must never break the connection flow,
// so it only returns the error for logging. 0600 — the file names the server.
func saveLastState(s LastState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, data, 0600)
}
