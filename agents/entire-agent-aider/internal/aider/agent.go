package aider

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/protocol"
)

// Agent implements the Entire external agent protocol for Aider.
type Agent struct{}

// New creates an agent instance.
func New() *Agent { return &Agent{} }

// Info describes the agent to Entire.
func (a *Agent) Info() protocol.InfoResponse {
	return protocol.InfoResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            "aider",
		Type:            "Aider",
		Description:     "Aider - AI pair programming in your terminal",
		IsPreview:       true,
		// Aider is unusual in writing its own state into the working tree
		// rather than under the user's home directory. The repo-map cache is
		// rewritten constantly, so without this every turn would report it as
		// a change. Entries must name real DIRECTORIES only — a file listed
		// here breaks `entire clean` for every agent, not just this one.
		//
		// BOTH cache versions are listed because the suffix is decided at
		// import time by whether tree-sitter-language-pack is installed:
		// repomap.py:33-43 sets CACHE_VERSION = 3, then 4 if USING_TSL_PACK,
		// and names the directory ".aider.tags.cache.v{CACHE_VERSION}". Two
		// installs of the same aider release can therefore disagree, and
		// matching is exact per path component, so a prefix will not do. A
		// listed directory that never exists is harmless.
		ProtectedDirs: []string{
			".aider.tags.cache.v3",
			".aider.tags.cache.v4",
		},
		// Aider's DEFAULT history paths, declared even though install-hooks
		// redirects them, so a user who runs aider with its stock config — or
		// with --no-... overrides — still cannot push a chat log into a
		// checkpoint.
		ProtectedFiles: []string{
			".aider.chat.history.md",
			".aider.input.history",
			".aider.llm.history",
			aiderConfigFile,
		},
		HookNames: []string{
			HookNameTurnEnd,
			HookNameSessionStart,
			HookNameSessionEnd,
		},
		Capabilities: protocol.DeclaredCapabilities{
			Hooks:              true,
			TranscriptAnalyzer: true,
			TokenCalculator:    true,
			UsesTerminal:       true,
		},
	}
}

// Detect reports whether aider is available on PATH.
func (a *Agent) Detect() protocol.DetectResponse {
	_, err := exec.LookPath("aider")
	return protocol.DetectResponse{Present: err == nil}
}

// GetSessionID returns the session identifier for a hook payload.
//
// Aider records no session ID of any kind and its notification command passes
// no payload, so the incoming HookInput is normally empty. The ID is derived
// instead from the most recent run banner in the transcript, which is the only
// per-run marker aider writes.
func (a *Agent) GetSessionID(input *protocol.HookInputJSON) string {
	if input != nil && input.SessionID != "" {
		return input.SessionID
	}
	data, err := os.ReadFile(a.transcriptPath())
	if err != nil {
		return ""
	}
	return sessionIDFromBanner(data)
}

// GetSessionDir returns the directory holding aider session state.
func (a *Agent) GetSessionDir(repoPath string) (string, error) {
	if override := os.Getenv("ENTIRE_TEST_AIDER_SESSION_DIR"); override != "" {
		return override, nil
	}
	if repoPath == "" {
		repoPath = protocol.RepoRoot()
	}
	if repoPath == "" {
		return "", errors.New("repo path is required to resolve the aider session directory")
	}
	return filepath.Join(repoPath, filepath.FromSlash(sessionDirRel)), nil
}

// ResolveSessionFile returns the transcript path for a session.
//
// The session ID is deliberately NOT used as a path component. Aider is
// configured through a static YAML file with one chat-history-file setting, so
// every run of a repo appends to the same transcript and runs are separated by
// the banner inside it rather than by separate files. Accepting the ID as a
// directory name would invent a layout aider never writes to.
func (a *Agent) ResolveSessionFile(sessionDir, _ string) string {
	if sessionDir == "" {
		return a.transcriptPath()
	}
	return filepath.Join(sessionDir, "chat.md")
}

// transcriptPath returns the repo-root-anchored transcript path.
func (a *Agent) transcriptPath() string {
	return filepath.Join(protocol.RepoRoot(), filepath.FromSlash(transcriptRel))
}

// ReadTranscript reads the raw chat-history bytes.
//
// Bytes are returned exactly as aider wrote them, CRLF and all. Native format
// preservation is a protocol rule, and it matters concretely here:
// --restore-chat-history parses this file back in (base_coder.py:520), so a
// normalized copy would not round-trip.
func (a *Agent) ReadTranscript(sessionRef string) ([]byte, error) {
	if sessionRef == "" {
		sessionRef = a.transcriptPath()
	}
	data, err := os.ReadFile(sessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte{}, nil
		}
		return nil, fmt.Errorf("read aider transcript: %w", err)
	}
	return data, nil
}

// ReadSession reads session data from aider's storage.
func (a *Agent) ReadSession(input *protocol.HookInputJSON) (protocol.AgentSessionJSON, error) {
	ref := ""
	if input != nil {
		ref = input.SessionRef
	}
	if ref == "" {
		ref = a.transcriptPath()
	}
	session := protocol.AgentSessionJSON{
		AgentName:  "aider",
		RepoPath:   protocol.RepoRoot(),
		SessionRef: ref,
		StartTime:  time.Now().UTC().Format(time.RFC3339),
	}
	if input != nil {
		session.SessionID = input.SessionID
	}

	data, err := os.ReadFile(ref)
	if err != nil {
		if os.IsNotExist(err) {
			// Aider creates the chat history lazily on its first append, so an
			// absent file is a normal early-session state, not a failure.
			return session, nil
		}
		return session, fmt.Errorf("read aider transcript: %w", err)
	}
	session.NativeData = data
	if session.SessionID == "" {
		session.SessionID = sessionIDFromBanner(data)
	}
	for _, t := range parseTurns(data) {
		for _, f := range t.ModifiedFiles {
			if !contains(session.ModifiedFiles, f) {
				session.ModifiedFiles = append(session.ModifiedFiles, f)
			}
		}
	}
	return session, nil
}

// WriteSession writes a transcript back so aider can resume from it.
func (a *Agent) WriteSession(session protocol.AgentSessionJSON) error {
	ref := session.SessionRef
	if ref == "" {
		ref = a.transcriptPath()
	}
	if err := os.MkdirAll(filepath.Dir(ref), 0o750); err != nil {
		return fmt.Errorf("create aider session dir: %w", err)
	}
	if err := os.WriteFile(ref, session.NativeData, 0o600); err != nil {
		return fmt.Errorf("write aider transcript: %w", err)
	}
	return nil
}

// FormatResumeCommand returns the command that resumes a session.
//
// --restore-chat-history makes aider read the transcript back in and rebuild
// the conversation. The session ID is not passed: aider has no flag that
// selects a run, so resuming replays whatever the configured chat-history-file
// currently holds.
func (a *Agent) FormatResumeCommand(sessionID string) string {
	cmd := "aider --restore-chat-history"
	if strings.TrimSpace(sessionID) == "" {
		return cmd
	}
	return cmd + "  # session " + sessionID
}
