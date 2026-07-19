# agtlog

agtlog is a read-only browser for coding-agent logs: one backend for Claude Code
and Codex sessions, with API-equivalent cost estimates rolled up through nested
subagents. The keyboard-first terminal UI is planned for the next milestone;
the current command prints one summary line per discovered session.

## Development

The Nix flake provides Go 1.26 and the project tools. Commands are defined in
the [justfile](justfile):

```bash
just build
just test
just check
just leakcheck
```

Agent and contributor guidance lives in [AGENTS.md](AGENTS.md). Architectural
decisions live in [docs/ADR/](docs/ADR/).

## License

[MIT](LICENSE)
