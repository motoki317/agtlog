package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/codex"
	"github.com/motoki317/agtlog/internal/tui"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	err := executeWithDiagnostics(context.Background(), os.Args[1:], os.Stdout, os.Stderr, defaultRegistry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agtlog: %s\n", terminalField(err.Error(), 512))
		os.Exit(1)
	}
}

func defaultRegistry(agent string, offline bool) (*source.Registry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cacheDir := defaultCacheDir(home, os.Getenv("XDG_CACHE_HOME"))
	claudeRoots := claude.DefaultRoots(home, os.Getenv("CLAUDE_CONFIG_DIR"))
	codexRoots := codex.DefaultRoots(home, os.Getenv("CODEX_HOME"))
	cacheRoots := append(append([]string(nil), claudeRoots...), codexRoots...)
	if resolved, ok := source.ResolveCacheDir(cacheDir, cacheRoots); ok {
		cacheDir = resolved
	} else {
		cacheDir = ""
	}
	table, err := cost.RuntimeTable(cacheDir, offline)
	if err != nil {
		return nil, err
	}
	calculator := cost.NewCalculator(table)
	var adapters []source.Source
	if agent == "" || agent == string(model.AgentClaude) {
		adapters = append(adapters, claude.NewSource(
			claude.NewParser(calculator),
			claudeRoots,
		))
	}
	if agent == "" || agent == string(model.AgentCodex) {
		adapters = append(adapters, codex.NewSource(
			codex.NewParser(calculator, "gpt-5"),
			codexRoots,
		))
	}
	return source.NewRegistry(adapters, source.Options{CacheDir: cacheDir}), nil
}

func defaultCacheDir(home, xdg string) string {
	if filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "agtlog")
	}
	return filepath.Join(home, ".cache", "agtlog")
}

func run(ctx context.Context, args []string, output io.Writer, registry *source.Registry) error {
	return execute(ctx, args, output, func(string, bool) (*source.Registry, error) { return registry, nil })
}

type cliOptions struct {
	showVersion bool
	help        bool
	offline     bool
	noWatch     bool
	agent       string
}

type tuiRunner func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error

type registryFactory func(string, bool) (*source.Registry, error)

func executeTUI(ctx context.Context, options cliOptions, input io.Reader, output io.Writer, registry *source.Registry, runner tuiRunner) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sessions, err := registry.Discover(runCtx)
	if err != nil {
		return err
	}
	var follower *source.Follower
	var updates <-chan source.SessionUpdate
	if !options.noWatch {
		follower, err = registry.Follow(runCtx, source.WatchOptions{})
		if err != nil {
			return err
		}
		defer func() {
			cancel()
			_ = follower.Close()
		}()
		updates = follower.Updates()
		sessions, err = registry.Discover(runCtx)
		if err != nil {
			return err
		}
	}
	return runner(runCtx, input, output, tui.NewModelWithContext(runCtx, sessions, registry), updates)
}

func parseOptions(args []string, output io.Writer) (cliOptions, error) {
	flags := flag.NewFlagSet("agtlog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := flags.Bool("version", false, "print version")
	offline := flags.Bool("offline", false, "skip pricing refresh")
	noWatch := flags.Bool("no-watch", false, "disable live session following")
	agent := flags.String("agent", "", "limit sessions to claude or codex")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "Usage: agtlog [--offline] [--no-watch] [--agent claude|codex] [--version]")
		_, _ = fmt.Fprintln(output, "  --agent     limit sessions to claude or codex")
		_, _ = fmt.Fprintln(output, "  --no-watch  disable live session following")
		_, _ = fmt.Fprintln(output, "  --offline   skip pricing refresh")
		_, _ = fmt.Fprintln(output, "  --version   print version")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cliOptions{help: true}, nil
		}
		return cliOptions{}, err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return cliOptions{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *agent != "" && *agent != string(model.AgentClaude) && *agent != string(model.AgentCodex) {
		flags.Usage()
		return cliOptions{}, fmt.Errorf("invalid agent %q", *agent)
	}
	return cliOptions{showVersion: *showVersion, offline: *offline, noWatch: *noWatch, agent: *agent}, nil
}

func execute(ctx context.Context, args []string, output io.Writer, registryFactory registryFactory) error {
	return executeWithDiagnostics(ctx, args, output, output, registryFactory)
}

func executeWithDiagnostics(ctx context.Context, args []string, output, diagnostics io.Writer, factory registryFactory) error {
	return executeApplication(ctx, args, os.Stdin, output, diagnostics, factory, runBubbleTea)
}

func executeApplication(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, factory registryFactory, runner tuiRunner) error {
	var parsedOutput bytes.Buffer
	options, err := parseOptions(args, &parsedOutput)
	if options.help {
		_, copyErr := io.Copy(output, &parsedOutput)
		return copyErr
	}
	if err != nil {
		_, _ = io.Copy(diagnostics, &parsedOutput)
		return err
	}
	if options.showVersion {
		_, err := fmt.Fprintln(output, currentVersion())
		return err
	}
	registry, err := factory(options.agent, options.offline)
	if err != nil {
		return err
	}
	return executeTUI(ctx, options, input, output, registry, runner)
}

func currentVersion() string {
	moduleVersion := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = info.Main.Version
	}
	return resolveVersion(version, moduleVersion)
}

func resolveVersion(linked, module string) string {
	if linked != "dev" {
		return linked
	}
	if module != "" && module != "(devel)" {
		return module
	}
	return linked
}

func runBubbleTea(ctx context.Context, input io.Reader, output io.Writer, initial tui.Model, updates <-chan source.SessionUpdate) error {
	inputFD, inputIsFile := input.(interface{ Fd() uintptr })
	outputFD, outputIsFile := output.(interface{ Fd() uintptr })
	if !inputIsFile || !outputIsFile || !term.IsTerminal(int(inputFD.Fd())) || !term.IsTerminal(int(outputFD.Fd())) {
		_, err := fmt.Fprintln(output, ansi.Strip(initial.StaticView()))
		return err
	}
	program := tea.NewProgram(initial, tea.WithAltScreen(), tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output))
	done := make(chan struct{})
	var group sync.WaitGroup
	if updates != nil {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-done:
					return
				case update, ok := <-updates:
					if !ok {
						return
					}
					program.Send(update)
				}
			}
		}()
	}
	_, err := program.Run()
	close(done)
	group.Wait()
	return err
}

func terminalField(value string, maxRunes int) string {
	var sanitized strings.Builder
	count := 0
	for _, r := range value {
		if count >= maxRunes {
			break
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			r = ' '
		}
		sanitized.WriteRune(r)
		count++
	}
	return strings.Join(strings.Fields(sanitized.String()), " ")
}
