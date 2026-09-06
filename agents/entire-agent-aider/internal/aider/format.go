package aider

import "strings"

// Format names, used only for diagnostics and tests. Nothing branches on these
// strings at runtime — dispatch goes through the interface.
const (
	formatMarkdown = "markdown"
	formatJSONL    = "jsonl"
)

// transcriptFormat is everything that differs between the two shapes aider
// writes. Every consumer above this interface — fingerprinting, dedup, the four
// transcript_analyzer methods, token calculation, the lifecycle events — works
// on []turn and is written exactly once.
//
// The seam sits BELOW parseTurns rather than above it, because turn is the only
// type any caller sees: the twelve callers the code graph reports for parseTurns
// consume []turn and nothing else. Dispatching here reaches all of them without
// a second copy of any logic.
type transcriptFormat interface {
	// Name identifies the format for diagnostics.
	Name() string

	// Turns decodes the transcript into the shared representation. It cannot
	// fail: an unreadable region yields fewer turns, never an error. See
	// parseTurns for why that is a requirement rather than a convenience.
	Turns(data []byte) []turn

	// SessionID extracts the identifier of the most recent run, already
	// sanitised for use as a path component, or "" when there is none.
	SessionID(data []byte) string

	// Summary returns a session summary when the format records one.
	Summary(data []byte) (string, bool)

	// Segments slices the transcript into byte ranges that are preferred chunk
	// boundaries, preserving every byte in order.
	Segments(data []byte) [][]byte

	// SplittableMidLine reports whether a chunk boundary may fall inside a
	// line. False for any format whose lines are individually parseable units.
	SplittableMidLine() bool
}

// detectFormat picks a decoder by CONTENT, never by filename, extension or
// config key.
//
// Both formats arrive at the same path. chat-history-file stays
// .entire/aider/chat.md, so managedBlock, ProtectedFiles and install-hooks are
// untouched and existing users are unaffected by this change — and because
// there remains exactly one transcript path, one session directory and one
// state.json, the dedup bookkeeping needs no per-format keying either.
//
// The discriminator is the first non-blank line's leading "{". Nothing aider
// writes into a markdown chat history can start a line that way at the top of
// the file: IO writes the run banner ("# aider chat started at ...") before
// anything else (io.py:336), and every subsequent structural line begins with
// "#### " or "> ". Prefix matching rather than a trial json.Unmarshal is
// deliberate — it keeps detection working when the FIRST line is itself
// truncated, which a stricter check would misread as markdown and turn into a
// silently empty session.
func detectFormat(data []byte) transcriptFormat {
	if isJSONLTranscript(data) {
		return jsonlFormat{}
	}
	return markdownFormat{}
}

func isJSONLTranscript(data []byte) bool {
	for _, line := range splitLines(data) {
		c := strings.TrimSpace(classify(line))
		if c == "" {
			continue
		}
		return strings.HasPrefix(c, "{")
	}
	// Empty or blank-only content keeps the historical behaviour: markdown,
	// zero turns, no error. Aider creates the chat history lazily, so this is a
	// normal early-session state.
	return false
}

// sanitizeSessionID makes an identifier safe to use as a path component.
//
// Entire validates a session ID before using it that way and rejects the
// Windows volume separator, so the banner-derived ID is already normalised to
// digits and dashes. An ID that arrives from a transcript gets the same
// treatment rather than being trusted.
//
// Disallowed characters are REPLACED, not rejected. Discarding the whole ID
// would return "" and drop the checkpoint — the exact silent-capture-loss the
// structured format was supposed to remove — so a usable ID is always preferred
// to no ID. The one thing that cannot survive is an ID made entirely of dots,
// which would name a traversal component rather than a directory.
func sanitizeSessionID(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	lastWasDash := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
			lastWasDash = r == '-'
		default:
			// A run of disallowed characters collapses to one dash, so a path
			// like "C:/work" reads as "C-work" rather than "C--work".
			if !lastWasDash {
				b.WriteByte('-')
				lastWasDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if strings.Trim(s, ".") == "" {
		return ""
	}
	return s
}

// sessionIDOf derives the session identifier from whichever format the
// transcript is in.
func sessionIDOf(data []byte) string {
	return detectFormat(data).SessionID(data)
}

// --- markdown --------------------------------------------------------------

// markdownFormat is aider's original chat-history format, unchanged. Every
// method here delegates to the function that already implemented it, so the
// behaviour existing users depend on is the same code, not a reimplementation.
type markdownFormat struct{}

func (markdownFormat) Name() string { return formatMarkdown }

func (markdownFormat) Turns(data []byte) []turn { return parseMarkdownTurns(data) }

func (markdownFormat) SessionID(data []byte) string { return sessionIDFromBanner(data) }

// Summary reports none. Aider's markdown history holds no session summary
// anywhere, and inventing one from the transcript would be a guess presented as
// a fact. The JSONL format records one explicitly; this one does not.
func (markdownFormat) Summary([]byte) (string, bool) { return "", false }

func (markdownFormat) Segments(data []byte) [][]byte { return turnSegments(data) }

// SplittableMidLine is true: markdown has no per-line structure to corrupt, so
// reassembly by concatenation is lossless wherever a split lands. Honouring the
// size budget is the better trade, since dropping a session because one
// assistant message contained a very long line would be the worse failure.
func (markdownFormat) SplittableMidLine() bool { return true }
