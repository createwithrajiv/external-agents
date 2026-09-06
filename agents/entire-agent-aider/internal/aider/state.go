package aider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// turnFingerprint identifies how far a session has progressed.
//
// A plain turn index cannot decide whether a turn ended, because aider's
// notification is not a turn-end signal. IO.ring_bell fires at the first
// interactive prompt after IO.llm_started, and Coder.send_message reaches those
// prompts in this order:
//
//	base_coder.py:1423  llm_started()        -> arms the bell
//	base_coder.py:1531  show_usage_report()  -> writes "> Tokens: ..."
//	base_coder.py:1585  apply_updates()      -> writes "> Applied edit to ..."
//	base_coder.py:1589  auto_commit()        -> git commit
//
// A confirmation prompt inside apply_updates therefore rings the bell after the
// usage report but BEFORE the commit, while a turn needing no confirmation
// rings it at get_input() after everything. One turn can also produce several
// usage reports when a lint or test reflection loop runs.
//
// Comparing the whole fingerprint means a later firing that observes genuinely
// more work — another API call, another applied edit — counts as progress,
// while one that observes nothing new is dropped.
type turnFingerprint struct {
	TurnIndex   int `json:"turn_index"`
	ReportCount int `json:"report_count"`
	EditCount   int `json:"edit_count"`
}

func (f turnFingerprint) advancedOver(prev turnFingerprint) bool {
	if f.TurnIndex != prev.TurnIndex {
		return f.TurnIndex > prev.TurnIndex
	}
	return f.ReportCount > prev.ReportCount || f.EditCount > prev.EditCount
}

// sessionState is the dedup bookkeeping, kept on disk because each turn-end
// runs in a fresh process that aider spawned.
type sessionState struct {
	SessionID   string          `json:"session_id"`
	LastTurnEnd turnFingerprint `json:"last_turn_end"`
}

func statePath(sessionDir string) string {
	return filepath.Join(sessionDir, stateFileName)
}

// loadState reads session state, treating an absent or unparseable file as a
// fresh session.
//
// Corruption is deliberately non-fatal. The file exists only to suppress
// duplicate checkpoints; losing it costs at most one redundant checkpoint,
// whereas failing the hook would surface a warning in the user's aider session
// on every turn.
func loadState(sessionDir string) sessionState {
	st := sessionState{LastTurnEnd: turnFingerprint{TurnIndex: -1}}
	data, err := os.ReadFile(statePath(sessionDir))
	if err != nil {
		return st
	}
	var parsed sessionState
	if json.Unmarshal(data, &parsed) != nil {
		return st
	}
	return parsed
}

// saveState writes session state atomically.
func saveState(sessionDir string, st sessionState) error {
	if err := os.MkdirAll(sessionDir, 0o750); err != nil {
		return fmt.Errorf("create aider session dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal aider session state: %w", err)
	}
	tmp := statePath(sessionDir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write aider session state: %w", err)
	}
	if err := os.Rename(tmp, statePath(sessionDir)); err != nil {
		// Windows refuses a rename onto an open file; fall back to a direct
		// write rather than losing the update.
		_ = os.Remove(tmp)
		if writeErr := os.WriteFile(statePath(sessionDir), data, 0o600); writeErr != nil {
			return fmt.Errorf("write aider session state: %w", writeErr)
		}
	}
	return nil
}

// fingerprintOf summarizes the most recent turn. ok is false when the
// transcript holds no turns yet.
func fingerprintOf(turns []turn) (turnFingerprint, bool) {
	if len(turns) == 0 {
		return turnFingerprint{}, false
	}
	last := turns[len(turns)-1]
	return turnFingerprint{
		TurnIndex:   last.Index,
		ReportCount: last.Usage.Reports,
		EditCount:   len(last.ModifiedFiles),
	}, true
}
