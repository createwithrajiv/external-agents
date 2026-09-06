package aider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoDir points ENTIRE_REPO_ROOT at a fresh temp directory, which is how the
// CLI tells a plugin which repository it is acting on.
func repoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ENTIRE_REPO_ROOT", dir)
	return dir
}

// seedTranscript writes content to the repo's configured transcript path.
func seedTranscript(t *testing.T, repo string, content []byte) string {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(transcriptRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestInstallHooks_FreshRepo(t *testing.T) {
	repo := repoDir(t)
	a := New()

	if a.AreHooksInstalled() {
		t.Error("hooks reported installed in a fresh repo")
	}

	n, err := a.InstallHooks(false, false)
	if err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if n != 1 {
		t.Errorf("InstallHooks returned %d, want 1", n)
	}
	if !a.AreHooksInstalled() {
		t.Error("hooks not reported installed after install")
	}

	data, err := os.ReadFile(filepath.Join(repo, aiderConfigFile))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)

	// notifications: true is not optional. IO.ring_bell guards on
	// `self.bell_on_next_input and self.notifications` (io.py:1090), so the
	// command alone is a silent no-op that captures nothing.
	if !strings.Contains(content, "notifications: true") {
		t.Error("config is missing `notifications: true` — the command alone is a silent no-op")
	}
	if !strings.Contains(content, notificationCommand) {
		t.Errorf("config is missing the notification command:\n%s", content)
	}
	// The echo is load-bearing: Entire's bridge rejects an empty stdin read
	// before the plugin runs, which aider would show the user every turn.
	if !strings.HasPrefix(notificationCommand, "echo {}") {
		t.Error("notification command must feed stdin; Entire's bridge rejects an empty read")
	}
	// Hook commands must name the entire binary via PATH, never a path inside
	// the working tree — this file is commonly committed.
	if strings.Contains(content, "..") || strings.Contains(content, "./entire") {
		t.Errorf("notification command must not reference a path in the working tree:\n%s", content)
	}
	for _, rel := range []string{transcriptRel, llmHistoryRel, inputHistRel} {
		if !strings.Contains(content, rel) {
			t.Errorf("config is missing redirect for %s", rel)
		}
	}
}

func TestInstallHooks_Idempotent(t *testing.T) {
	repo := repoDir(t)
	a := New()

	for i := 0; i < 3; i++ {
		if _, err := a.InstallHooks(false, false); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(repo, aiderConfigFile))
	if got := strings.Count(string(data), markerBegin); got != 1 {
		t.Errorf("found %d managed blocks after 3 installs, want 1", got)
	}
}

func TestInstallHooks_PreservesUserConfig(t *testing.T) {
	repo := repoDir(t)
	a := New()

	userConfig := "model: gpt-4o\nauto-commits: true\n"
	path := filepath.Join(repo, aiderConfigFile)
	if err := os.WriteFile(path, []byte(userConfig), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, err := a.InstallHooks(false, false); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "model: gpt-4o") {
		t.Error("install destroyed the user's existing settings")
	}
	if !strings.Contains(string(data), markerBegin) {
		t.Error("managed block not appended")
	}

	// Uninstall must give the file back exactly as it was.
	if err := a.UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config was removed even though the user had their own settings in it: %v", err)
	}
	if string(data) != userConfig {
		t.Errorf("uninstall did not restore the user's config exactly:\ngot  %q\nwant %q", data, userConfig)
	}
}

func TestUninstallHooks_RemovesOwnFile(t *testing.T) {
	repo := repoDir(t)
	a := New()

	if _, err := a.InstallHooks(false, false); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if err := a.UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	// The file held nothing but our block, so it should be gone entirely.
	if _, err := os.Stat(filepath.Join(repo, aiderConfigFile)); !os.IsNotExist(err) {
		t.Error("config file should be removed when it contained only our block")
	}
	if a.AreHooksInstalled() {
		t.Error("hooks still reported installed after uninstall")
	}
}

// entire disable --uninstall cannot finish without the plugin, so a missing
// file is success rather than an error.
func TestUninstallHooks_MissingFileIsNotAnError(t *testing.T) {
	repoDir(t)
	if err := New().UninstallHooks(); err != nil {
		t.Errorf("uninstall with no config should succeed, got %v", err)
	}
}

func TestParseHook_UnknownVerb(t *testing.T) {
	repoDir(t)
	ev, err := New().ParseHook("bogus", nil)
	if err != nil {
		t.Fatalf("unknown verb errored: %v", err)
	}
	if ev != nil {
		t.Errorf("unknown verb returned an event: %+v", ev)
	}
}

// parse-hook must NEVER return an error for turn-end. Aider reports a non-zero
// exit to the user as "Failed to run notifications command: ..." (io.py:1097),
// so an error would print a warning into the session on every affected turn.
func TestParseHook_TurnEndNeverErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string)
		input []byte
	}{
		{"no transcript at all", func(*testing.T, string) {}, nil},
		{"empty payload", func(t *testing.T, r string) { seedTranscript(t, r, fixture(t)) }, nil},
		{"malformed json payload", func(t *testing.T, r string) { seedTranscript(t, r, fixture(t)) }, []byte("{not json")},
		{"empty transcript", func(t *testing.T, r string) { seedTranscript(t, r, nil) }, []byte("{}")},
		{"transcript with no turns", func(t *testing.T, r string) {
			seedTranscript(t, r, []byte("# aider chat started at 2026-01-01 00:00:00\n"))
		}, []byte("{}")},
		{"garbage transcript", func(t *testing.T, r string) {
			seedTranscript(t, r, []byte("\x00\xff binary nonsense"))
		}, []byte("{}")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := repoDir(t)
			tc.setup(t, repo)
			if _, err := New().ParseHook(HookNameTurnEnd, tc.input); err != nil {
				t.Errorf("turn-end returned an error (%v); it must always succeed", err)
			}
		})
	}
}

func TestParseHook_TurnEndDedup(t *testing.T) {
	repo := repoDir(t)
	seedTranscript(t, repo, fixture(t))
	a := New()

	first, err := a.ParseHook(HookNameTurnEnd, []byte("{}"))
	if err != nil {
		t.Fatalf("first turn-end: %v", err)
	}
	if first == nil {
		t.Fatal("first turn-end produced no event")
	}
	if first.Type != eventTurnEnd {
		t.Errorf("event type = %d, want %d", first.Type, eventTurnEnd)
	}
	if first.SessionID == "" {
		t.Error("event carries no session id")
	}

	// A second firing that observes nothing new must be suppressed. Aider's
	// bell is not a turn-end signal: it also rings from confirm_ask and
	// prompt_ask, so duplicates are the normal case, not an edge case.
	second, err := a.ParseHook(HookNameTurnEnd, []byte("{}"))
	if err != nil {
		t.Fatalf("second turn-end: %v", err)
	}
	if second != nil {
		t.Errorf("duplicate turn-end produced an event: %+v", second)
	}

	// Real progress resumes reporting.
	path := filepath.Join(repo, filepath.FromSlash(transcriptRel))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	_, _ = f.WriteString("\r\n#### another prompt  \r\n\r\nreply\r\n\r\n> Applied edit to new.go  \r\n> Tokens: 100 sent, 20 received.  \r\n")
	_ = f.Close()

	third, err := a.ParseHook(HookNameTurnEnd, []byte("{}"))
	if err != nil {
		t.Fatalf("third turn-end: %v", err)
	}
	if third == nil {
		t.Error("turn-end after real progress produced no event")
	}
}

// A confirmation prompt raised before any model response rings the bell with
// neither a usage report nor an applied edit recorded. That must not create a
// checkpoint.
func TestParseHook_TurnEndIgnoresTurnWithNoWork(t *testing.T) {
	repo := repoDir(t)
	seedTranscript(t, repo, []byte(
		"# aider chat started at 2026-01-01 00:00:00\r\n\r\n"+
			"#### add a thing  \r\n"+
			"> Add foo.go to the chat? (Y)es/(N)o [Yes]: y  \r\n"))

	ev, err := New().ParseHook(HookNameTurnEnd, []byte("{}"))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if ev != nil {
		t.Errorf("a turn with no usage report and no applied edit produced an event: %+v", ev)
	}
}

// The same turn index with an ADDITIONAL applied edit is real progress, which
// is why the fingerprint carries more than the turn index.
func TestParseHook_TurnEndFiresAgainOnMoreEditsInSameTurn(t *testing.T) {
	repo := repoDir(t)
	path := seedTranscript(t, repo, []byte(
		"# aider chat started at 2026-01-01 00:00:00\r\n\r\n"+
			"#### do it  \r\n"+
			"> Tokens: 100 sent, 20 received.  \r\n"+
			"> Applied edit to a.go  \r\n"))
	a := New()

	if ev, _ := a.ParseHook(HookNameTurnEnd, []byte("{}")); ev == nil {
		t.Fatal("first turn-end produced no event")
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("> Applied edit to b.go  \r\n")
	_ = f.Close()

	ev, err := a.ParseHook(HookNameTurnEnd, []byte("{}"))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if ev == nil {
		t.Error("an additional applied edit in the same turn should count as progress")
	}
}

func TestParseHook_SessionEvents(t *testing.T) {
	repo := repoDir(t)
	seedTranscript(t, repo, fixture(t))
	a := New()

	for _, tc := range []struct {
		verb string
		want int
	}{
		{HookNameSessionStart, eventSessionStart},
		{HookNameSessionEnd, eventSessionEnd},
	} {
		ev, err := a.ParseHook(tc.verb, []byte("{}"))
		if err != nil {
			t.Fatalf("%s: %v", tc.verb, err)
		}
		if ev == nil {
			t.Fatalf("%s produced no event", tc.verb)
		}
		if ev.Type != tc.want {
			t.Errorf("%s type = %d, want %d", tc.verb, ev.Type, tc.want)
		}
		if ev.SessionID == "" {
			t.Errorf("%s carries no session id", tc.verb)
		}
	}
}

func TestReplaceManagedBlock(t *testing.T) {
	block := markerBegin + "\nx: 1\n" + markerEnd

	t.Run("removal leaves surrounding content intact", func(t *testing.T) {
		content := "before: 1\n" + block + "\nafter: 2\n"
		got := replaceManagedBlock(content, "")
		want := "before: 1\nafter: 2\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no markers is a no-op", func(t *testing.T) {
		content := "model: gpt-4o\n"
		if got := replaceManagedBlock(content, ""); got != content {
			t.Errorf("got %q, want unchanged", got)
		}
	})

	t.Run("replacement swaps only the block", func(t *testing.T) {
		content := "a: 1\n" + block + "\nb: 2\n"
		got := replaceManagedBlock(content, "NEW")
		if !strings.Contains(got, "a: 1") || !strings.Contains(got, "b: 2") || !strings.Contains(got, "NEW") {
			t.Errorf("unexpected result %q", got)
		}
		if strings.Contains(got, "x: 1") {
			t.Errorf("old block survived: %q", got)
		}
	})
}
