// Package aider implements the Entire external agent protocol for Aider
// (https://aider.chat). See AGENT.md for the protocol mapping and for every
// aider behaviour this package relies on, each verified against aider 0.86.2's
// source rather than its documentation.
package aider

import "regexp"

// Hook verbs. Only turnEnd is ever fired: aider's single outbound callback is
// --notifications-command, and it has no pre-prompt or session-lifecycle
// equivalent. The others are declared so Entire's routing accepts them if a
// future aider gains them.
const (
	HookNameTurnEnd      = "turn-end"
	HookNameSessionStart = "session-start"
	HookNameSessionEnd   = "session-end"
)

// Event type codes from the external agent protocol.
const (
	eventSessionStart = 1
	eventTurnEnd      = 3
	eventSessionEnd   = 5
)

// Paths, all relative to the repository root. Everything lives under .entire,
// which Entire's strategy layer already treats as protected, so transcripts are
// never recorded as session changes.
const (
	sessionDirRel = ".entire/aider"

	// transcriptRel is where BOTH transcript formats land. The path is part of
	// the managed config block, so leaving it alone is what keeps install-hooks
	// and every existing installation unaffected by the arrival of the
	// structured format — the format is detected from the bytes instead. It
	// also means there stays exactly one session directory and one state.json,
	// so turn-end dedup needs no per-format keying.
	transcriptRel = ".entire/aider/chat.md"
	llmHistoryRel = ".entire/aider/llm.log"
	inputHistRel  = ".entire/aider/input.history"
	stateFileName = "state.json"

	// aiderConfigFile is aider's YAML config. main.py builds
	// default_config_files including the git-root copy (main.py:474-475), and
	// aider parses it with configargparse, so every long flag is settable
	// here. This is what makes install-hooks meaningful: a plain `aider` run
	// picks the settings up with no wrapper.
	aiderConfigFile = ".aider.conf.yml"
)

// Chat-history markers for the MARKDOWN format, verified by generating
// testdata/chat_history.md with aider's own InputOutput writer. The structured
// JSONL format shares none of these; its discriminators live in jsonl.go, and
// detectFormat decides which decoder a transcript reaches.
const (
	// promptLinePrefix opens a user-input line. IO.user_input (io.py:775) sets
	// prefix = "####" and applies it to EVERY line of a multi-line prompt, so
	// consecutive prefixed lines are ONE turn, not several.
	promptLinePrefix = "#### "

	// blockquotePrefix marks tool output, warnings, errors and confirmation
	// answers — append_chat_history(blockquote=True) prepends "> ". Assistant
	// prose carries no marker at all.
	blockquotePrefix = "> "

	// blankPromptSentinel is what user_input writes for an empty submission.
	blankPromptSentinel = "<blank>"

	// sessionBannerPrefix opens each aider run inside the shared history file
	// (io.py:336). It is the only per-run boundary aider records, so it is what
	// session identity is derived from.
	sessionBannerPrefix = "# aider chat started at "

	// appliedEditPrefix is aider's authoritative "this file actually changed"
	// signal, emitted per path by Coder.apply_updates (base_coder.py:2334)
	// only after the edit lands. Preferred over scraping SEARCH/REPLACE
	// blocks, which also appear for edits that failed to apply.
	appliedEditPrefix = "Applied edit to "
)

// tokenReportRe matches aider's usage report as it reaches the chat history.
// Built at base_coder.py:2023 and emitted through tool_output, so it arrives
// blockquoted. The cache clauses appear only when the model reports them.
//
//	> Tokens: 15k sent, 1.1k cache write, 3.2k cache hit, 642 received. Cost: ...
var tokenReportRe = regexp.MustCompile(
	`Tokens:\s*([\d.]+k?)\s*sent` +
		`(?:,\s*([\d.]+k?)\s*cache write)?` +
		`(?:,\s*([\d.]+k?)\s*cache hit)?` +
		`,\s*([\d.]+k?)\s*received`)

// costReportRe matches the optional cost clause. Absent for models with no
// known pricing, where base_coder.py:2031 returns before building it.
var costReportRe = regexp.MustCompile(
	`Cost:\s*\$([\d.]+)\s*message,\s*\$([\d.]+)\s*session`)

// bannerTimestampRe extracts the timestamp from a session banner so it can be
// normalized into a session ID.
var bannerTimestampRe = regexp.MustCompile(
	`^# aider chat started at (\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})`)

// turn is one user prompt and everything aider emitted in response.
type turn struct {
	Index         int
	Prompt        string
	ModifiedFiles []string
	Usage         tokenReport
}

// tokenReport is a parsed usage report.
type tokenReport struct {
	Sent        int
	CacheWrite  int
	CacheHit    int
	Received    int
	Reports     int
	MessageCost float64
	SessionCost float64
}

func (r *tokenReport) add(o tokenReport) {
	r.Sent += o.Sent
	r.CacheWrite += o.CacheWrite
	r.CacheHit += o.CacheHit
	r.Received += o.Received
	r.Reports += o.Reports
	r.MessageCost += o.MessageCost
	if o.SessionCost > r.SessionCost {
		r.SessionCost = o.SessionCost
	}
}
