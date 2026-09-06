package aider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/protocol"
)

func TestInfo(t *testing.T) {
	info := New().Info()

	if info.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", info.ProtocolVersion, protocol.ProtocolVersion)
	}
	if info.Name != "aider" {
		t.Errorf("Name = %q, want %q", info.Name, "aider")
	}
	if !info.Capabilities.Hooks {
		t.Error("hooks capability must be declared or Entire skips hook registration entirely")
	}
	if !info.Capabilities.TranscriptAnalyzer || !info.Capabilities.TokenCalculator {
		t.Error("transcript_analyzer and token_calculator are implemented and must be declared")
	}

	// Every declared hook name must be one ParseHook actually handles,
	// otherwise Entire routes a verb into a silent no-op.
	for _, name := range info.HookNames {
		ev, err := New().ParseHook(name, []byte("{}"))
		if err != nil {
			t.Errorf("declared hook %q errors: %v", name, err)
		}
		_ = ev
	}
}

// ProtectedDirs entries must name real DIRECTORIES, with no separators. A file
// or a trailing slash there makes `entire clean` abort its orphan-temp sweep
// for EVERY agent, not just this one.
func TestInfo_ProtectedDirsAreSafe(t *testing.T) {
	info := New().Info()

	if len(info.ProtectedDirs) == 0 {
		t.Fatal("no protected dirs declared; aider writes its repo-map cache into the working tree")
	}
	for _, d := range info.ProtectedDirs {
		if strings.ContainsAny(d, `/\`) {
			t.Errorf("ProtectedDirs entry %q contains a separator; matching is per-component", d)
		}
		if strings.HasSuffix(d, "/") || strings.HasSuffix(d, `\`) {
			t.Errorf("ProtectedDirs entry %q has a trailing separator", d)
		}
		if strings.HasSuffix(d, ".md") || strings.HasSuffix(d, ".yml") || strings.HasSuffix(d, ".history") {
			t.Errorf("ProtectedDirs entry %q looks like a FILE; files belong in ProtectedFiles", d)
		}
	}

	// The cache suffix is decided at import time by whether
	// tree-sitter-language-pack is installed (repomap.py:33-43), so two
	// installs of the same aider release can disagree. Both must be listed
	// because matching is exact.
	var v3, v4 bool
	for _, d := range info.ProtectedDirs {
		switch d {
		case ".aider.tags.cache.v3":
			v3 = true
		case ".aider.tags.cache.v4":
			v4 = true
		}
	}
	if !v3 || !v4 {
		t.Errorf("both tags-cache versions must be protected, got %v", info.ProtectedDirs)
	}
}

// Aider's DEFAULT history paths are declared even though install-hooks
// redirects them, so a bare `aider` run cannot push a chat log into a
// checkpoint.
func TestInfo_ProtectedFilesCoverAiderDefaults(t *testing.T) {
	info := New().Info()
	want := []string{".aider.chat.history.md", ".aider.input.history"}
	for _, w := range want {
		var found bool
		for _, f := range info.ProtectedFiles {
			if f == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ProtectedFiles is missing aider default %q; a bare aider run would pollute checkpoints", w)
		}
	}
	// Entries are compared with raw string equality in one consumer, so they
	// must be written exactly as git prints them.
	for _, f := range info.ProtectedFiles {
		if strings.Contains(f, `\`) || strings.HasPrefix(f, "./") {
			t.Errorf("ProtectedFiles entry %q must use git-style forward slashes with no ./ prefix", f)
		}
	}
}

func TestGetSessionDir(t *testing.T) {
	a := New()

	t.Run("explicit repo path", func(t *testing.T) {
		dir, err := a.GetSessionDir(filepath.FromSlash("/repo"))
		if err != nil {
			t.Fatalf("GetSessionDir: %v", err)
		}
		if !strings.HasSuffix(filepath.ToSlash(dir), sessionDirRel) {
			t.Errorf("GetSessionDir = %q, want it to end with %q", dir, sessionDirRel)
		}
	})

	t.Run("test override wins", func(t *testing.T) {
		t.Setenv("ENTIRE_TEST_AIDER_SESSION_DIR", "/override")
		dir, err := a.GetSessionDir("/repo")
		if err != nil {
			t.Fatalf("GetSessionDir: %v", err)
		}
		if dir != "/override" {
			t.Errorf("GetSessionDir = %q, want the override", dir)
		}
	})

	t.Run("falls back to ENTIRE_REPO_ROOT", func(t *testing.T) {
		repo := repoDir(t)
		dir, err := a.GetSessionDir("")
		if err != nil {
			t.Fatalf("GetSessionDir: %v", err)
		}
		if !strings.HasPrefix(dir, repo) {
			t.Errorf("GetSessionDir = %q, want it under %q", dir, repo)
		}
	})
}

// ResolveSessionFile builds a filesystem path. A hostile session id must never
// escape the session directory, whether or not the caller validated it.
func TestResolveSessionFile_StaysInsideSessionDir(t *testing.T) {
	a := New()
	base := filepath.FromSlash("/sessions")

	hostile := []string{
		"../../escape",
		"a/b",
		"..",
		".",
		"",
		"C:evil",
		`..\..\windows`,
	}
	for _, id := range hostile {
		got := a.ResolveSessionFile(base, id)
		clean := filepath.Clean(got)
		if !strings.HasPrefix(clean, filepath.Clean(base)) {
			t.Errorf("ResolveSessionFile(%q, %q) = %q, which escapes %q", base, id, clean, base)
		}
	}
}

func TestReadSession(t *testing.T) {
	repo := repoDir(t)
	a := New()

	t.Run("missing transcript is not an error", func(t *testing.T) {
		session, err := a.ReadSession(&protocol.HookInputJSON{})
		if err != nil {
			t.Fatalf("ReadSession: %v", err)
		}
		if len(session.NativeData) != 0 {
			t.Errorf("expected no native data, got %d bytes", len(session.NativeData))
		}
	})

	t.Run("populates native data and files", func(t *testing.T) {
		path := seedTranscript(t, repo, fixture(t))
		session, err := a.ReadSession(&protocol.HookInputJSON{SessionRef: path})
		if err != nil {
			t.Fatalf("ReadSession: %v", err)
		}
		if len(session.NativeData) == 0 {
			t.Error("NativeData is empty")
		}
		if len(session.ModifiedFiles) != 2 {
			t.Errorf("ModifiedFiles = %v, want 2 entries", session.ModifiedFiles)
		}
		if session.SessionID == "" {
			t.Error("session id was not derived from the banner")
		}
	})
}

// Bytes must survive a write/read cycle untouched: aider parses this file back
// in for --restore-chat-history, so a normalised copy would not round-trip.
func TestWriteSession_RoundTrip(t *testing.T) {
	repo := repoDir(t)
	a := New()
	data := fixture(t)

	ref := filepath.Join(repo, "nested", "dir", "chat.md")
	if err := a.WriteSession(protocol.AgentSessionJSON{SessionRef: ref, NativeData: data}); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	back, err := a.ReadTranscript(ref)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if string(back) != string(data) {
		t.Error("transcript did not round-trip byte-for-byte")
	}
}

func TestReadTranscript_MissingIsEmpty(t *testing.T) {
	repoDir(t)
	data, err := New().ReadTranscript(filepath.Join(t.TempDir(), "absent.md"))
	if err != nil {
		t.Fatalf("missing transcript should not error: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty, got %d bytes", len(data))
	}
}

func TestGetSessionID(t *testing.T) {
	repo := repoDir(t)
	a := New()

	// An explicit id in the payload wins.
	if got := a.GetSessionID(&protocol.HookInputJSON{SessionID: "explicit"}); got != "explicit" {
		t.Errorf("GetSessionID = %q, want %q", got, "explicit")
	}

	// Otherwise it comes from the transcript banner, because aider records no
	// session id and its notification passes no payload.
	seedTranscript(t, repo, fixture(t))
	if got := a.GetSessionID(&protocol.HookInputJSON{}); got == "" {
		t.Error("GetSessionID did not fall back to the banner")
	}

	if got := a.GetSessionID(nil); got == "" {
		t.Error("GetSessionID(nil) should still resolve from the transcript")
	}
}

func TestFormatResumeCommand(t *testing.T) {
	a := New()
	if got := a.FormatResumeCommand(""); !strings.Contains(got, "--restore-chat-history") {
		t.Errorf("FormatResumeCommand = %q, want it to use --restore-chat-history", got)
	}
	if got := a.FormatResumeCommand("20260906-112206"); !strings.Contains(got, "20260906-112206") {
		t.Errorf("FormatResumeCommand = %q, want it to mention the session", got)
	}
}

func TestDetect(t *testing.T) {
	// Detect just reports whether aider is on PATH; assert only that it answers
	// without panicking, since CI may or may not have aider installed.
	_ = New().Detect()
}

func TestTranscriptPathIsUnderRepoRoot(t *testing.T) {
	repo := repoDir(t)
	got := New().transcriptPath()
	if !strings.HasPrefix(got, repo) {
		t.Errorf("transcriptPath = %q, want it under %q", got, repo)
	}
	if _, err := os.Stat(filepath.Dir(got)); err == nil {
		t.Log("session dir already exists, fine")
	}
}
