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
	// Mode is the proxy/tun mode the user last switched to, so a restart comes
	// back up the way they left it. Empty means "never switched" — cfg.Mode wins.
	Mode string `json:"mode,omitempty"`
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

// updateState edits the saved state in place. The server selection and the mode
// are written by unrelated actions, so each one must read what the other left
// behind instead of overwriting the whole file from its own partial view.
// A missing or corrupted file starts from an empty state rather than failing:
// this is a convenience record, not something worth blocking a connection over.
func updateState(edit func(*LastState)) error {
	s, err := loadLastState()
	if err != nil {
		s = &LastState{}
	}
	edit(s)
	return saveLastState(*s)
}
