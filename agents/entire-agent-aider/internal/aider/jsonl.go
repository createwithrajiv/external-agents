package aider

import (
	"encoding/json"
	"strings"
)

// jsonlFormat decodes aider's structured transcript: one JSON object per line,
// each carrying an "event" discriminator.
//
// Two properties of the format drive every decision in this file. Lines are
// independent, so a damaged one costs exactly one event rather than the whole
// file; and the schema is open, so this build will meet event types and fields
// it has never heard of.
type jsonlFormat struct{}

func (jsonlFormat) Name() string { return formatJSONL }

// Event discriminators this build understands. Anything else is ignored, which
// is a supported outcome rather than a fallback — see decodeJSONLLine.
const (
	jsonlEventUserPrompt        = "user_prompt"
	jsonlEventFileChanged       = "file_changed"
	jsonlEventUsage             = "usage"
	jsonlEventCheckpointCreated = "checkpoint_created"
)

// jsonlEvent is the union of the fields this build reads. Only fields actually
// observed in the format are declared: encoding/json silently drops the rest,
// so an event carrying twenty keys we do not know about costs nothing, and
// speculating about keys that may never exist would be inventing a schema.
//
// Summary is shared by file_changed and checkpoint_created. That is safe
// because every read is guarded by a switch on Event.
type jsonlEvent struct {
	Event     string `json:"event"`
	SessionID string `json:"session_id"`

	// user_prompt
	Text string `json:"text"`

	// file_changed
	Path string `json:"path"`

	// usage. Reported as exact integers, unlike markdown's format_tokens
	// output, which is lossy above 999.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`

	// checkpoint_created
	Summary       string   `json:"summary"`
	Intent        string   `json:"intent"`
	OpenQuestions []string `json:"open_questions"`
}

// decodeJSONLLine parses one line, reporting ok=false for anything that is not
// a usable JSON object.
//
// A false here is never an error. It covers the blank line at end of file, a
// line truncated because the writer died mid-append, and a line holding a JSON
// value that is not an object. All three must leave the surrounding transcript
// intact: an incomplete transcript is a partial result, not a discarded
// session.
func decodeJSONLLine(line string) (jsonlEvent, bool) {
	c := strings.TrimSpace(classify(line))
	if !strings.HasPrefix(c, "{") {
		return jsonlEvent{}, false
	}
	var ev jsonlEvent
	if json.Unmarshal([]byte(c), &ev) != nil {
		return jsonlEvent{}, false
	}
	return ev, true
}

// eachJSONLEvent walks the decodable events in order, skipping the rest.
func eachJSONLEvent(data []byte, fn func(jsonlEvent)) {
	for _, line := range splitLines(data) {
		if ev, ok := decodeJSONLLine(line); ok {
			fn(ev)
		}
	}
}

// Turns folds the event stream into the shared turn representation.
//
// A turn opens at each user_prompt. Markdown needs run-collapsing because aider
// prefixes every line of a multi-line submission with "#### " (io.py:775);
// JSONL carries the whole submission in one event's text field, so one event is
// one turn and an embedded newline is just content.
//
// Every unrecognised event falls through the switch and contributes nothing.
// That is what keeps a future format addition from being able to break this
// build: the parser reads the events it knows and is inert to the rest.
func (jsonlFormat) Turns(data []byte) []turn {
	var turns []turn
	var cur *turn

	eachJSONLEvent(data, func(ev jsonlEvent) {
		switch ev.Event {
		case jsonlEventUserPrompt:
			turns = append(turns, turn{Index: len(turns), Prompt: ev.Text})
			cur = &turns[len(turns)-1]

		case jsonlEventFileChanged:
			if cur == nil {
				// Preamble before the first prompt, the same case the markdown
				// parser skips: not attributable to a turn.
				return
			}
			path := strings.TrimSpace(ev.Path)
			if path != "" && !contains(cur.ModifiedFiles, path) {
				cur.ModifiedFiles = append(cur.ModifiedFiles, path)
			}

		case jsonlEventUsage:
			if cur == nil {
				return
			}
			// Sent carries the whole prompt in the markdown format, where the
			// cache clauses are subtracted back out in CalculateTokens. This
			// format reports no cache figures at all, so the subtraction is an
			// identity here and input_tokens passes through exactly.
			cur.Usage.add(tokenReport{
				Sent:     ev.InputTokens,
				Received: ev.OutputTokens,
				Reports:  1,
			})
		}
	})
	return turns
}

// SessionID returns the most recent run's identifier.
//
// The last id seen wins, matching the markdown rule that the latest banner wins
// when one file spans several runs. Unlike markdown, no derivation is needed —
// the format states the id — but it is sanitised all the same, because it
// becomes a path component and nothing guarantees an emitter keeps it tame.
func (jsonlFormat) SessionID(data []byte) string {
	last := ""
	eachJSONLEvent(data, func(ev jsonlEvent) {
		if id := sanitizeSessionID(strings.TrimSpace(ev.SessionID)); id != "" {
			last = id
		}
	})
	return last
}

// Summary renders the most recent checkpoint_created event.
//
// This is the one capability the new format adds outright: the agent states its
// own summary, intent and open questions, so reporting them is reading a
// record rather than guessing at one. The markdown format still reports none.
func (jsonlFormat) Summary(data []byte) (string, bool) {
	var last *jsonlEvent
	eachJSONLEvent(data, func(ev jsonlEvent) {
		if ev.Event == jsonlEventCheckpointCreated {
			e := ev
			last = &e
		}
	})
	if last == nil {
		return "", false
	}

	var sections []string
	if s := strings.TrimSpace(last.Summary); s != "" {
		sections = append(sections, s)
	}
	if s := strings.TrimSpace(last.Intent); s != "" {
		sections = append(sections, "Intent: "+s)
	}
	questions := []string{"Open questions:"}
	for _, q := range last.OpenQuestions {
		if q = strings.TrimSpace(q); q != "" {
			questions = append(questions, "- "+q)
		}
	}
	if len(questions) > 1 {
		sections = append(sections, strings.Join(questions, "\n"))
	}

	if len(sections) == 0 {
		// A checkpoint_created carrying nothing readable is no summary at all,
		// and an empty string reported as present would be worse than absent.
		return "", false
	}
	return strings.Join(sections, "\n\n"), true
}

// Segments returns one segment per line, newline included.
//
// Line boundaries are the ONLY legal chunk boundaries for this format, so there
// is nothing coarser to prefer: a turn-sized segment would just be a run of
// lines the chunker can pack itself.
func (jsonlFormat) Segments(data []byte) [][]byte {
	lines := splitLines(data)
	segments := make([][]byte, 0, len(lines))
	for i, line := range lines {
		raw := []byte(line)
		if i < len(lines)-1 {
			raw = append(raw, '\n')
		}
		if len(raw) > 0 {
			segments = append(segments, raw)
		}
	}
	return segments
}

// SplittableMidLine is false: splitting a JSON object corrupts it, and a
// corrupted object is a worse outcome than a chunk over budget. An oversized
// line is emitted whole instead — reassembly is plain concatenation, so that
// still round-trips losslessly.
func (jsonlFormat) SplittableMidLine() bool { return false }
