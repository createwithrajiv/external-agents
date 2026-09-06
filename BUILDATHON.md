# entire-agent-aider

## One-sentence summary

An Entire integration for [Aider](https://aider.chat) — the one widely-used AI
coding agent Entire could not capture, because aider has no hook system at all.

## Problem, intended user and why it matters

**The user:** a developer who uses aider as their daily coding agent and wants
the same session history, checkpoints and rewind that Entire already gives
Claude Code, Codex, Cursor, Gemini, Copilot, OpenCode and Pi users.

**The problem:** aider users got none of it. Every aider session was invisible
to Entire — no transcript, no checkpoints, no way to recover from a bad run, no
record of why code changed.

**Why it was still missing:** every other integration in this repo works because
its agent offers lifecycle hooks. Aider offers none. It exposes exactly one
outbound callback, `--notifications-command`, and reading its implementation
(`aider/io.py:1088`) shows five constraints that make it much weaker than it
looks:

1. It only runs if `notifications: true` is *also* set — the guard is
   `if self.bell_on_next_input and self.notifications`. Setting just the command
   is a silent no-op that captures nothing.
2. It receives **no arguments, no environment and no stdin**, so it cannot say
   which session fired.
3. Its output is discarded (`capture_output=True`).
4. A non-zero exit is shown to the user as
   `Failed to run notifications command: ...` — a failing hook nags them on
   every turn.
5. **It is not a turn-end signal.** `ring_bell()` is reached from `get_input()`,
   `confirm_ask()` and `prompt_ask()`, so it fires mid-turn at any confirmation
   prompt, and never at all in headless mode.

Aider also writes its own state *into the working tree* — `.aider.chat.history.md`
and a repo-map cache land in the repository root — where every other agent uses
the user's home directory.

## Selected Entire track and why Entire is essential

**Track 3 — Bring Entire to a New Agent or Workflow.**

This is not Entire bolted onto a product for tracking. The product *is* an
Entire integration: it implements the Entire external agent protocol
(`info`, `detect`, `parse-hook`, `install-hooks`, `get-transcript-position`,
`extract-modified-files`, `extract-prompts`, `calculate-tokens`, and the session
and transcript subcommands) as a standalone `entire-agent-aider` binary that
Entire discovers on `$PATH`. Without Entire there is no product; without this
adapter Entire has no aider support.

## Architecture and main workflow

```
aider (no hook system)
  │
  │  .aider.conf.yml  ← written by install-hooks, loaded by aider from the git
  │                     root via configargparse (main.py:474-475)
  │     notifications: true
  │     notifications-command: "echo {} | entire hooks aider turn-end"
  │     chat-history-file: .entire/aider/chat.md
  │
  └─ model round finishes ──► entire hooks aider turn-end
                                   │
                                   ▼
                          entire-agent-aider parse-hook
                                   │ reads .entire/aider/chat.md
                                   │ computes turn fingerprint
                                   │ compares against state.json
                                   ▼
                            Event{TurnEnd} ──► Entire checkpoint
```

**No wrapper command.** Because the configuration lives in a file aider already
reads, a plain `aider` run in the repo is captured. There is nothing new for the
user to remember.

**Three design decisions worth defending:**

| Decision | Why |
|---|---|
| Config file, not a wrapper process | `install-hooks` becomes meaningful and reversible, and capture works for the user's existing habits |
| Fingerprint dedup, not a turn index | Constraint 5 above: the bell is not a turn-end signal |
| `Applied edit to <path>` for file changes | Emitted by `Coder.apply_updates` (`base_coder.py:2334`) only *after* an edit lands; scraping SEARCH/REPLACE blocks also catches edits that failed |

**The dedup fingerprint** is `{turn index, usage-report count, applied-edit count}`.
A plain turn index is insufficient because of the ordering inside
`Coder.send_message`:

```
base_coder.py:1423  llm_started()        → arms the bell
base_coder.py:1531  show_usage_report()  → writes "> Tokens: ..."
base_coder.py:1585  apply_updates()      → writes "> Applied edit to ..."
base_coder.py:1589  auto_commit()        → git commit
```

A confirmation prompt inside `apply_updates` rings the bell *before* the commit;
a clean turn rings it at `get_input()` after everything. A turn with neither a
usage report nor an applied edit is treated as "nothing observable happened" and
produces no checkpoint.

**Transcript parsing** handles two traps that silently corrupt naive parsers:

- **A multi-line prompt is one turn.** `user_input` (`io.py:775`) prefixes
  *every* line with `#### `, so a two-line submission produces two prefixed
  lines. Turns open at a *run* of prefixed lines. Our fixture has 5 `####` lines
  and 4 turns.
- **Line endings are platform-native.** Aider defaults to
  `line_endings="platform"`, so the same file is CRLF on Windows and LF
  elsewhere.

## Entire Graph findings and verification

**1. Search — locating the concept.**

```
entire graph search --query "aider turn end deduplication fingerprint" --repo .
```

Top hit: `Agent.parseTurnEnd` at
`agents/entire-agent-aider/internal/aider/hooks.go:233-277`. The graph also
emitted a VERIFY command naming the narrowest covering test, which we ran.

**2. Impact analysis before a high-risk change.**

```
entire graph impact --symbol parseTurns --repo . --depth 2
```

> Blast radius: **12 callers (8 direct, 4 transitive)**, 5 callees,
> 2 type consumers, 3 data flows.

Direct callers: `Agent.ReadSession`, `Agent.parseTurnEnd`, `readTurns`,
`Agent.CalculateTokens`, plus four tests. Transitive (via `readTurns`):
`Agent.GetTranscriptPosition`, `Agent.ExtractModifiedFiles`,
`Agent.ExtractPrompts`, and `Agent.ParseHook` via `parseTurnEnd`.

This identified `parseTurns` as the highest-fan-in function in the package — the
single place where a parsing change breaks the most behaviour — which is exactly
why the turn-grouping rule is the most heavily tested part of the suite.

**3. Verification against source (graph is evidence, not an oracle).**

```
$ grep -n "parseTurns(" *.go | grep -v "func parseTurns" | wc -l
8
```

**The graph reported 8 direct callers; the source has exactly 8 call sites.**
The claim was checked rather than trusted. The full test suite
(`go test ./...`, 65 cases, 92.1% statement coverage) passes against the same
tree. *(Counts as of the pre-curveball commit; see the Noon Curveball section
for the current figures.)*

**4. Semantic diff of the submitted implementation.**

```
entire graph diff --base f31731c --head HEAD --repo . -- agents/entire-agent-aider
```

Scoped to the adapter, the entity-level change list is:

```
internal/aider/agent_test.go       + 12 functions added
internal/aider/hooks_test.go       + 14 functions added
internal/aider/state_test.go       +  8 functions added
internal/aider/transcript_test.go  + 11 functions added
internal/aider/state.go            ~ function loadState body changed (5 dependents)
```

**The finding that matters: exactly one production function changed.**
`loadState`, with 5 dependents flagged — which is precisely the null-unmarshal
bug the new tests surfaced. Every other entity in the diff is an added test.

That is the verification we wanted from the diff: it confirms the test pass
introduced no incidental behaviour change, and it independently points at the
one function whose behaviour did change, matching what the test failure told us.
The "5 dependents" count is also the graph's own signal to run the tests before
trusting the change — which we did (`go test ./...`, 65 cases green).

**5. Semantic diff of the Noon Curveball change.**

```
entire graph diff --base b8c8af3 --head HEAD --repo . -- agents/entire-agent-aider
```

The entity-level change list separates cleanly into *new* and *touched*:

```
format.go   + 18 entities  (transcriptFormat + its 6 methods, detectFormat,
                            isJSONLTranscript, sanitizeSessionID, sessionIDOf,
                            markdownFormat + its 6 methods)
jsonl.go    + 19 entities  (jsonlFormat + its 6 methods, jsonlEvent + 8 fields,
                            decodeJSONLLine, eachJSONLEvent)

agent.go        ~ Agent.GetSessionID    body changed  (19 dependents)
                ~ Agent.ReadSession     body changed  (33 dependents)
hooks.go        ~ Agent.sessionEvent    body changed  ( 1 dependent)
                ~ Agent.parseTurnEnd    body changed  ( 1 dependent)
transcript.go   ~ parseTurns            body changed  (15 dependents)
                + parseMarkdownTurns    added
                ~ Agent.ExtractSummary  signature      (35 dependents)
                ~ Agent.ChunkTranscript body changed  (28 dependents)
types.go        ~ constants and comments
```

**The finding that matters: seven touched production entities, and every one of
them is a dispatch site.** Four are the `sessionIDFromBanner` → `sessionIDOf`
swap, one is `parseTurns` becoming a one-line dispatch, and two are
`ExtractSummary` and `ChunkTranscript` consulting the format. That is the
negative claim the diff can make and a line-level `git diff` cannot: **no
Markdown parsing logic changed at all.** `isPromptLine`, `blockquoteText`,
`absorbToolLine`, `parseTokenReport`, `parseTokenCount`, `sessionIDFromBanner`,
`turnSegments`, `fingerprintOf`, `advancedOver`, `loadState`,
`GetTranscriptPosition`, `ExtractModifiedFiles`, `ExtractPrompts`,
`CalculateTokens`, `managedBlock`, `InstallHooks`, `Info` — none appear in the
diff. Support for a second format was added without touching the first one's
behaviour, which is what "no duplicated implementation" has to mean in practice.

Two honest readings of that list. `parseMarkdownTurns` shows as **added** rather
than changed because the original `parseTurns` body was moved under a new name;
the graph cannot prove the body is byte-identical, so the 65 unchanged
pre-existing test cases are what carries that claim instead. And
`ExtractSummary`'s "signature changed" is the parameter going from an unused
`_ string` to a named `sessionRef string` — the types are identical; it now
reads the file it was always handed.

The dependent counts are the risk signal: 35 on `ExtractSummary`, 33 on
`ReadSession`, 28 on `ChunkTranscript`. All three are high-fan-in, which is why
the full suite was run rather than only the new tests.

## Noon Curveball: what changed and how we adapted

**The curveball.** Aider released a new JSONL transcript and lifecycle event
format. Existing users still produce the original Markdown. Three requirements:
support both without duplicating the implementation, never crash on an unknown
event, and turn an incomplete transcript into a *partial* result rather than a
discarded session.

**Where the graph pointed the change.** The same
`entire graph impact --symbol parseTurns --depth 2` from section 2 is what made
the design obvious rather than guessed:

> Type consumers (0 in, 2 out): `-> turn` [RETURNS_TYPE], `-> turn` [USES_TYPE]

All 12 callers consume `[]turn` and **nothing else**. So the format seam belongs
*below* `parseTurns`, not above it: one dispatch point reaches every caller, and
no caller changes. Putting it above would have meant editing all four production
callers plus three transitive ones and maintaining two copies of the analyzer
surface. The callee list said the same thing from the other side — `splitLines`,
`isPromptLine`, `promptLineText`, `blockquoteText`, `absorbToolLine` are all
Markdown-shape-specific, and they are exactly what a second format replaces.

The graph also **under-reported one path**, which is why its output is treated
as evidence rather than an oracle: `ChunkTranscript` never appears in
`parseTurns`' blast radius, because it reaches the Markdown helpers through
`turnSegments` as a *sibling* rather than through `parseTurns`. It was found by
reading the callee list and checking who else used those five helpers.

**What was built.**

```
                         .entire/aider/chat.md          ← path UNCHANGED
                                  │
                          detectFormat(data)            ← by CONTENT, not filename
                        ┌─────────┴─────────┐
              markdownFormat            jsonlFormat
                        └─────────┬─────────┘
                            []turn (shared)
                                  │
     fingerprint · dedup · state.json · turn-end event · the four
     transcript_analyzer methods · CalculateTokens · chunking
                          — all written ONCE
```

| Requirement | How it is met |
|---|---|
| No duplicated implementation | One IR (`turn`), two decoders behind `transcriptFormat`; everything above the interface is shared. `markdownFormat` *delegates to the original functions* — it is not a reimplementation |
| Never crash on unknown events | The JSONL decoder switches on `event` and falls through for anything else; `encoding/json` drops unknown fields for free. Mirrors the existing `ParseHook` `default: return nil, nil` one layer down |
| Partial, not discarded | Decoding is per line. A malformed line costs that one event. `parseTurns` returns **no error** — a signature that is load-bearing, since an error return would invite a discard at each of the 12 call sites |

**Detection is by content, never by filename or config key.** The first
non-blank line's leading `{` decides. This is what keeps `managedBlock()`,
`ProtectedFiles`, `install-hooks` and `chat-history-file` untouched, so existing
users are unaffected — and because there is still exactly one transcript path,
there is still one session directory and one `state.json`, so turn-end dedup
needed no per-format keying. Prefix matching rather than a trial
`json.Unmarshal` is deliberate: it keeps detection working when the *first* line
is itself truncated, which a stricter check would misread as Markdown and turn
into a silently empty session.

**Four things the new format fixes.**

1. **Session identity.** `parseTurnEnd` returns `nil` when the session ID is
   empty, and the Markdown ID is derived from a run banner. JSONL states
   `session_id` outright — but it is sanitised the same way, because it becomes
   a path component. Disallowed characters are *replaced*, not rejected:
   discarding the ID would return `""` and drop the checkpoint, which is the
   exact silent capture loss the structured format was meant to remove.
2. **Token counts are exact.** `input_tokens: 8421`, not Markdown's rounded
   `8.4k`.
3. **`ExtractSummary` finally has something to report.** `checkpoint_created`
   carries the agent's own `summary`, `intent` and `open_questions`. Markdown
   still reports none — that is a property of the format, not of aider, and
   inventing one there would still be a guess.
4. **A real `session_ended` event** exists, where aider previously had none.

**One trade made explicitly.** `ChunkTranscript`'s original comment already
predicted this case: *"a JSONL chunker must reject an oversized line because
splitting a JSON object corrupts it, but Markdown has no such constraint."* The
formats now answer `SplittableMidLine()` differently. Markdown keeps its
byte-level fallback and honours the size budget. JSONL emits an oversized line
whole, as a single over-budget chunk — reassembly is concatenation, so it still
round-trips exactly, and a corrupted JSON object is worse than a chunk over
budget.

**What was deliberately *not* done.** `FormatResumeCommand` still emits
`aider --restore-chat-history`, documented in-source as **unverified** for
JSONL. The flag is confirmed against the Markdown history
(`base_coder.py:520`); whether it also accepts JSONL has not been checked
against aider's source. Emitting an invented flag would fail exactly when the
user needs the resume, and dropping the capability would break every existing
user. No speculative schema was added either: `jsonlEvent` declares only fields
actually observed in the format.

**Verification.** Tests were written to fail first, covering all four required
groups — original Markdown, new JSONL, unknown events, and a truncated final
line at four different cut points. The truncated cases assert the turns parsed
so far, a still-resolvable session ID, and explicitly fail on an empty result.

```
before:  45 top-level tests · 65 cases · 92.1% coverage
after:   71 top-level tests · 108 cases · 93.3% coverage
```

The four original test files are **byte-identical** to the pre-curveball commit
(`git diff --stat` empty), and running exactly the 45 original test names still
yields 65 passing cases. Nothing was rewritten to accommodate the new format.

## Checkpoint links and what each checkpoint proves

Repository mirror: `entire://aws-ap-south-1.entire.io/gh/createwithrajiv/external-agents`
(Entire project `01M1TN3W2GGQC5QEDDKTYRYG9T`, region `in`). Open any checkpoint with
`entire checkpoint explain <id>` from a clone of that mirror.

| Milestone | Checkpoint | Commit | What it proves |
|---|---|---|---|
| Initial understanding and intended architecture | `e42c966daba2` | `24a9739` | Why aider is the hard case — no hook system, only `--notifications-command` with five documented weaknesses — and the two decisions that follow: config file over wrapper, fingerprint over turn index. Records the rejected wrapper option and why it loses |
| Last stable state before the Noon Curveball | `c47c0664af62` | `8327df7` | Working end-to-end capture with the suite green: 65 cases, 92.1% coverage, and a semantic diff proving exactly one production entity changed in the preceding test pass |
| Response to the Noon Curveball | `4309066d4958` | `047450c` | The fresh session reconstructed the project from checkpoint context before reading any code, ran `entire graph impact --symbol parseTurns --depth 2`, and used its type-consumer output to place the format seam below `parseTurns`. Dual-format support, tests first, then green |
| Final implementation and verification | `6dce1122db8e` | `e33d8a5` | The semantic diff of the Curveball change itself: seven touched production entities, every one a dispatch site, and no Markdown parsing logic in the list — the evidence that a second format was added without disturbing the first |

**On the first checkpoint's intent line.** It reads *"Review the uncommitted work in this
repo…"* rather than "initial understanding", because the architecture was explained in
response to a review request rather than written up in advance. The body carries the
substance — the wrapper rejection and the fingerprint rationale are both argued there in
full — but the intent line understates it, and that is a property of how the work
happened rather than something worth retrofitting.

**Session continuity.** The Curveball response ran in a genuinely fresh agent session
(`3acaeb06-cb2c…`), separate from the session that built the original adapter
(`a9711272-cda1…`). Reconstruction from checkpoint context came first, before any file
was opened — which is what the required workflow asks for, and what makes the
checkpoints load-bearing rather than decorative.

## Setup, run and test instructions

```bash
# 1. Build and install the adapter
cd agents/entire-agent-aider
go build -o entire-agent-aider ./cmd/entire-agent-aider
cp entire-agent-aider ~/.local/bin/          # anywhere on $PATH

# 2. Opt the repo into external agents (must be the LOCAL settings file —
#    Entire refuses the grant from a tracked file so a repo cannot ship it)
cd /path/to/your/repo
echo '{"external_agents": true}' > .entire/settings.local.json

# 3. Enable
entire enable -y --agent aider --telemetry=false

# 4. Use aider normally
export OPENAI_API_KEY=sk-...
aider --model gpt-4o
```

Then `entire status` shows the live session:

```
── Active Sessions ──────────────────────────────
Aider · 20260906-112206
> "add a farewell function to greeting.py"
started just now · tokens 2.6k
```

**Tests:**

```bash
cd agents/entire-agent-aider
go test ./...                 # 108 cases
go test -cover ./internal/aider/   # 93.3% of statements
```

Test fixtures under `internal/aider/testdata/` are real transcripts, not
hand-written mocks: `chat_history.md` was generated by driving aider's own
`InputOutput` writer class, and `session.jsonl` is the structured transcript
from the Noon Curveball. `.gitattributes` disables line-ending normalisation for
that directory so the CRLF the Markdown parser must tolerate survives in git.

**Notes for reproducing on Windows:** run aider from PowerShell, not Git Bash —
aider's `prompt_toolkit` requires a real Windows console. Git Bash also rewrites
`/gh/...` arguments into Windows paths; `MSYS_NO_PATHCONV=1` avoids that.

## Databricks use, data sources and limitations

Not applicable — this project does not opt into the Databricks award.

## Known limitations and next steps

Stated plainly rather than hidden; all three are properties of aider, not
oversights:

- **No `TurnStart`.** Aider exposes no pre-prompt callback of any kind, so
  Entire cannot capture state *before* a prompt is submitted. Checkpoints are
  created at turn end only.
- **Headless runs are not captured.** Under `aider -m "..." --yes-always`,
  `ring_bell` is unreachable because aider never hits an interactive prompt, so
  the notification never fires.
- **Token counts are rounded at or above 1000 — in the Markdown format only.**
  `format_tokens` (`aider/utils.py:276`) renders `<1000` exactly, `<10000` as
  `1.2k` and the rest as `15k`, and `--llm-history-file` carries no counts at
  all, so no unrounded copy exists anywhere on disk. The JSONL format reports
  exact integers and has no such limitation.
- **One transcript per repo.** Aider is configured with a single
  `chat-history-file`, so runs append to the same file and are separated within
  it — by the `# aider chat started at ...` banner in Markdown, or by
  `session_started` in JSONL. Both are also where the session ID comes from.
- **Resume is unverified for JSONL.** `--restore-chat-history` is confirmed
  against the Markdown history (`base_coder.py:520`). Whether it accepts a JSONL
  transcript has not been checked against aider's source, so the command is
  emitted unchanged and the uncertainty is documented rather than papered over.

**Next steps toward production readiness:**

1. Recover `TurnStart` by watching the transcript from a lightweight sidecar, so
   pre-prompt state is captured without forcing users through a wrapper command.
2. Capture headless runs by having `install-hooks` also wire aider's
   `--lint-cmd`/`--test-cmd`, which *do* run in `-m` mode.
3. Upstream a feature request to aider for a real post-turn hook that carries a
   session identifier — which would remove most of the machinery here.

## Two bugs found by testing rather than by reading

Recorded because they show the value of the verification loop:

1. **`.aider.tags.cache.v4`, not `v3`.** The suffix is chosen at import time by
   whether `tree-sitter-language-pack` is installed (`repomap.py:33-43`), so two
   installs of the same aider release disagree. Found by running aider for real.
   Both versions are now protected.
2. **`json.Unmarshal` accepts `null`.** A state file containing the literal
   `null` unmarshalled into a zeroed struct, reporting `TurnIndex == 0` —
   indistinguishable from "turn 0 already checkpointed" — which would silently
   swallow the first turn of a session. Found by a unit test; fixed by
   unmarshalling through a pointer.
