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
tree.

**4. Semantic diff of the submitted implementation.**

```
entire graph diff <base> HEAD
```

*(to be recorded against the final commit)*

## Noon Curveball: what changed and how we adapted

*(to be completed at 12:00)*

## Checkpoint links and what each checkpoint proves

| Milestone | Checkpoint | What it proves |
|---|---|---|
| Initial understanding and intended architecture | *(pending)* | Why aider is the hard case, and the config-file-over-wrapper decision |
| Last stable state before the Noon Curveball | *(pending)* | Working end-to-end capture with tests green |
| Response to the Noon Curveball | *(pending)* | |
| Final implementation and verification | *(pending)* | |

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
go test ./...                 # 65 cases
go test -cover ./internal/aider/   # 92.1% of statements
```

Test fixtures under `internal/aider/testdata/` were generated by driving aider's
own `InputOutput` writer class, so they are byte-for-byte what aider produces
rather than hand-written mocks. `.gitattributes` disables line-ending
normalisation so the CRLF the parser must tolerate survives in git.

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
- **Token counts are rounded at or above 1000.** `format_tokens`
  (`aider/utils.py:276`) renders `<1000` exactly, `<10000` as `1.2k` and the
  rest as `15k`, and `--llm-history-file` carries no counts at all — so no
  unrounded copy exists anywhere on disk. Cost figures are more precise.
- **One transcript per repo.** Aider is configured with a single
  `chat-history-file`, so runs append to the same file and are separated by the
  `# aider chat started at ...` banner within it, which is also where the
  session ID comes from.

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
