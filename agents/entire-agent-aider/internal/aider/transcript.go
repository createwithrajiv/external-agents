package aider

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/protocol"
)

// splitLines splits a transcript into lines without discarding anything.
//
// Aider opens its chat history with line_endings="platform", so the same parser
// sees CRLF on Windows and LF elsewhere, and one file can hold both when a
// session moves between machines. Classification is therefore always done
// against a \r-trimmed copy while the original bytes are preserved for storage.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// classify strips the line ending and the trailing "  " that
// append_chat_history(linebreak=True) appends to every line it writes.
func classify(line string) string {
	return strings.TrimRight(strings.TrimSuffix(line, "\r"), " \t")
}

func isPromptLine(line string) bool {
	c := classify(line)
	return strings.HasPrefix(c, promptLinePrefix) || c == strings.TrimSpace(promptLinePrefix)
}

func promptLineText(line string) string {
	c := classify(line)
	if t, ok := strings.CutPrefix(c, promptLinePrefix); ok {
		return t
	}
	return ""
}

func blockquoteText(line string) (string, bool) {
	return strings.CutPrefix(classify(line), blockquotePrefix)
}

// parseTurns splits a transcript into turns, whichever format it is in.
//
// This is the single dispatch point for the whole package. Every caller — the
// lifecycle hooks, all four transcript_analyzer methods, token calculation —
// goes through here and sees []turn, so supporting a second format costs one
// decoder rather than a second copy of everything above it.
//
// It returns no error, and that signature is load-bearing rather than
// convenient. An unreadable region must cost only the events inside it: an
// incomplete transcript has to degrade into a partial result, never a discarded
// session, and an error return would invite exactly that discard at each of the
// twelve call sites.
func parseTurns(data []byte) []turn {
	return detectFormat(data).Turns(data)
}

// parseMarkdownTurns splits an aider chat history into turns.
//
// A turn opens at a RUN of consecutive prompt lines, not at each one: aider
// prefixes every line of a multi-line submission with "#### " (io.py:775), so
// treating each prefixed line as its own turn would count a two-line prompt as
// two turns and desynchronize every offset in this package.
func parseMarkdownTurns(data []byte) []turn {
	lines := splitLines(data)
	var turns []turn
	var cur *turn
	inPromptRun := false

	for _, line := range lines {
		if isPromptLine(line) {
			if !inPromptRun {
				turns = append(turns, turn{Index: len(turns)})
				cur = &turns[len(turns)-1]
				inPromptRun = true
			}
			text := promptLineText(line)
			if text == blankPromptSentinel && cur.Prompt == "" {
				continue
			}
			if cur.Prompt == "" {
				cur.Prompt = text
			} else {
				cur.Prompt += "\n" + text
			}
			continue
		}
		inPromptRun = false
		if cur == nil {
			// Preamble before the first prompt: the session banner and any
			// startup notices, not attributable to a turn.
			continue
		}
		if body, ok := blockquoteText(line); ok {
			absorbToolLine(cur, body)
		}
	}
	return turns
}

func absorbToolLine(t *turn, body string) {
	if path, ok := strings.CutPrefix(body, appliedEditPrefix); ok {
		path = strings.TrimSpace(path)
		if path != "" && !contains(t.ModifiedFiles, path) {
			t.ModifiedFiles = append(t.ModifiedFiles, path)
		}
		return
	}
	if rep, ok := parseTokenReport(body); ok {
		t.Usage.add(rep)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// parseTokenReport parses one usage line. Reports are independent rather than
// cumulative: show_usage_report zeroes message_tokens_sent/received and
// message_cost immediately after emitting each line (base_coder.py:2124-2126),
// so several reports inside one turn — which a lint or test reflection loop
// produces — must be summed.
func parseTokenReport(body string) (tokenReport, bool) {
	m := tokenReportRe.FindStringSubmatch(body)
	if m == nil {
		return tokenReport{}, false
	}
	rep := tokenReport{
		Sent:       parseTokenCount(m[1]),
		CacheWrite: parseTokenCount(m[2]),
		CacheHit:   parseTokenCount(m[3]),
		Received:   parseTokenCount(m[4]),
		Reports:    1,
	}
	if c := costReportRe.FindStringSubmatch(body); c != nil {
		rep.MessageCost, _ = strconv.ParseFloat(c[1], 64)
		rep.SessionCost, _ = strconv.ParseFloat(c[2], 64)
	}
	return rep, true
}

// parseTokenCount reads one of aider's formatted token counts.
//
// The value is LOSSY above 999 and no unrounded copy exists anywhere on disk:
// format_tokens (utils.py:276) renders <1000 exactly, <10000 as "1.2k", and
// everything else as "15k". Token metadata for aider sessions is therefore
// exact below 1000 and rounded above it, which AGENT.md states plainly rather
// than presenting rounded figures as precise.
func parseTokenCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := 1.0
	if v, ok := strings.CutSuffix(s, "k"); ok {
		s = v
		mult = 1000
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(math.Round(f * mult))
}

// sessionIDFromBanner derives a session ID from the most recent aider run
// banner in the transcript.
//
// Aider records no session identifier of any kind. The banner IO writes on
// startup (io.py:336) is the only per-run marker in the file, so its timestamp
// becomes the ID. Normalized to digits and dashes because Entire validates a
// session ID before using it as a path component and rejects colons.
func sessionIDFromBanner(data []byte) string {
	last := ""
	for _, line := range splitLines(data) {
		c := classify(line)
		if !strings.HasPrefix(c, sessionBannerPrefix) {
			continue
		}
		if m := bannerTimestampRe.FindStringSubmatch(c); m != nil {
			last = m[1] + m[2] + m[3] + "-" + m[4] + m[5] + m[6]
		}
	}
	return last
}

// readTurns loads and parses a transcript, tolerating a missing file.
//
// A transcript that does not exist yet is not an error: aider creates the chat
// history lazily on its first append.
func readTurns(path string) ([]turn, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read aider transcript: %w", err)
	}
	return parseTurns(data), nil
}

// --- transcript_analyzer capability --------------------------------------

// GetTranscriptPosition returns the number of turns in the transcript.
//
// Turn count, not line count, is the unit every offset here is expressed in,
// because a turn is the smallest thing that maps onto a lifecycle event.
func (a *Agent) GetTranscriptPosition(path string) (int, error) {
	turns, err := readTurns(path)
	if err != nil {
		return 0, err
	}
	return len(turns), nil
}

// ExtractModifiedFiles returns the files aider reported as edited from offset
// onward, plus the current turn count.
func (a *Agent) ExtractModifiedFiles(path string, offset int) ([]string, int, error) {
	turns, err := readTurns(path)
	if err != nil {
		return nil, 0, err
	}
	files := []string{}
	for _, t := range turns {
		if t.Index < offset {
			continue
		}
		for _, f := range t.ModifiedFiles {
			if !contains(files, f) {
				files = append(files, f)
			}
		}
	}
	return files, len(turns), nil
}

// ExtractPrompts returns the user submissions from offset onward.
func (a *Agent) ExtractPrompts(sessionRef string, offset int) ([]string, error) {
	turns, err := readTurns(sessionRef)
	if err != nil {
		return nil, err
	}
	prompts := []string{}
	for _, t := range turns {
		if t.Index < offset {
			continue
		}
		if t.Prompt != "" {
			prompts = append(prompts, t.Prompt)
		}
	}
	return prompts, nil
}

// ExtractSummary returns a session summary when the transcript records one.
//
// Whether there is one to return is a property of the FORMAT, not of aider. The
// original markdown history holds no summary anywhere, so reporting one there
// would be a guess presented as a fact. The structured format records the
// agent's own summary, intent and open questions in a checkpoint_created event,
// so reporting those is reading a record rather than inventing one.
func (a *Agent) ExtractSummary(sessionRef string) (string, bool, error) {
	data, err := os.ReadFile(sessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			// Aider creates the transcript lazily on its first append, so an
			// absent file is a normal early-session state, not a failure.
			return "", false, nil
		}
		return "", false, fmt.Errorf("read aider transcript: %w", err)
	}
	summary, ok := detectFormat(data).Summary(data)
	return summary, ok, nil
}

// --- token_calculator capability -----------------------------------------

// CalculateTokens sums aider's own usage reports from offset onward.
func (a *Agent) CalculateTokens(data []byte, offset int) (protocol.TokenUsageResponse, error) {
	turns := parseTurns(data)
	var total tokenReport
	for _, t := range turns {
		if t.Index < offset {
			continue
		}
		total.add(t.Usage)
	}
	if total.Reports == 0 {
		return protocol.TokenUsageResponse{}, nil
	}
	// Aider's "sent" figure covers the whole prompt including any cached
	// portion, so fresh input is what remains after the cache clauses. Clamped
	// at zero: the components are independently rounded, and on a heavily
	// cached turn that rounding can push the difference negative.
	input := total.Sent - total.CacheWrite - total.CacheHit
	if input < 0 {
		input = 0
	}
	return protocol.TokenUsageResponse{
		InputTokens:         input,
		CacheCreationTokens: total.CacheWrite,
		CacheReadTokens:     total.CacheHit,
		OutputTokens:        total.Received,
		APICallCount:        total.Reports,
	}, nil
}

// --- chunking -------------------------------------------------------------

// ChunkTranscript splits a transcript into chunks of at most maxSize bytes,
// preferring the boundaries its format declares.
//
// Reassembly is plain concatenation, so a split is lossless wherever it lands.
// What differs between the formats is whether a split is legal at all inside a
// line. Markdown has no per-line structure to corrupt, so an oversized line is
// cut at a byte boundary and the budget is honoured — dropping a session
// because one assistant message contained a very long line would be the worse
// failure. JSONL lines are individually parseable objects, so cutting one
// corrupts it; there an oversized line is emitted whole as a single
// over-budget chunk, which still round-trips exactly.
func (a *Agent) ChunkTranscript(content []byte, maxSize int) ([][]byte, error) {
	if len(content) == 0 {
		return [][]byte{}, nil
	}
	if maxSize <= 0 {
		return nil, fmt.Errorf("invalid max chunk size %d", maxSize)
	}
	if len(content) <= maxSize {
		return [][]byte{content}, nil
	}

	var chunks [][]byte
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, cur)
			cur = nil
		}
	}
	format := detectFormat(content)
	for _, seg := range format.Segments(content) {
		if len(cur)+len(seg) <= maxSize {
			cur = append(cur, seg...)
			continue
		}
		flush()
		if len(seg) <= maxSize {
			cur = append([]byte(nil), seg...)
			continue
		}
		if !format.SplittableMidLine() {
			// Over budget, but intact. Rejecting it would discard the session
			// and splitting it would hand back a corrupted object.
			chunks = append(chunks, append([]byte(nil), seg...))
			continue
		}
		parts := splitBytes(seg, maxSize)
		chunks = append(chunks, parts[:len(parts)-1]...)
		cur = append([]byte(nil), parts[len(parts)-1]...)
	}
	flush()
	return chunks, nil
}

// ReassembleTranscript concatenates chunks. ChunkTranscript never inserts or
// drops a byte, so this is an exact inverse.
func (a *Agent) ReassembleTranscript(chunks [][]byte) ([]byte, error) {
	n := 0
	for _, c := range chunks {
		n += len(c)
	}
	out := make([]byte, 0, n)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out, nil
}

// turnSegments slices a markdown chat history into byte ranges that each begin
// at a turn boundary, preserving every byte in order.
func turnSegments(content []byte) [][]byte {
	lines := splitLines(content)
	var segments [][]byte
	var cur []byte
	inPromptRun := false

	for i, line := range lines {
		raw := []byte(line)
		if i < len(lines)-1 {
			raw = append(raw, '\n')
		}
		if isPromptLine(line) {
			if !inPromptRun && len(cur) > 0 {
				segments = append(segments, cur)
				cur = nil
			}
			inPromptRun = true
		} else {
			inPromptRun = false
		}
		cur = append(cur, raw...)
	}
	if len(cur) > 0 {
		segments = append(segments, cur)
	}
	return segments
}

// splitBytes chops b into pieces of at most size bytes, always returning at
// least one piece.
func splitBytes(b []byte, size int) [][]byte {
	var out [][]byte
	for len(b) > size {
		out = append(out, b[:size])
		b = b[size:]
	}
	out = append(out, b)
	return out
}
