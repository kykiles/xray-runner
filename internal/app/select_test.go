package app

import (
	"errors"
	"testing"

	"xray-runner/internal/subscription"
)

func testEntries() []subscription.SubEntry {
	return []subscription.SubEntry{
		{Remarks: "Amsterdam-1", Address: "ams.example.com", Port: 443},
		{Remarks: "Frankfurt", Address: "fra.example.com", Port: 443},
		{Remarks: "Amsterdam-2", Address: "ams2.example.com", Port: 8443},
	}
}

func TestPickEntryByIndex(t *testing.T) {
	e, err := pickEntry(testEntries(), "2", nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Remarks != "Frankfurt" {
		t.Errorf("expected Frankfurt, got %s", e.Remarks)
	}
}

func TestPickEntryIndexOutOfRange(t *testing.T) {
	if _, err := pickEntry(testEntries(), "5", nil, false); !errors.Is(err, ErrSelection) {
		t.Errorf("expected ErrSelection, got %v", err)
	}
}

func TestPickEntryByExactName(t *testing.T) {
	e, err := pickEntry(testEntries(), "frankfurt", nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Remarks != "Frankfurt" {
		t.Errorf("expected Frankfurt, got %s", e.Remarks)
	}
}

func TestPickEntryAmbiguousSubstring(t *testing.T) {
	if _, err := pickEntry(testEntries(), "amsterdam", nil, false); !errors.Is(err, ErrSelection) {
		t.Errorf("expected ErrSelection for ambiguous match, got %v", err)
	}
}

func TestPickEntryUniqueSubstring(t *testing.T) {
	e, err := pickEntry(testEntries(), "ams2", nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Remarks != "Amsterdam-2" {
		t.Errorf("expected Amsterdam-2, got %s", e.Remarks)
	}
}

func TestPickEntryNotFound(t *testing.T) {
	if _, err := pickEntry(testEntries(), "tokyo", nil, false); !errors.Is(err, ErrSelection) {
		t.Errorf("expected ErrSelection, got %v", err)
	}
}

func TestPickEntryFromState(t *testing.T) {
	state := &LastState{ServerAddress: "fra.example.com", ServerPort: 443}
	e, err := pickEntry(testEntries(), "", state, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Remarks != "Frankfurt" {
		t.Errorf("expected Frankfurt, got %s", e.Remarks)
	}
}

func TestPickEntryFromStateByRemarks(t *testing.T) {
	// Address changed but remarks survived — fall back to remarks match.
	state := &LastState{ServerRemarks: "Frankfurt", ServerAddress: "old.example.com", ServerPort: 443}
	e, err := pickEntry(testEntries(), "", state, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Remarks != "Frankfurt" {
		t.Errorf("expected Frankfurt, got %s", e.Remarks)
	}
}

func TestPickEntryStateGone(t *testing.T) {
	state := &LastState{ServerRemarks: "Tokyo", ServerAddress: "tok.example.com", ServerPort: 443}
	if _, err := pickEntry(testEntries(), "", state, true); !errors.Is(err, ErrSelection) {
		t.Errorf("expected ErrSelection, got %v", err)
	}
}

func TestPickEntryNothingRequested(t *testing.T) {
	if _, err := pickEntry(testEntries(), "", nil, false); !errors.Is(err, ErrSelection) {
		t.Errorf("expected ErrSelection, got %v", err)
	}
}
