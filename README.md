# agtlog

agtlog is a terminal UI for coding-agent logs. It lists your local Claude Code and Codex sessions
in one place and estimates what each session would cost at API prices. It reads the log files that
are already on your machine, so it needs no proxy, no plugin, and no API key.

## Install

With Go 1.26.2 or later:

```bash
go install github.com/motoki317/agtlog/cmd/agtlog@latest
```

Prebuilt Linux and macOS binaries are on the
[releases page](https://github.com/motoki317/agtlog/releases). With Nix, you can run agtlog
without installing it:

```bash
nix run github:motoki317/agtlog
```

## Usage

```bash
agtlog
```

agtlog lists every session with the cost of its subagents rolled into the session row, and it
updates the list as new activity arrives. Open a session to read its timeline and its subagents.

```text
--agent claude|codex  show only one agent
--claude-dir PATH     also read this Claude home (repeatable)
--codex-dir PATH      also read this Codex home (repeatable)
--theme NAME          color theme: default, nord, or dracula
--no-watch            do not follow new activity
--offline             do not download prices
--refresh-prices      download prices before the list opens
```

An agent home is the directory that holds an agent's logs. agtlog reads `projects` under each
Claude home and `sessions` under each Codex home. The default homes are `~/.claude` and
`~/.config/claude` for Claude Code, and `~/.codex` for Codex. `CLAUDE_CONFIG_DIR` and `CODEX_HOME`
replace those defaults when they are set.

`AGTLOG_CLAUDE_DIRS` and `AGTLOG_CODEX_DIRS` list extra homes without the flags. They use the same
separator as `PATH`, and a `--claude-dir` or `--codex-dir` flag replaces the matching list.
`AGTLOG_THEME` sets the theme, and a non-empty `NO_COLOR` turns color off.

Prices come from a LiteLLM snapshot that is built into agtlog. If the price cache is missing or
older than a day, agtlog downloads the current table from `raw.githubusercontent.com` in the
background, and the new prices apply from the next launch.

agtlog never writes to agent logs or configuration. It writes only session and price caches under
`$XDG_CACHE_HOME/agtlog`, or under `~/.cache/agtlog` when `XDG_CACHE_HOME` is unset.

### Machine-readable CLI

The `list`, `show`, and `search` subcommands give scripts and coding agents the TUI's sessions as
JSON. Each session has a ref, such as `claude:<session-id>`, that `show` accepts.

```bash
# Sessions from the last 7 days whose working directory is named forge
agtlog list --project forge --since 7d
# Events that mention a phrase, with each hit's session ref and event index
agtlog search 'watcher race' --project forge --since 7d
# The full text of the event at index 12
agtlog show 'claude:0f3a9c21-4d7e-4a1b-9d2c-8e5f7a1b3c4d' --offset 12 --limit 1 --full
```

`--format text` prints plain tables instead of JSON. Subcommands download prices only when you
pass `--refresh-prices`.

[docs/cli.md](docs/cli.md) defines the commands and the JSON contract.

### About cost

A `~` marks a Codex cost that uses another model's rate, because the logged model has no published
rate of its own. A `!` means that at least one model has no price, so the total is partial. On a
subscription plan, you pay a flat fee instead of these API prices.

## Keys

| Key | Action |
| --- | --- |
| `j`/`k` or `↑`/`↓` | Move the selection |
| `enter` | Open the selected session, subagent, or event |
| `esc` or `h` | Go back |
| `/` | Filter sessions |
| `?` | Show every key for the current screen |
| `q` | Quit |

[docs/design.md](docs/design.md) explains the screens, glyphs, and colors.

## Development

The Nix dev shell provides Go and the project tools. With direnv, run `direnv allow` to load it.
[AGENTS.md](AGENTS.md) has the build, test, and commit rules, and [docs/ADR/](docs/ADR/) records the
design decisions.

## License

[MIT](LICENSE)
