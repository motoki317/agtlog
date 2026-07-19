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
	"strings"
	"unicode"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/codex"
)

var version = "dev"

func main() {
	err := executeWithDiagnostics(context.Background(), os.Args[1:], os.Stdout, os.Stderr, defaultRegistry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agtlog: %s\n", terminalField(err.Error(), 512))
		os.Exit(1)
	}
}

func defaultRegistry() (*source.Registry, error) {
	table, err := cost.EmbeddedTable()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	calculator := cost.NewCalculator(table)
	adapters := []source.Source{
		claude.NewSource(
			claude.NewParser(calculator),
			claude.DefaultRoots(home, os.Getenv("CLAUDE_CONFIG_DIR")),
		),
		codex.NewSource(
			codex.NewParser(calculator, "gpt-5"),
			codex.DefaultRoots(home, os.Getenv("CODEX_HOME")),
		),
	}
	cacheDir := ""
	if userCacheDir, err := os.UserCacheDir(); err == nil {
		cacheDir = filepath.Join(userCacheDir, "agtlog")
	}
	return source.NewRegistry(adapters, source.Options{CacheDir: cacheDir}), nil
}

func run(ctx context.Context, args []string, output io.Writer, registry *source.Registry) error {
	return execute(ctx, args, output, func() (*source.Registry, error) { return registry, nil })
}

type cliOptions struct {
	showVersion bool
	help        bool
}

func parseOptions(args []string, output io.Writer) (cliOptions, error) {
	flags := flag.NewFlagSet("agtlog", flag.ContinueOnError)
	flags.SetOutput(output)
	showVersion := flags.Bool("version", false, "print version")
	_ = flags.Bool("no-watch", false, "disable live session following")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "Usage: agtlog [--no-watch] [--version]")
		_, _ = fmt.Fprintln(output, "  --no-watch  disable live session following")
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
	return cliOptions{showVersion: *showVersion}, nil
}

func execute(ctx context.Context, args []string, output io.Writer, registryFactory func() (*source.Registry, error)) error {
	return executeWithDiagnostics(ctx, args, output, output, registryFactory)
}

func executeWithDiagnostics(ctx context.Context, args []string, output, diagnostics io.Writer, registryFactory func() (*source.Registry, error)) error {
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
		_, err := fmt.Fprintln(output, version)
		return err
	}
	registry, err := registryFactory()
	if err != nil {
		return err
	}
	sessions, err := registry.Discover(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if _, err := fmt.Fprintln(output, formatSession(session)); err != nil {
			return err
		}
	}
	return nil
}

func formatSession(session *model.Session) string {
	usage := session.TotalUsage()
	cost := session.TotalCost()
	costPrefix := "$"
	if cost.Estimated {
		costPrefix = "~$"
	}
	costField := fmt.Sprintf("%s%.4f", costPrefix, cost.USD)
	if len(cost.MissingPricingModels) > 0 {
		costField = costPrefix + "unpriced(" + terminalField(strings.Join(cost.MissingPricingModels, ","), 200) + ")"
	}
	models := make([]string, 0, len(session.Models))
	for _, name := range session.Models {
		models = append(models, terminalField(name, 200))
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%d msgs\t%d tokens\t%s",
		terminalField(string(session.Agent), 32),
		terminalField(session.Project, 200),
		terminalField(session.Title, 200),
		strings.Join(models, ","),
		session.Messages,
		usage.TotalTokens(),
		costField,
	)
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
