# agtlog

agtlog is k9s for coding-agent logs: one keyboard-first terminal view that browses Claude Code and
Codex transcripts and rolls each session's tokens and API-equivalent cost up through nested
subagents. It reads the logs already on the machine, so no proxy, agent plugin, or API key is
required.

## Install

For a tagged release, Go users with Go 1.26.2 or newer can install the latest version:

```bash
go install github.com/motoki317/agtlog/cmd/agtlog@latest
```

Prebuilt Linux and macOS archives are attached to tagged versions on the
[GitHub releases page](https://github.com/motoki317/agtlog/releases). Nix users can run a reachable
repository revision without installing it:

```bash
nix run github:motoki317/agtlog
```

## Quick start

```bash
agtlog
```

agtlog discovers standard Claude Code and Codex log roots, opens the combined session list, and
follows new activity. Useful launch options are:

```text
--agent claude|codex  show one agent
--no-watch            keep a static snapshot
--offline             skip the background pricing refresh
--version             print the version
```

agtlog is **read-only against agent logs and configuration**. Its only writes are summary and
pricing caches in the operating system's XDG cache directory.

Codex costs have a `~` prefix. They are **API-equivalent estimates**, not subscription charges:
tokens are priced at public API rates, while ChatGPT and other plan users normally pay less.
An `!` means at least one model lacks pricing and the total is partial.

## Keys

| Screen | Keys | Action |
| --- | --- | --- |
| List | `j`/`k`, `↑`/`↓` | Move |
| List | `/` | Filter sessions |
| List | `s` | Cycle age, token, and cost sort |
| List | `a` | Cycle all, Claude, and Codex |
| List | `enter` | Open the selected session |
| List | `r` | Rediscover sessions |
| Detail | `j`/`k`, `↑`/`↓` | Move and scroll |
| Detail | `space`/`enter` | Expand or collapse |
| Detail | `J`/`K` | Jump between subagents |
| Detail | `esc`/`h` | Return to the list |
| Both | `?` | Show all keys |
| Both | `q`/`ctrl-c` | Quit |

## Development

The Nix flake provides Go 1.26 and the project tools. Commands are defined in
the [justfile](justfile):

```bash
just build
just test
just check
just leakcheck
just test-race
```

Agent and contributor guidance lives in [AGENTS.md](AGENTS.md). Architectural
decisions live in [docs/ADR/](docs/ADR/), and the interface vocabulary is recorded in
[docs/design.md](docs/design.md).

## License

[MIT](LICENSE)
