# entire-agent-aider

Entire CLI support for [Aider](https://aider.chat), the terminal AI pair
programmer.

Aider has no hook or plugin system, so this adapter drives capture through the
one callback aider does expose — `--notifications-command` — configured via
`.aider.conf.yml`. Once hooks are installed, a plain `aider` run in the repo is
captured; there is no wrapper command to remember.

## Setup

```bash
cd agents/entire-agent-aider
mise run build
cp entire-agent-aider ~/.local/bin/     # or anywhere on $PATH

cd /path/to/your/repo
echo '{"external_agents": true}' > .entire/settings.local.json
entire enable -y --agent aider --telemetry=false

aider --model gpt-4o
```

`external_agents` must go in `.entire/settings.local.json`, not
`.entire/settings.json`. Entire refuses the grant from a tracked file so that a
repository cannot ship `{"external_agents": true}` next to a binary and have it
execute on clone.

`entire enable` writes a managed block into `.aider.conf.yml`:

```yaml
# >>> entire-agent-aider (managed) >>>
notifications: true
notifications-command: "echo {} | entire hooks aider turn-end"
chat-history-file: .entire/aider/chat.md
llm-history-file: .entire/aider/llm.log
input-history-file: .entire/aider/input.history
# <<< entire-agent-aider (managed) <<<
```

Anything you write outside that block is preserved, and
`entire-agent-aider uninstall-hooks` removes only the block.

The history files are redirected under `.entire/` so aider's chat log stays out
of your working tree — by default aider writes `.aider.chat.history.md` into the
repository root.

## What gets captured

| | |
|---|---|
| Checkpoint trigger | Each completed model round (turn end) |
| Transcript | Aider's own Markdown chat history, unmodified |
| Files changed | Aider's `Applied edit to <path>` lines |
| Prompts | The `#### ` blocks in the transcript |
| Tokens | Aider's own usage report — see the limitation below |
| Session ID | Timestamp of aider's `# aider chat started at ...` banner |

## Verifying it works

```bash
entire status
```

```
── Active Sessions ──────────────────────────────
Aider · 20260906-102235
> "what does Greet return for an empty name?"
started just now · tokens 19.2k
```

## Limitations

- **No pre-prompt capture.** Aider exposes no callback before a prompt is
  submitted, so checkpoints are created at turn end only.
- **Headless runs are not captured.** Under `aider -m "..." --yes-always` aider
  never reaches an interactive prompt, so the notification never fires.
- **Token counts are rounded above 1000.** Aider formats them as `1.2k` / `15k`
  before writing them down (`aider/utils.py:276`) and keeps no unrounded copy,
  so counts at or above 1000 are approximate. Cost figures are more precise.
- **One transcript per repo.** Aider is configured with a single
  `chat-history-file`, so runs append to the same file and are separated by the
  banner within it. `aider --restore-chat-history` replays the whole file.

`AGENT.md` documents the protocol mapping and every aider behaviour this adapter
relies on, with source references.

## Development

```bash
mise run build   # build the binary
mise run test    # unit tests
mise run clean   # remove the binary
```
