# agtlog

agtlog is a terminal UI for coding-agent logs.
It lists your local Claude Code and Codex sessions in one place and estimates
how much each session would cost at API prices.
It reads the log files already on your machine, so you need no proxy, no plugin,
and no API key.

## Install

With Go 1.26.2 or newer:

```bash
go install github.com/motoki317/agtlog/cmd/agtlog@latest
```

Prebuilt Linux and macOS binaries are on the
[releases page](https://github.com/motoki317/agtlog/releases).
With Nix, you can run it without installing:

```bash
nix run github:motoki317/agtlog
```

## Usage

```bash
agtlog
```

agtlog finds the standard Claude Code and Codex log folders, shows every session
in one list, and updates as new activity arrives. Common options:

```text
--agent claude|codex   show only one agent
--theme NAME           color theme: default, nord, or dracula
--no-watch             do not follow new activity
--offline              do not fetch fresh prices
--version              print the version
```

agtlog never writes to your agent logs or config.
Its only writes are small caches for sessions and prices, kept under your XDG
cache folder.

### About cost

Costs are estimates based on public API prices.
Codex costs start with `~`: if you pay for a ChatGPT plan, your real cost is
usually lower.
An `!` means one model has no price yet, so the shown total is partial.

## Keys

| Screen | Keys | Action |
| --- | --- | --- |
| List | `j`/`k`, `↑`/`↓` | Move |
| List | `g`/`G` | Jump to top or bottom |
| List | `/` | Filter sessions |
| List | `s` | Change sort: age, tokens, or cost |
| List | `a` | Filter by agent: all, Claude, or Codex |
| List | `enter` | Open a session |
| List | `r` | Reload sessions |
| Detail | `j`/`k`, `↑`/`↓` | Move and scroll |
| Detail | `g`/`G` | Jump to top or bottom |
| Detail | `space`/`enter` | Expand or collapse |
| Detail | `J`/`K` | Jump between subagents |
| Detail | `esc`/`h` | Back to the list |
| Both | `t` | Change color theme |
| Both | `?` | Show all keys |
| Both | `q`/`Ctrl-C` | Quit |

## Development

The Nix flake provides Go 1.26 and the project tools.
Commands live in the [justfile](justfile):

```bash
just build
just test
just check
just leakcheck
just test-race
```

Contributor guidance is in [AGENTS.md](AGENTS.md).
Design decisions are in [docs/ADR/](docs/ADR/), and the interface vocabulary is
in [docs/design.md](docs/design.md).

## License

[MIT](LICENSE)
