package aider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/protocol"
)

// jsonlFixture returns the JSONL transcript emitted by aider's new structured
// writer. Line endings are LF and .gitattributes marks testdata as -text, so
// the bytes on disk are the bytes the parser sees.
func jsonlFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatalf("read jsonl fixture: %v", err)
	}
	return data
}

// truncateBefore cuts data partway through the line containing marker, leaving
// a final line that is not valid JSON — what a transcript looks like when the
// process writing it dies mid-append.
func truncateBefore(t *testing.T, data []byte, marker string) []byte {
	t.Helper()
	i := bytes.Index(data, []byte(marker))
	if i < 0 {
		t.Fatalf("marker %q not in fixture", marker)
	}
	cut := data[:i+len(marker)/2]
	if bytes.HasSuffix(cut, []byte("\n")) {
		t.Fatalf("truncation at %q landed on a line boundary; the point is a PARTIAL line", marker)
	}
	return cut
}

// --- detection ------------------------------------------------------------

// Detection is by content, never by filename or config key. Both formats
// arrive at the same .entire/aider/chat.md path, because chat-history-file is
// unchanged and existing users must be unaffected.
func TestDetectFormat_ByContentNotFilename(t *testing.T) {
	if got := detectFormat(fixture(t)); got.Name() != formatMarkdown {
		t.Errorf("markdown fixture detected as %q, want %q", got.Name(), formatMarkdown)
	}
	// The same ".md" path, holding JSONL.
	if got := detectFormat(jsonlFixture(t)); got.Name() != formatJSONL {
		t.Errorf("jsonl fixture detected as %q, want %q", got.Name(), formatJSONL)
	}
}

func TestDetectFormat_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		// An absent or not-yet-written transcript keeps the historical
		// behaviour: markdown, zero turns, no error.
		{"empty", nil, formatMarkdown},
		{"blank lines only", []byte("\n\n  \n"), formatMarkdown},
		{"leading blank line then json", []byte("\n\n{\"event\":\"session_started\"}\n"), formatJSONL},
		{"markdown banner", []byte("# aider chat started at 2026-01-01 00:00:00\n"), formatMarkdown},
		{"prompt line holding a brace", []byte("#### fix {\"a\":1}\n"), formatMarkdown},
		{"binary garbage", []byte("\x00\xff nonsense"), formatMarkdown},
		// A first line truncated mid-object is still JSONL: the leading brace
		// is the signal, so detection survives truncation anywhere.
		{"truncated first line", []byte(`{"event":"session_star`), formatJSONL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectFormat(tc.data).Name(); got != tc.want {
				t.Errorf("detectFormat = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- group 1: the original markdown format is untouched --------------------

// The seam must not change a single observable thing about markdown parsing.
// This pins the whole markdown result set through the dispatching entry point.
func TestParseTurns_MarkdownUnchangedByDispatch(t *testing.T) {
	data := fixture(t)
	turns := parseTurns(data)

	if got, want := len(turns), 4; got != want {
		t.Fatalf("len(turns) = %d, want %d", got, want)
	}
	if turns[1].Prompt != "now add a Farewell function too\nand make sure both are covered by tests" {
		t.Errorf("multi-line prompt = %q", turns[1].Prompt)
	}
	if got, want := strings.Join(turns[1].ModifiedFiles, ","), "greeting.go,greeting_test.go"; got != want {
		t.Errorf("turn 1 files = %q, want %q", got, want)
	}
	if got, want := sessionIDOf(data), "20260906-102235"; got != want {
		t.Errorf("sessionIDOf = %q, want %q", got, want)
	}
	// Markdown carries no summary, and inventing one would be a guess.
	if _, ok := detectFormat(data).Summary(data); ok {
		t.Error("markdown reported a summary")
	}
	// Markdown stays splittable mid-line: it has no per-line structure to
	// corrupt, and dropping a session over one very long line is worse.
	if !detectFormat(data).SplittableMidLine() {
		t.Error("markdown should remain splittable mid-line")
	}
}

// --- group 2: the new JSONL format -----------------------------------------

func TestParseTurns_JSONLFixture(t *testing.T) {
	turns := parseTurns(jsonlFixture(t))

	if got, want := len(turns), 1; got != want {
		t.Fatalf("len(turns) = %d, want %d", got, want)
	}
	got := turns[0]

	const wantPrompt = "Add coupon validation to checkout. Coupons should be rejected if expired, " +
		"disabled, or below the minimum cart value. Add tests."
	if got.Prompt != wantPrompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, wantPrompt)
	}
	if got.Index != 0 {
		t.Errorf("index = %d, want 0", got.Index)
	}

	// Three file_changed events name only two distinct paths: apply_coupon.ts
	// is modified twice, once for the validation and once for the reordering.
	wantFiles := "src/checkout/apply_coupon.ts,tests/checkout/apply_coupon.test.ts"
	if joined := strings.Join(got.ModifiedFiles, ","); joined != wantFiles {
		t.Errorf("files = %q, want %q (deduplicated, in first-seen order)", joined, wantFiles)
	}

	// Unlike markdown's rounded "15k", JSONL reports exact integers.
	if got.Usage.Reports != 1 {
		t.Errorf("reports = %d, want 1", got.Usage.Reports)
	}
	if got.Usage.Sent != 8421 {
		t.Errorf("sent = %d, want 8421 (exact, not rounded)", got.Usage.Sent)
	}
	if got.Usage.Received != 2194 {
		t.Errorf("received = %d, want 2194", got.Usage.Received)
	}
}

// Events before the first user_prompt — session_started and anything else the
// agent emits at startup — belong to no turn, exactly as the markdown banner
// and startup notices do.
func TestParseTurns_JSONLPreambleBelongsToNoTurn(t *testing.T) {
	data := []byte(`{"event":"session_started","session_id":"s1"}
{"event":"file_changed","path":"stray.go"}
{"event":"usage","input_tokens":10,"output_tokens":5}
{"event":"user_prompt","text":"go"}
{"event":"file_changed","path":"real.go"}
`)
	turns := parseTurns(data)
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1", len(turns))
	}
	if joined := strings.Join(turns[0].ModifiedFiles, ","); joined != "real.go" {
		t.Errorf("files = %q, want %q (preamble edits are not attributable to a turn)", joined, "real.go")
	}
	if turns[0].Usage.Reports != 0 {
		t.Errorf("reports = %d, want 0", turns[0].Usage.Reports)
	}
}

// Each user_prompt opens its own turn. Markdown needs run-collapsing because
// aider prefixes every line of a multi-line submission; JSONL carries the whole
// submission in one event, so there is no run to collapse.
func TestParseTurns_JSONLEachPromptIsOneTurn(t *testing.T) {
	data := []byte(`{"event":"user_prompt","text":"first\nsecond line of the SAME submission"}
{"event":"usage","input_tokens":1,"output_tokens":1}
{"event":"user_prompt","text":"a genuinely new turn"}
`)
	turns := parseTurns(data)
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if !strings.Contains(turns[0].Prompt, "\n") {
		t.Errorf("embedded newline lost: %q", turns[0].Prompt)
	}
	if turns[1].Index != 1 {
		t.Errorf("second turn index = %d, want 1", turns[1].Index)
	}
}

func TestSessionIDOf_JSONL(t *testing.T) {
	if got, want := sessionIDOf(jsonlFixture(t)), "btw-track3-demo-001"; got != want {
		t.Errorf("sessionIDOf = %q, want %q", got, want)
	}
	// The most recent run wins when one file holds several, matching the
	// markdown rule that the latest banner wins.
	multi := []byte(`{"event":"session_started","session_id":"older"}
{"event":"session_ended","session_id":"older"}
{"event":"session_started","session_id":"newer"}
`)
	if got, want := sessionIDOf(multi), "newer"; got != want {
		t.Errorf("sessionIDOf(multi) = %q, want %q", got, want)
	}
	if got := sessionIDOf([]byte(`{"event":"user_prompt","text":"no id here"}` + "\n")); got != "" {
		t.Errorf("sessionIDOf without an id = %q, want empty", got)
	}
}

// Entire uses the session ID as a path component and rejects the Windows
// volume separator, so a JSONL id gets the same treatment the banner-derived
// one already gets.
func TestSanitizeSessionID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"btw-track3-demo-001", "btw-track3-demo-001"},
		{"plain_id.v2", "plain_id.v2"},
		{"C:/work/sess", "C-work-sess"},
		{`a\b`, "a-b"},
		{"a:b", "a-b"},
		{"has space", "has-space"},
		// A traversal component must never survive as a directory name.
		{"..", ""},
		{".", ""},
		{"/", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := sanitizeSessionID(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeSessionID(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.ContainsAny(got, `:/\`) {
			t.Errorf("sanitizeSessionID(%q) = %q, which is path-unsafe", tc.in, got)
		}
	}
}

func TestSessionIDOf_JSONLSanitises(t *testing.T) {
	data := []byte(`{"event":"session_started","session_id":"C:/runs/2026-09-06"}` + "\n")
	got := sessionIDOf(data)
	if strings.ContainsAny(got, `:/\`) {
		t.Errorf("sessionIDOf = %q, which is path-unsafe", got)
	}
	if got == "" {
		t.Error("a sanitisable id was discarded entirely; capture would be lost")
	}
}

// --- group 2: the analyzer surface over JSONL ------------------------------

func TestTranscriptAnalyzer_OverJSONL(t *testing.T) {
	a := New()
	// Deliberately written to chat.md: the path is unchanged, only the content
	// differs, and that is the whole point of content detection.
	path := writeTranscript(t, jsonlFixture(t))

	pos, err := a.GetTranscriptPosition(path)
	if err != nil {
		t.Fatalf("GetTranscriptPosition: %v", err)
	}
	if pos != 1 {
		t.Errorf("position = %d, want 1", pos)
	}

	files, pos, err := a.ExtractModifiedFiles(path, 0)
	if err != nil {
		t.Fatalf("ExtractModifiedFiles: %v", err)
	}
	want := "src/checkout/apply_coupon.ts,tests/checkout/apply_coupon.test.ts"
	if joined := strings.Join(files, ","); joined != want {
		t.Errorf("files = %q, want %q", joined, want)
	}
	if pos != 1 {
		t.Errorf("position = %d, want 1", pos)
	}

	prompts, err := a.ExtractPrompts(path, 0)
	if err != nil {
		t.Fatalf("ExtractPrompts: %v", err)
	}
	if len(prompts) != 1 || !strings.HasPrefix(prompts[0], "Add coupon validation") {
		t.Errorf("prompts = %q", prompts)
	}
}

func TestCalculateTokens_JSONLIsExact(t *testing.T) {
	usage, err := New().CalculateTokens(jsonlFixture(t), 0)
	if err != nil {
		t.Fatalf("CalculateTokens: %v", err)
	}
	if usage.APICallCount != 1 {
		t.Errorf("APICallCount = %d, want 1", usage.APICallCount)
	}
	// No rounding: markdown would have rendered 8421 as "8.4k".
	if usage.InputTokens != 8421 {
		t.Errorf("InputTokens = %d, want 8421", usage.InputTokens)
	}
	if usage.OutputTokens != 2194 {
		t.Errorf("OutputTokens = %d, want 2194", usage.OutputTokens)
	}
	if usage.CacheCreationTokens != 0 || usage.CacheReadTokens != 0 {
		t.Errorf("cache tokens = %d/%d, want 0/0 (the format reports none)",
			usage.CacheCreationTokens, usage.CacheReadTokens)
	}
}

// --- group 2: ExtractSummary ------------------------------------------------

func TestExtractSummary_JSONLFromCheckpointCreated(t *testing.T) {
	path := writeTranscript(t, jsonlFixture(t))
	summary, has, err := New().ExtractSummary(path)
	if err != nil {
		t.Fatalf("ExtractSummary: %v", err)
	}
	if !has {
		t.Fatal("no summary reported for a transcript containing checkpoint_created")
	}
	for _, want := range []string{
		"Coupon validation implemented and tested.",
		"Reject expired, disabled, or minimum-cart-value-ineligible coupons during checkout.",
		"Should expiry or disabled state take precedence in user-facing errors?",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestExtractSummary_JSONLWithoutCheckpointReportsNone(t *testing.T) {
	path := writeTranscript(t, []byte(`{"event":"user_prompt","text":"hi"}`+"\n"))
	summary, has, err := New().ExtractSummary(path)
	if err != nil {
		t.Fatalf("ExtractSummary: %v", err)
	}
	if has || summary != "" {
		t.Errorf("ExtractSummary = (%q, %v), want (\"\", false)", summary, has)
	}
}

// A missing transcript is a normal early-session state, not a failure.
func TestExtractSummary_MissingFileIsNotAnError(t *testing.T) {
	summary, has, err := New().ExtractSummary(filepath.Join(t.TempDir(), "absent.md"))
	if err != nil {
		t.Fatalf("ExtractSummary: %v", err)
	}
	if has || summary != "" {
		t.Errorf("ExtractSummary = (%q, %v), want (\"\", false)", summary, has)
	}
}

// --- group 3: unknown events ------------------------------------------------

// The format will grow event types this build has never heard of. An unknown
// event, and an unknown field on a known event, must both be inert — never a
// crash, never a lost session.
func TestParseTurns_JSONLUnknownEventsAreInert(t *testing.T) {
	data := []byte(`{"event":"session_started","session_id":"s1"}
{"event":"telemetry_flushed","payload":{"nested":{"deep":[1,2,3]}}}
{"event":"user_prompt","text":"do the thing","unknown_field":{"a":1}}
{"event":"reasoning_step","thought":"considering"}
{"event":"file_changed","path":"a.go","change":"modified","brand_new_key":true}
{"event":"sub_agent_spawned","children":[{"event":"user_prompt","text":"NOT a real turn"}]}
{"event":"usage","input_tokens":100,"output_tokens":20,"future_metric":1.5}
{"event":""}
{"no_event_key_at_all":true}
{"event":"session_ended","status":"completed"}
`)
	turns := parseTurns(data)
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1 (a nested prompt inside an unknown event is not a turn)", len(turns))
	}
	if turns[0].Prompt != "do the thing" {
		t.Errorf("prompt = %q", turns[0].Prompt)
	}
	if joined := strings.Join(turns[0].ModifiedFiles, ","); joined != "a.go" {
		t.Errorf("files = %q, want %q", joined, "a.go")
	}
	if turns[0].Usage.Sent != 100 || turns[0].Usage.Received != 20 {
		t.Errorf("usage = %+v, want 100 sent / 20 received", turns[0].Usage)
	}
	if got := sessionIDOf(data); got != "s1" {
		t.Errorf("sessionIDOf = %q, want %q", got, "s1")
	}
}

// The same guarantee at the lifecycle boundary: a JSONL transcript full of
// unknown events must not make turn-end error, because aider prints a non-zero
// exit into the user's session on every affected turn.
func TestParseHook_TurnEndNeverErrorsOnJSONL(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{"full fixture", nil}, // filled in below
		{"unknown events only", []byte(`{"event":"who_knows","x":1}` + "\n")},
		{"empty object lines", []byte("{}\n{}\n")},
		{"truncated first line", []byte(`{"event":"session_star`)},
		{"json array instead of object", []byte("[1,2,3]\n")},
		{"deeply nested junk", []byte(`{"event":"x","a":{"b":{"c":{"d":[[[1]]]}}}}` + "\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := repoDir(t)
			content := tc.content
			if content == nil {
				content = jsonlFixture(t)
			}
			seedTranscript(t, repo, content)
			if _, err := New().ParseHook(HookNameTurnEnd, []byte("{}")); err != nil {
				t.Errorf("turn-end returned an error (%v); it must always succeed", err)
			}
		})
	}
}

// --- group 4: a truncated final line ---------------------------------------

// The requirement the whole design turns on: an incomplete transcript is a
// PARTIAL result, never a discarded session. A whole-file json.Unmarshal would
// fail this outright.
func TestParseTurns_JSONLTruncatedFinalLineYieldsPartialResult(t *testing.T) {
	full := jsonlFixture(t)

	tests := []struct {
		name      string
		marker    string
		wantFiles string
		wantUsage bool
	}{
		{
			// Dies inside the last line. Everything before it is intact.
			name:      "inside session_ended",
			marker:    `{"timestamp": "2026-09-06T09:05:02`,
			wantFiles: "src/checkout/apply_coupon.ts,tests/checkout/apply_coupon.test.ts",
			wantUsage: true,
		},
		{
			// Dies inside checkpoint_created: the summary is gone, but the turn,
			// both edited files and the usage report all survive.
			name:      "inside checkpoint_created",
			marker:    `{"timestamp": "2026-09-06T09:04:49`,
			wantFiles: "src/checkout/apply_coupon.ts,tests/checkout/apply_coupon.test.ts",
			wantUsage: true,
		},
		{
			// Dies inside usage: the turn and its edits still stand.
			name:      "inside usage",
			marker:    `{"timestamp": "2026-09-06T09:04:33`,
			wantFiles: "src/checkout/apply_coupon.ts,tests/checkout/apply_coupon.test.ts",
			wantUsage: false,
		},
		{
			// Dies inside the very first file_changed, before any edit landed.
			// One turn with a prompt and nothing else — still not empty.
			name:      "inside first file_changed",
			marker:    `{"timestamp": "2026-09-06T09:02:31`,
			wantFiles: "",
			wantUsage: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			partial := truncateBefore(t, full, tc.marker)

			turns := parseTurns(partial)
			if len(turns) == 0 {
				t.Fatal("a truncated transcript produced NO turns; it must degrade to a partial result, not an empty session")
			}
			if len(turns) != 1 {
				t.Fatalf("len(turns) = %d, want 1", len(turns))
			}
			if !strings.HasPrefix(turns[0].Prompt, "Add coupon validation") {
				t.Errorf("prompt lost: %q", turns[0].Prompt)
			}
			if joined := strings.Join(turns[0].ModifiedFiles, ","); joined != tc.wantFiles {
				t.Errorf("files = %q, want %q", joined, tc.wantFiles)
			}
			if hasUsage := turns[0].Usage.Reports > 0; hasUsage != tc.wantUsage {
				t.Errorf("usage present = %v, want %v", hasUsage, tc.wantUsage)
			}
			// The session must still be identifiable, or the partial result has
			// nowhere to be filed.
			if got, want := sessionIDOf(partial), "btw-track3-demo-001"; got != want {
				t.Errorf("sessionIDOf = %q, want %q", got, want)
			}
		})
	}
}

// The analyzer surface must survive truncation too — no error anywhere.
func TestTranscriptAnalyzer_TruncatedJSONLNeverErrors(t *testing.T) {
	partial := truncateBefore(t, jsonlFixture(t), `{"timestamp": "2026-09-06T09:04:49`)
	path := writeTranscript(t, partial)
	a := New()

	if _, err := a.GetTranscriptPosition(path); err != nil {
		t.Errorf("GetTranscriptPosition: %v", err)
	}
	if _, _, err := a.ExtractModifiedFiles(path, 0); err != nil {
		t.Errorf("ExtractModifiedFiles: %v", err)
	}
	if _, err := a.ExtractPrompts(path, 0); err != nil {
		t.Errorf("ExtractPrompts: %v", err)
	}
	if _, _, err := a.ExtractSummary(path); err != nil {
		t.Errorf("ExtractSummary: %v", err)
	}
	if _, err := a.CalculateTokens(partial, 0); err != nil {
		t.Errorf("CalculateTokens: %v", err)
	}
}

// A truncated transcript still produces a checkpoint. Dropping the turn because
// the tail was incomplete is exactly the discarded-session failure.
func TestParseHook_TurnEndOnTruncatedJSONL(t *testing.T) {
	repo := repoDir(t)
	seedTranscript(t, repo, truncateBefore(t, jsonlFixture(t), `{"timestamp": "2026-09-06T09:04:49`))

	ev, err := New().ParseHook(HookNameTurnEnd, []byte("{}"))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if ev == nil {
		t.Fatal("a truncated transcript with real work produced no event")
	}
	if ev.SessionID != "btw-track3-demo-001" {
		t.Errorf("session id = %q", ev.SessionID)
	}
}

// --- the lifecycle path end to end -----------------------------------------

func TestParseHook_TurnEndOverJSONL(t *testing.T) {
	repo := repoDir(t)
	path := seedTranscript(t, repo, jsonlFixture(t))
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
	if first.SessionID != "btw-track3-demo-001" {
		t.Errorf("session id = %q, want %q", first.SessionID, "btw-track3-demo-001")
	}

	// Dedup is format-independent: the fingerprint measures observable
	// progress, and JSONL supplies all three components directly.
	if second, _ := a.ParseHook(HookNameTurnEnd, []byte("{}")); second != nil {
		t.Errorf("duplicate turn-end produced an event: %+v", second)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	_, _ = f.WriteString(`{"event":"user_prompt","session_id":"btw-track3-demo-001","text":"one more"}` + "\n" +
		`{"event":"file_changed","session_id":"btw-track3-demo-001","path":"src/new.ts"}` + "\n")
	_ = f.Close()

	if third, _ := a.ParseHook(HookNameTurnEnd, []byte("{}")); third == nil {
		t.Error("turn-end after real progress produced no event")
	}
}

// The zero-work guard is format-independent: a prompt with no usage report and
// no file change is not a checkpoint in either format.
func TestParseHook_TurnEndJSONLIgnoresTurnWithNoWork(t *testing.T) {
	repo := repoDir(t)
	seedTranscript(t, repo, []byte(`{"event":"session_started","session_id":"s1"}
{"event":"user_prompt","text":"add a thing"}
{"event":"tool_call","tool":"search","call_id":"t1"}
`))
	ev, err := New().ParseHook(HookNameTurnEnd, []byte("{}"))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if ev != nil {
		t.Errorf("a turn with no usage and no file change produced an event: %+v", ev)
	}
}

func TestSessionEvent_OverJSONL(t *testing.T) {
	repo := repoDir(t)
	seedTranscript(t, repo, jsonlFixture(t))

	for _, verb := range []string{HookNameSessionStart, HookNameSessionEnd} {
		ev, err := New().ParseHook(verb, []byte("{}"))
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if ev == nil {
			t.Fatalf("%s produced no event", verb)
		}
		if ev.SessionID != "btw-track3-demo-001" {
			t.Errorf("%s session id = %q", verb, ev.SessionID)
		}
	}
}

func TestReadSession_OverJSONL(t *testing.T) {
	repo := repoDir(t)
	path := seedTranscript(t, repo, jsonlFixture(t))

	session, err := New().ReadSession(&protocol.HookInputJSON{SessionRef: path})
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if session.SessionID != "btw-track3-demo-001" {
		t.Errorf("session id = %q", session.SessionID)
	}
	want := "src/checkout/apply_coupon.ts,tests/checkout/apply_coupon.test.ts"
	if joined := strings.Join(session.ModifiedFiles, ","); joined != want {
		t.Errorf("modified files = %q, want %q", joined, want)
	}
}

// --- chunking ---------------------------------------------------------------

// A JSONL chunk boundary may only fall on a line boundary: splitting a JSON
// object corrupts it, and a corrupted object is worse than a chunk over budget.
func TestChunkTranscript_JSONLNeverSplitsMidLine(t *testing.T) {
	a := New()
	data := jsonlFixture(t)

	for _, maxSize := range []int{16, 64, 200, 1000, len(data) - 1, len(data), len(data) + 1} {
		chunks, err := a.ChunkTranscript(data, maxSize)
		if err != nil {
			t.Fatalf("maxSize %d: %v", maxSize, err)
		}

		back, err := a.ReassembleTranscript(chunks)
		if err != nil {
			t.Fatalf("maxSize %d reassemble: %v", maxSize, err)
		}
		if !bytes.Equal(back, data) {
			t.Fatalf("maxSize %d: round trip lost bytes (%d in, %d out)", maxSize, len(data), len(back))
		}

		// Every line in every chunk must be a complete, parseable JSON object.
		for i, c := range chunks {
			for _, line := range strings.Split(string(c), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var probe map[string]any
				if err := json.Unmarshal([]byte(line), &probe); err != nil {
					t.Errorf("maxSize %d chunk %d holds a corrupted object: %v", maxSize, i, err)
					break
				}
			}
		}
	}
}

// An oversized line is emitted whole rather than split or rejected. Reassembly
// is concatenation, so an over-budget chunk still round-trips losslessly.
func TestChunkTranscript_JSONLOversizedLineIsEmittedWhole(t *testing.T) {
	a := New()
	long := `{"event":"agent_response","text":"` + strings.Repeat("x", 500) + `"}`
	data := []byte(`{"event":"user_prompt","text":"hi"}` + "\n" + long + "\n")

	chunks, err := a.ChunkTranscript(data, 64)
	if err != nil {
		t.Fatalf("ChunkTranscript: %v", err)
	}

	found := false
	for _, c := range chunks {
		if strings.Contains(string(c), long) {
			found = true
		}
	}
	if !found {
		t.Error("the oversized line was split; it must be emitted as one over-budget chunk")
	}

	back, err := a.ReassembleTranscript(chunks)
	if err != nil {
		t.Fatalf("ReassembleTranscript: %v", err)
	}
	if !bytes.Equal(back, data) {
		t.Error("round trip lost bytes")
	}
}

// Markdown keeps its byte-level fallback: it has no per-line structure to
// corrupt, so honouring the budget is the better trade there.
func TestChunkTranscript_MarkdownStillHonoursBudget(t *testing.T) {
	a := New()
	data := fixture(t)
	chunks, err := a.ChunkTranscript(data, 16)
	if err != nil {
		t.Fatalf("ChunkTranscript: %v", err)
	}
	for i, c := range chunks {
		if len(c) > 16 {
			t.Errorf("chunk %d is %d bytes, over the 16 byte budget", i, len(c))
		}
	}
}
