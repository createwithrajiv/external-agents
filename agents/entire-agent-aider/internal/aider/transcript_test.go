package aider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture returns the real aider chat history captured from aider 0.86.2's own
// InputOutput writer. It is CRLF, as aider writes on Windows, and
// .gitattributes keeps it that way.
func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "chat_history.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Contains(data, []byte("\r\n")) {
		t.Fatal("fixture lost its CRLF line endings — .gitattributes should prevent normalisation")
	}
	return data
}

// writeTranscript drops content into a temp dir and returns its path.
func writeTranscript(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.md")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestParseTurns_Fixture(t *testing.T) {
	turns := parseTurns(fixture(t))

	// The fixture has FIVE "#### " lines but only FOUR turns: aider prefixes
	// every line of a multi-line prompt, so two adjacent prefixed lines are one
	// submission. This is the single easiest thing to get wrong.
	if got, want := len(turns), 4; got != want {
		t.Fatalf("len(turns) = %d, want %d (a multi-line prompt is ONE turn)", got, want)
	}

	tests := []struct {
		index   int
		prompt  string
		files   []string
		reports int
	}{
		{0, "add a Greet function to greeting.go that returns a greeting string", []string{"greeting.go"}, 1},
		{1, "now add a Farewell function too\nand make sure both are covered by tests", []string{"greeting.go", "greeting_test.go"}, 1},
		{2, "", nil, 0},
		{3, "what does Greet return for an empty name?", nil, 1},
	}
	for _, tc := range tests {
		got := turns[tc.index]
		if got.Prompt != tc.prompt {
			t.Errorf("turn %d prompt = %q, want %q", tc.index, got.Prompt, tc.prompt)
		}
		if len(got.ModifiedFiles) != len(tc.files) {
			t.Errorf("turn %d files = %v, want %v", tc.index, got.ModifiedFiles, tc.files)
			continue
		}
		for i, f := range tc.files {
			if got.ModifiedFiles[i] != f {
				t.Errorf("turn %d file[%d] = %q, want %q", tc.index, i, got.ModifiedFiles[i], f)
			}
		}
		if got.Usage.Reports != tc.reports {
			t.Errorf("turn %d reports = %d, want %d", tc.index, got.Usage.Reports, tc.reports)
		}
	}
}

// A blank submission is written by aider as "#### <blank>". The sentinel is
// aider's placeholder, not something the user typed, so it must not leak out.
func TestParseTurns_BlankPromptIsEmpty(t *testing.T) {
	turns := parseTurns(fixture(t))
	if turns[2].Prompt != "" {
		t.Errorf("blank turn prompt = %q, want empty (never the %q sentinel)", turns[2].Prompt, blankPromptSentinel)
	}
}

// Aider writes with line_endings="platform", so the same parser sees CRLF on
// Windows and LF elsewhere — and one file can hold both after a session moves
// between machines.
func TestParseTurns_LineEndingAgnostic(t *testing.T) {
	crlf := fixture(t)
	lf := bytes.ReplaceAll(crlf, []byte("\r\n"), []byte("\n"))

	a, b := parseTurns(crlf), parseTurns(lf)
	if len(a) != len(b) {
		t.Fatalf("CRLF gave %d turns, LF gave %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Prompt != b[i].Prompt {
			t.Errorf("turn %d prompt differs: CRLF %q vs LF %q", i, a[i].Prompt, b[i].Prompt)
		}
		if strings.Join(a[i].ModifiedFiles, ",") != strings.Join(b[i].ModifiedFiles, ",") {
			t.Errorf("turn %d files differ: %v vs %v", i, a[i].ModifiedFiles, b[i].ModifiedFiles)
		}
		if a[i].Usage != b[i].Usage {
			t.Errorf("turn %d usage differs: %+v vs %+v", i, a[i].Usage, b[i].Usage)
		}
	}
}

func TestGetTranscriptPosition(t *testing.T) {
	a := New()

	pos, err := a.GetTranscriptPosition(writeTranscript(t, fixture(t)))
	if err != nil {
		t.Fatalf("GetTranscriptPosition: %v", err)
	}
	if pos != 4 {
		t.Errorf("position = %d, want 4", pos)
	}

	// A transcript that does not exist yet is not an error: aider creates the
	// chat history lazily on its first append.
	pos, err = a.GetTranscriptPosition(filepath.Join(t.TempDir(), "absent.md"))
	if err != nil {
		t.Errorf("missing transcript should not error, got %v", err)
	}
	if pos != 0 {
		t.Errorf("missing transcript position = %d, want 0", pos)
	}
}

func TestExtractModifiedFiles(t *testing.T) {
	a := New()
	path := writeTranscript(t, fixture(t))

	tests := []struct {
		offset int
		want   []string
	}{
		{0, []string{"greeting.go", "greeting_test.go"}}, // deduplicated across turns
		{1, []string{"greeting.go", "greeting_test.go"}},
		{2, []string{}},
		{99, []string{}},
	}
	for _, tc := range tests {
		files, pos, err := a.ExtractModifiedFiles(path, tc.offset)
		if err != nil {
			t.Fatalf("offset %d: %v", tc.offset, err)
		}
		if pos != 4 {
			t.Errorf("offset %d: position = %d, want 4", tc.offset, pos)
		}
		if strings.Join(files, ",") != strings.Join(tc.want, ",") {
			t.Errorf("offset %d: files = %v, want %v", tc.offset, files, tc.want)
		}
	}
}

func TestExtractPrompts(t *testing.T) {
	a := New()
	path := writeTranscript(t, fixture(t))

	// Three, not four: the blank submission carries no text.
	prompts, err := a.ExtractPrompts(path, 0)
	if err != nil {
		t.Fatalf("ExtractPrompts: %v", err)
	}
	if len(prompts) != 3 {
		t.Fatalf("len(prompts) = %d, want 3 (blank submission omitted): %q", len(prompts), prompts)
	}
	if !strings.Contains(prompts[1], "\n") {
		t.Errorf("multi-line prompt should be rejoined with a newline, got %q", prompts[1])
	}

	prompts, err = a.ExtractPrompts(path, 3)
	if err != nil {
		t.Fatalf("ExtractPrompts(offset 3): %v", err)
	}
	if len(prompts) != 1 || prompts[0] != "what does Greet return for an empty name?" {
		t.Errorf("offset 3 prompts = %q", prompts)
	}
}

func TestCalculateTokens(t *testing.T) {
	a := New()
	data := fixture(t)

	// Turn 0: 2.4k sent, 187 received.
	// Turn 1: 15k sent, 1.1k cache write, 3.2k cache hit, 642 received.
	// Turn 3: 890 sent, 54 received.
	// Reports are independent, not cumulative — show_usage_report zeroes the
	// counters after emitting each line (base_coder.py:2124-2126) — so summing
	// is correct.
	usage, err := a.CalculateTokens(data, 0)
	if err != nil {
		t.Fatalf("CalculateTokens: %v", err)
	}
	if got, want := usage.APICallCount, 3; got != want {
		t.Errorf("APICallCount = %d, want %d", got, want)
	}
	if got, want := usage.OutputTokens, 187+642+54; got != want {
		t.Errorf("OutputTokens = %d, want %d", got, want)
	}
	if got, want := usage.CacheCreationTokens, 1100; got != want {
		t.Errorf("CacheCreationTokens = %d, want %d", got, want)
	}
	if got, want := usage.CacheReadTokens, 3200; got != want {
		t.Errorf("CacheReadTokens = %d, want %d", got, want)
	}
	// Aider's "sent" covers the whole prompt including the cached part, so
	// fresh input is what remains once the cache clauses come off.
	if got, want := usage.InputTokens, (2400+15000+890)-1100-3200; got != want {
		t.Errorf("InputTokens = %d, want %d", got, want)
	}

	// Offset past every usage line yields a zero value, not an error.
	usage, err = a.CalculateTokens(data, 99)
	if err != nil {
		t.Fatalf("CalculateTokens(offset 99): %v", err)
	}
	if usage.APICallCount != 0 || usage.InputTokens != 0 {
		t.Errorf("offset past end = %+v, want zero value", usage)
	}
}

// InputTokens is clamped at zero: the components are rounded independently, so
// on a heavily cached turn sent-minus-cache can legitimately go negative.
func TestCalculateTokens_ClampsNegativeInput(t *testing.T) {
	data := []byte("#### p\r\n> Tokens: 1.0k sent, 900 cache write, 900 cache hit, 10 received.\r\n")
	usage, err := New().CalculateTokens(data, 0)
	if err != nil {
		t.Fatalf("CalculateTokens: %v", err)
	}
	if usage.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0 (clamped)", usage.InputTokens)
	}
}

func TestParseTokenCount(t *testing.T) {
	// format_tokens (aider/utils.py:276) renders <1000 exactly, <10000 as one
	// decimal + "k", and everything else as a rounded "k". The value is lossy
	// above 999 and no unrounded copy exists on disk.
	tests := []struct {
		in   string
		want int
	}{
		{"890", 890},
		{"2.4k", 2400},
		{"15k", 15000},
		{"1.1k", 1100},
		{"0", 0},
		{"", 0},
		{"   ", 0},
		{"garbage", 0},
		{"k", 0},
	}
	for _, tc := range tests {
		if got := parseTokenCount(tc.in); got != tc.want {
			t.Errorf("parseTokenCount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSessionIDFromBanner(t *testing.T) {
	// Aider records no session ID. The startup banner is the only per-run
	// marker, and the ID must contain no ":" — Entire rejects the Windows
	// volume separator in a value used as a path component.
	id := sessionIDFromBanner(fixture(t))
	if id == "" {
		t.Fatal("no session id derived from the fixture banner")
	}
	if strings.ContainsAny(id, `:/\`) {
		t.Errorf("session id %q contains a path-unsafe character", id)
	}

	// The most recent banner wins when a file spans several aider runs.
	multi := []byte("# aider chat started at 2026-01-01 00:00:00\n\n" +
		"# aider chat started at 2026-02-03 04:05:06\n\n#### hi\n")
	if got, want := sessionIDFromBanner(multi), "20260203-040506"; got != want {
		t.Errorf("sessionIDFromBanner = %q, want %q (latest banner)", got, want)
	}

	if got := sessionIDFromBanner([]byte("no banner here")); got != "" {
		t.Errorf("sessionIDFromBanner without a banner = %q, want empty", got)
	}
}

func TestChunkReassemble_RoundTrip(t *testing.T) {
	a := New()
	data := fixture(t)

	// Reassembly is plain concatenation, so a split is lossless wherever it
	// lands — including inside a single oversized line, which is why very small
	// sizes are exercised here.
	for _, maxSize := range []int{16, 64, 200, 1000, len(data) - 1, len(data), len(data) + 1} {
		chunks, err := a.ChunkTranscript(data, maxSize)
		if err != nil {
			t.Fatalf("maxSize %d: %v", maxSize, err)
		}
		for i, c := range chunks {
			if len(c) > maxSize {
				t.Errorf("maxSize %d: chunk %d is %d bytes", maxSize, i, len(c))
			}
		}
		back, err := a.ReassembleTranscript(chunks)
		if err != nil {
			t.Fatalf("maxSize %d reassemble: %v", maxSize, err)
		}
		if !bytes.Equal(back, data) {
			t.Errorf("maxSize %d: round trip lost bytes (%d in, %d out)", maxSize, len(data), len(back))
		}
	}
}

func TestChunkTranscript_EdgeCases(t *testing.T) {
	a := New()

	chunks, err := a.ChunkTranscript(nil, 100)
	if err != nil {
		t.Fatalf("empty content: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("empty content gave %d chunks, want 0", len(chunks))
	}

	if _, err := a.ChunkTranscript([]byte("x"), 0); err == nil {
		t.Error("maxSize 0 should error")
	}

	// Content with no turns at all still round-trips.
	plain := []byte("just prose\nwith no prompts\n")
	chunks, err = a.ChunkTranscript(plain, 8)
	if err != nil {
		t.Fatalf("no-turn content: %v", err)
	}
	back, _ := a.ReassembleTranscript(chunks)
	if !bytes.Equal(back, plain) {
		t.Errorf("no-turn round trip mismatch: %q", back)
	}
}

func TestExtractSummary_ReportsNone(t *testing.T) {
	// Aider writes no session summary anywhere; inventing one from the
	// transcript would be a guess presented as a fact.
	summary, has, err := New().ExtractSummary("anything")
	if err != nil {
		t.Fatalf("ExtractSummary: %v", err)
	}
	if has || summary != "" {
		t.Errorf("ExtractSummary = (%q, %v), want (\"\", false)", summary, has)
	}
}
