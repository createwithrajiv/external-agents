package aider

import (
	"os"
	"path/filepath"
	"testing"
)

// The fingerprint carries more than the turn index because aider's bell is not
// a turn-end signal — it also rings from confirm_ask and prompt_ask, and one
// turn can emit several usage reports during a lint/test reflection loop.
func TestTurnFingerprint_AdvancedOver(t *testing.T) {
	base := turnFingerprint{TurnIndex: 2, ReportCount: 1, EditCount: 1}

	tests := []struct {
		name string
		next turnFingerprint
		want bool
	}{
		{"identical does not advance", base, false},
		{"higher turn advances", turnFingerprint{TurnIndex: 3}, true},
		{"lower turn never advances even with more work",
			turnFingerprint{TurnIndex: 1, ReportCount: 99, EditCount: 99}, false},
		{"same turn, extra report advances",
			turnFingerprint{TurnIndex: 2, ReportCount: 2, EditCount: 1}, true},
		{"same turn, extra edit advances",
			turnFingerprint{TurnIndex: 2, ReportCount: 1, EditCount: 2}, true},
		{"same turn, fewer reports does not advance",
			turnFingerprint{TurnIndex: 2, ReportCount: 0, EditCount: 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.next.advancedOver(base); got != tc.want {
				t.Errorf("advancedOver = %v, want %v", got, tc.want)
			}
		})
	}
}

// A fresh session must report turn -1 so that turn 0 is correctly seen as new.
func TestLoadState_FreshSession(t *testing.T) {
	st := loadState(t.TempDir())
	if st.LastTurnEnd.TurnIndex != -1 {
		t.Errorf("LastTurnEnd.TurnIndex = %d, want -1 so turn 0 counts as progress", st.LastTurnEnd.TurnIndex)
	}
	if !(turnFingerprint{TurnIndex: 0}).advancedOver(st.LastTurnEnd) {
		t.Error("turn 0 must be treated as new progress in a fresh session")
	}
}

// Corruption costs at most one redundant checkpoint. Failing the hook would
// cost the user a warning in their aider session on every turn.
func TestLoadState_CorruptFileIsNotFatal(t *testing.T) {
	for _, content := range []string{"", "{not json", "null", "[]", "\x00\xff"} {
		dir := t.TempDir()
		if err := os.WriteFile(statePath(dir), []byte(content), 0o600); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		st := loadState(dir)
		if st.LastTurnEnd.TurnIndex != -1 {
			t.Errorf("corrupt state %q gave TurnIndex %d, want the fresh value -1",
				content, st.LastTurnEnd.TurnIndex)
		}
	}
}

func TestSaveLoadState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := sessionState{
		SessionID:   "20260906-112206",
		LastTurnEnd: turnFingerprint{TurnIndex: 4, ReportCount: 2, EditCount: 3},
	}
	if err := saveState(dir, want); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got := loadState(dir)
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestSaveState_CreatesDirAndLeavesNoTempFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist")
	if err := saveState(dir, sessionState{SessionID: "x"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	if _, err := os.Stat(statePath(dir)); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file %q left behind after a successful save", e.Name())
		}
	}
}

// Overwriting must not accumulate state or leave the previous value behind.
func TestSaveState_Overwrites(t *testing.T) {
	dir := t.TempDir()
	if err := saveState(dir, sessionState{SessionID: "first", LastTurnEnd: turnFingerprint{TurnIndex: 1}}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := saveState(dir, sessionState{SessionID: "second", LastTurnEnd: turnFingerprint{TurnIndex: 9}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got := loadState(dir)
	if got.SessionID != "second" || got.LastTurnEnd.TurnIndex != 9 {
		t.Errorf("got %+v, want the second write", got)
	}
}

func TestFingerprintOf(t *testing.T) {
	if _, ok := fingerprintOf(nil); ok {
		t.Error("empty turn slice should report ok=false")
	}

	turns := parseTurns(fixture(t))
	fp, ok := fingerprintOf(turns)
	if !ok {
		t.Fatal("fingerprintOf reported not-ok for a populated transcript")
	}
	// It must describe the LAST turn, which is the one that just completed.
	last := turns[len(turns)-1]
	if fp.TurnIndex != last.Index {
		t.Errorf("TurnIndex = %d, want %d (the last turn)", fp.TurnIndex, last.Index)
	}
	if fp.ReportCount != last.Usage.Reports {
		t.Errorf("ReportCount = %d, want %d", fp.ReportCount, last.Usage.Reports)
	}
	if fp.EditCount != len(last.ModifiedFiles) {
		t.Errorf("EditCount = %d, want %d", fp.EditCount, len(last.ModifiedFiles))
	}
}

func TestTokenReport_Add(t *testing.T) {
	var total tokenReport
	total.add(tokenReport{Sent: 100, Received: 10, Reports: 1, MessageCost: 0.01, SessionCost: 0.01})
	total.add(tokenReport{Sent: 200, Received: 20, Reports: 1, MessageCost: 0.02, SessionCost: 0.03})

	if total.Sent != 300 || total.Received != 30 || total.Reports != 2 {
		t.Errorf("sums wrong: %+v", total)
	}
	// Message costs add up; the session figure is already cumulative in aider,
	// so the maximum is the meaningful one.
	if total.MessageCost < 0.029 || total.MessageCost > 0.031 {
		t.Errorf("MessageCost = %v, want ~0.03", total.MessageCost)
	}
	if total.SessionCost != 0.03 {
		t.Errorf("SessionCost = %v, want the maximum 0.03", total.SessionCost)
	}
}
