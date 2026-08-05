package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	machinecli "github.com/motoki317/agtlog/internal/cli"
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
	ctx, stop := newApplicationContext()
	defer stop()
	err := executeWithDiagnostics(ctx, os.Args[1:], os.Stdout, os.Stderr, defaultRegistry)
	if err != nil {
		if status, ok := machinecli.ExitStatus(err); ok {
			os.Exit(status)
		}
		fmt.Fprintf(os.Stderr, "agtlog: %s\n", terminalField(err.Error(), 512))
		os.Exit(1)
	}
}

func newApplicationContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func defaultRegistry(ctx context.Context, options cliOptions) (*source.Registry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cacheDir := defaultCacheDir(home, os.Getenv("XDG_CACHE_HOME"))
	claudeRoots := claude.DefaultRoots(home, os.Getenv("CLAUDE_CONFIG_DIR"))
	codexRoots := codex.DefaultRoots(home, os.Getenv("CODEX_HOME"))
	logRoots := append(append([]string(nil), claudeRoots...), codexRoots...)
	cacheRoots := make([]string, 0, 2*len(logRoots))
	for _, root := range logRoots {
		cacheRoots = append(cacheRoots, root, filepath.Dir(root))
	}
	if resolved, ok := source.ResolveCacheDir(cacheDir, cacheRoots); ok {
		cacheDir = resolved
	} else {
		if options.refreshPrices {
			return nil, fmt.Errorf("cannot refresh prices: cache directory could not be safely resolved outside agent log and configuration directories; set XDG_CACHE_HOME to a separate location")
		}
		cacheDir = ""
	}
	var table cost.Table
	if options.refreshPrices {
		table, err = cost.RefreshTable(ctx, cacheDir)
	} else {
		table, err = cost.RuntimeTable(cacheDir, options.offline)
	}
	if err != nil {
		return nil, err
	}
	calculator := cost.NewCalculator(table)
	var adapters []source.Source
	if options.agent == "" || options.agent == string(model.AgentClaude) {
		adapters = append(adapters, claude.NewSource(
			claude.NewParser(calculator),
			claudeRoots,
		))
	}
	if options.agent == "" || options.agent == string(model.AgentCodex) {
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

func configuredWatchRootCount(home, claudeConfig, codexHome, agent string) int {
	var roots []string
	if agent == "" || agent == string(model.AgentClaude) {
		roots = append(roots, claude.DefaultRoots(home, claudeConfig)...)
	}
	if agent == "" || agent == string(model.AgentCodex) {
		roots = append(roots, codex.DefaultRoots(home, codexHome)...)
	}
	existing := make(map[string]bool, len(roots))
	for _, root := range roots {
		info, err := os.Stat(root)
		if err == nil && info.IsDir() {
			existing[filepath.Clean(root)] = true
		}
	}
	return len(existing)
}

func run(ctx context.Context, args []string, output io.Writer, registry *source.Registry) error {
	return execute(ctx, args, output, func(context.Context, cliOptions) (*source.Registry, error) { return registry, nil })
}

type cliOptions struct {
	showVersion   bool
	help          bool
	offline       bool
	refreshPrices bool
	noWatch       bool
	agent         string
	theme         string
}

type tuiRunner func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error

type registryFactory func(context.Context, cliOptions) (*source.Registry, error)

func executeTUI(ctx context.Context, options cliOptions, input io.Reader, output io.Writer, registry *source.Registry, runner tuiRunner) error {
	theme, err := tui.ResolveTheme(options.theme)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sessions, err := registry.Discover(runCtx)
	if err != nil {
		return err
	}
	var follower *source.Follower
	var updates <-chan source.SessionUpdate
	watchingRoots := 0
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
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return homeErr
		}
		watchingRoots = configuredWatchRootCount(home, os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("CODEX_HOME"), options.agent)
		sessions, err = registry.Discover(runCtx)
		if err != nil {
			return err
		}
	}
	initial := tui.NewModelWithContextAndTheme(runCtx, sessions, registry, theme).WithWatchingRoots(watchingRoots)
	return runner(runCtx, input, output, initial, updates)
}

func parseOptions(args []string, output io.Writer) (cliOptions, error) {
	flags := flag.NewFlagSet("agtlog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := flags.Bool("version", false, "print version")
	offline := flags.Bool("offline", false, "skip pricing refresh")
	refreshPrices := flags.Bool("refresh-prices", false, "refresh cached prices before starting")
	noWatch := flags.Bool("no-watch", false, "disable live session following")
	agent := flags.String("agent", "", "limit sessions to claude or codex")
	theme := flags.String("theme", "", "color theme: default, nord, or dracula")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "Usage: agtlog [--offline] [--refresh-prices] [--no-watch] [--agent claude|codex] [--theme default|nord|dracula] [--version]")
		_, _ = fmt.Fprintln(output, "       agtlog list [flags]")
		_, _ = fmt.Fprintln(output, "       agtlog show <ref> [flags]")
		_, _ = fmt.Fprintln(output, "       agtlog search <pattern> [flags]")
		_, _ = fmt.Fprintln(output, "  --agent           limit sessions to claude or codex")
		_, _ = fmt.Fprintln(output, "  --no-watch        disable live session following")
		_, _ = fmt.Fprintln(output, "  --offline         skip pricing refresh")
		_, _ = fmt.Fprintln(output, "  --refresh-prices  refresh cached prices before starting")
		_, _ = fmt.Fprintln(output, "  --theme           color theme: default, nord, or dracula")
		_, _ = fmt.Fprintln(output, "                    precedence: --theme > AGTLOG_THEME > default; NO_COLOR forces mono")
		_, _ = fmt.Fprintln(output, "  --version         print version")
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
	if *offline && *refreshPrices {
		flags.Usage()
		return cliOptions{}, fmt.Errorf("--offline and --refresh-prices cannot be used together")
	}
	if *agent != "" && *agent != string(model.AgentClaude) && *agent != string(model.AgentCodex) {
		flags.Usage()
		return cliOptions{}, fmt.Errorf("invalid agent %q", *agent)
	}
	return cliOptions{showVersion: *showVersion, offline: *offline, refreshPrices: *refreshPrices, noWatch: *noWatch, agent: *agent, theme: *theme}, nil
}

func execute(ctx context.Context, args []string, output io.Writer, registryFactory registryFactory) error {
	return executeWithDiagnostics(ctx, args, output, output, registryFactory)
}

func executeWithDiagnostics(ctx context.Context, args []string, output, diagnostics io.Writer, factory registryFactory) error {
	return executeApplication(ctx, args, os.Stdin, output, diagnostics, factory, runBubbleTea)
}

func executeApplication(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, factory registryFactory, runner tuiRunner) error {
	if len(args) > 0 && machinecli.IsCommand(args[0]) {
		return machinecli.Execute(ctx, args, output, diagnostics, func(ctx context.Context, options machinecli.Options) (machinecli.Registry, error) {
			return factory(ctx, cliOptions{offline: options.Offline, refreshPrices: options.RefreshPrices, agent: options.Agent})
		})
	}
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
	if _, err := tui.ResolveTheme(options.theme); err != nil {
		return err
	}
	registry, err := factory(ctx, options)
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
	program := newBubbleTeaProgram(ctx, input, output, initial)
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

func newBubbleTeaProgram(ctx context.Context, input io.Reader, output io.Writer, initial tea.Model) *tea.Program {
	return tea.NewProgram(initial, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output))
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
