package aider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/protocol"
)

// Managed-block markers inside .aider.conf.yml. Everything between them is
// ours to rewrite or remove; everything outside is the user's and is never
// touched.
const (
	markerBegin = "# >>> entire-agent-aider (managed) >>>"
	markerEnd   = "# <<< entire-agent-aider (managed) <<<"
)

// notificationCommand is what aider runs through a shell when a model round
// finishes.
//
// The `echo {}` is load-bearing, not decoration. Aider invokes this with
// subprocess.run(cmd, shell=True) and passes no stdin of its own (io.py:1094),
// but Entire's external-agent bridge reads the hook payload from stdin before
// it ever reaches this plugin and rejects an empty read with
// "parse-hook: read stdin: empty hook input". That surfaces to the user as
// "Failed to run notifications command: ..." on every single turn. Feeding it
// an empty JSON object satisfies the bridge; this plugin ignores the payload
// and derives everything from the transcript, because aider has nothing to put
// in it.
//
// `echo {} | ...` is valid in both POSIX sh and cmd.exe, and needs no path
// quoting, so it works wherever aider runs. The command names the `entire`
// binary resolved through PATH — never a path inside the working tree, since
// this file is commonly committed.
const notificationCommand = "echo {} | entire hooks aider " + HookNameTurnEnd

// managedBlock is the configuration install-hooks writes.
//
// Every key here is a normal aider long option. aider parses .aider.conf.yml
// with configargparse and includes the git-root copy in default_config_files
// (main.py:474-475), so a plain `aider` run in this repo picks all of it up
// with no wrapper and no shell involvement.
//
// `notifications: true` is not optional. IO.ring_bell guards on
// `self.bell_on_next_input and self.notifications` (io.py:1090), so setting
// only notifications-command is a silent no-op that captures nothing.
//
// The command names the `entire` binary resolved through PATH, never a path
// inside the working tree: this file is committed in many repos, and a
// repo-relative command would run whatever the checked-out branch contained on
// every aider turn.
func managedBlock() string {
	return strings.Join([]string{
		markerBegin,
		"# Managed by Entire. Edit above or below this block, not inside it.",
		"# Remove with: entire-agent-aider uninstall-hooks",
		"notifications: true",
		`notifications-command: "` + notificationCommand + `"`,
		"chat-history-file: " + transcriptRel,
		"llm-history-file: " + llmHistoryRel,
		"input-history-file: " + inputHistRel,
		markerEnd,
	}, "\n")
}

func (a *Agent) configPath() string {
	return filepath.Join(protocol.RepoRoot(), aiderConfigFile)
}

// InstallHooks writes the managed block into .aider.conf.yml.
//
// localDev is accepted and ignored: the protocol removed it because it asked
// agents to point hooks at a build inside the working tree.
func (a *Agent) InstallHooks(_ bool, force bool) (int, error) {
	path := a.configPath()
	block := managedBlock()

	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if err := os.WriteFile(path, []byte(block+"\n"), 0o600); err != nil {
			return 0, fmt.Errorf("write %s: %w", aiderConfigFile, err)
		}
		return 1, nil
	case err != nil:
		return 0, fmt.Errorf("read %s: %w", aiderConfigFile, err)
	}

	content := string(existing)
	if hasManagedBlock(content) {
		if !force && strings.Contains(content, block) {
			// Already current; nothing to rewrite.
			return 1, nil
		}
		content = replaceManagedBlock(content, block)
	} else {
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		content += block + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return 0, fmt.Errorf("write %s: %w", aiderConfigFile, err)
	}
	return 1, nil
}

// UninstallHooks removes the managed block, and the file if nothing else is
// left in it.
//
// Only this binary can remove its own hooks, so `entire disable --uninstall`
// cannot finish without it; a missing file is success, not an error.
func (a *Agent) UninstallHooks() error {
	path := a.configPath()
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", aiderConfigFile, err)
	}
	content := replaceManagedBlock(string(existing), "")
	if strings.TrimSpace(content) == "" {
		// The file existed only to hold our block.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", aiderConfigFile, err)
		}
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", aiderConfigFile, err)
	}
	return nil
}

// AreHooksInstalled reports whether the managed block is present.
func (a *Agent) AreHooksInstalled() bool {
	data, err := os.ReadFile(a.configPath())
	if err != nil {
		return false
	}
	return hasManagedBlock(string(data))
}

func hasManagedBlock(content string) bool {
	return strings.Contains(content, markerBegin) && strings.Contains(content, markerEnd)
}

// replaceManagedBlock swaps the managed region for replacement, dropping it
// entirely when replacement is empty. Content outside the markers is preserved
// byte-for-byte.
func replaceManagedBlock(content, replacement string) string {
	start := strings.Index(content, markerBegin)
	if start < 0 {
		return content
	}
	endIdx := strings.Index(content[start:], markerEnd)
	if endIdx < 0 {
		return content
	}
	end := start + endIdx + len(markerEnd)
	// Absorb one trailing newline so removal does not leave a blank line.
	if end < len(content) && content[end] == '\n' {
		end++
	}
	if replacement == "" {
		return content[:start] + content[end:]
	}
	return content[:start] + replacement + "\n" + content[end:]
}

// --- parse-hook -----------------------------------------------------------

// ParseHook translates an aider signal into a lifecycle event.
//
// turn-end is the only verb aider actually fires, and it arrives with an EMPTY
// payload: aider runs the notification command through a shell with no argv, no
// environment of its own and no stdin (io.py:1094). Everything is therefore
// derived from the transcript on disk.
func (a *Agent) ParseHook(hookName string, input []byte) (*protocol.EventJSON, error) {
	switch hookName {
	case HookNameTurnEnd:
		return a.parseTurnEnd(), nil
	case HookNameSessionStart:
		return a.sessionEvent(input, eventSessionStart), nil
	case HookNameSessionEnd:
		return a.sessionEvent(input, eventSessionEnd), nil
	default:
		return nil, nil
	}
}

// sessionEvent builds a session lifecycle event from whatever payload arrived.
func (a *Agent) sessionEvent(input []byte, eventType int) *protocol.EventJSON {
	ref := a.transcriptPath()
	var hookInput protocol.HookInputJSON
	if len(input) > 0 {
		_ = json.Unmarshal(input, &hookInput)
	}
	if hookInput.SessionRef != "" {
		ref = hookInput.SessionRef
	}
	sessionID := hookInput.SessionID
	if sessionID == "" {
		if data, err := os.ReadFile(ref); err == nil {
			sessionID = sessionIDOf(data)
		}
	}
	if sessionID == "" {
		return nil
	}
	return &protocol.EventJSON{
		Type:       eventType,
		SessionID:  sessionID,
		SessionRef: ref,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
}

// parseTurnEnd decides whether the transcript actually advanced, returning nil
// when it did not.
//
// This never returns an error, on purpose. aider reports a non-zero exit from
// the notification command to the user as
// "Failed to run notifications command: ..." (io.py:1097), so any error here
// would print a warning into the user's session on every affected turn. An
// uninterpretable turn-end is reported as "nothing happened", which is what the
// protocol's null response already means.
func (a *Agent) parseTurnEnd() *protocol.EventJSON {
	ref := a.transcriptPath()
	data, err := os.ReadFile(ref)
	if err != nil {
		return nil
	}
	turns := parseTurns(data)
	fp, ok := fingerprintOf(turns)
	if !ok {
		// The bell can ring before the user has submitted anything.
		return nil
	}

	// Require evidence that a model round completed. A confirmation prompt
	// raised before any response — "Add foo.go to the chat?" during file
	// mention detection — rings the bell with neither a usage report nor an
	// applied edit recorded, and must not produce a checkpoint.
	if fp.ReportCount == 0 && fp.EditCount == 0 {
		return nil
	}

	sessionDir := filepath.Dir(ref)
	st := loadState(sessionDir)
	if !fp.advancedOver(st.LastTurnEnd) {
		return nil
	}

	sessionID := sessionIDOf(data)
	if sessionID == "" {
		return nil
	}

	st.LastTurnEnd = fp
	st.SessionID = sessionID
	// A failure to persist costs at most one redundant checkpoint next time;
	// failing the hook would cost the user a warning every turn.
	_ = saveState(sessionDir, st)

	return &protocol.EventJSON{
		Type:       eventTurnEnd,
		SessionID:  sessionID,
		SessionRef: ref,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
}
