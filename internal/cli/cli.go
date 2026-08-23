package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

type Registry interface {
	DiscoverWithDiagnostics(context.Context) ([]*model.Session, []source.DiscoveryDiagnostic, error)
	LoadDetail(context.Context, *model.Session) error
}

type nodeDetailRegistry interface {
	LoadNodeDetail(context.Context, *model.Session) error
}

type detailReleaser interface {
	ReleaseDetail(*model.Session)
}

type commandDiagnostic struct {
	agent model.AgentKind
	path  string
	err   error
	code  string
}

func commandDiagnostics(diagnostics []source.DiscoveryDiagnostic) []commandDiagnostic {
	result := make([]commandDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		result = append(result, commandDiagnostic{
			agent: diagnostic.Agent,
			path:  diagnostic.Path,
			err:   diagnostic.Err,
			code:  "unreadable_session",
		})
	}
	return result
}

func loadNodeDetail(ctx context.Context, registry Registry, session *model.Session) error {
	if nodeRegistry, ok := registry.(nodeDetailRegistry); ok {
		return nodeRegistry.LoadNodeDetail(ctx, session)
	}
	return registry.LoadDetail(ctx, session)
}

type Options struct {
	Offline       bool
	RefreshPrices bool
	Agent         string
	ClaudeDirs    []string
	CodexDirs     []string
}

type DirList []string

func (d *DirList) String() string {
	return strings.Join(*d, string(os.PathListSeparator))
}

func (d *DirList) Set(value string) error {
	*d = append(*d, value)
	return nil
}

func ParseDirList(value string) []string {
	var dirs []string
	for _, dir := range filepath.SplitList(value) {
		if dir = strings.TrimSpace(dir); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func ResolveDirs(explicit []string, environment string) []string {
	if len(explicit) > 0 {
		return append([]string(nil), explicit...)
	}
	return ParseDirList(environment)
}

func ValidateAgentDirs(agent string, claudeDirs, codexDirs []string) error {
	if agent == "" || agent == string(model.AgentClaude) {
		if err := validateDirs("claude", claudeDirs); err != nil {
			return err
		}
	}
	if agent == "" || agent == string(model.AgentCodex) {
		if err := validateDirs("codex", codexDirs); err != nil {
			return err
		}
	}
	return nil
}

func validateDirs(agent string, dirs []string) error {
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s directory does not exist: %s", agent, dir)
		}
		if err != nil {
			return fmt.Errorf("inspect %s directory %s: %w", agent, dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s directory is not a directory: %s", agent, dir)
		}
	}
	return nil
}

type RegistryFactory func(context.Context, Options) (Registry, error)

type ExitError struct {
	Status int
	Detail ErrorDetail
}

func (e *ExitError) Error() string {
	return e.Detail.Message
}

func ExitStatus(err error) (int, bool) {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Status, true
	}
	return 0, false
}

type commonOptions struct {
	offline       bool
	refreshPrices bool
	agent         string
	format        string
	claudeDirs    DirList
	codexDirs     DirList
}

func IsCommand(value string) bool {
	return value == "list" || value == "show" || value == "search"
}

func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, factory RegistryFactory) error {
	if len(args) == 0 || !IsCommand(args[0]) {
		return errors.New("CLI command is missing")
	}
	var response any
	var format string
	var err error
	switch args[0] {
	case "list":
		response, format, err = runList(ctx, args[1:], stdout, factory)
	case "show":
		response, format, err = runShow(ctx, args[1:], stdout, factory)
	case "search":
		response, format, err = runSearch(ctx, args[1:], stdout, factory)
	}
	if err != nil {
		var exit *ExitError
		if !errors.As(err, &exit) {
			exit = runtimeError("internal", err.Error())
		}
		if writeErr := writeJSON(stderr, ErrorResponse{SchemaVersion: SchemaVersion, Error: exit.Detail}); writeErr != nil {
			return writeErr
		}
		return exit
	}
	if response == nil {
		return nil
	}
	if format == "text" {
		return writeText(stdout, response)
	}
	return writeJSON(stdout, response)
}

func addCommonFlags(flags *flag.FlagSet, options *commonOptions) {
	flags.BoolVar(&options.offline, "offline", false, "skip pricing refresh")
	flags.BoolVar(&options.refreshPrices, "refresh-prices", false, "refresh cached prices before running")
	flags.StringVar(&options.agent, "agent", "", "limit sessions to claude or codex")
	flags.StringVar(&options.format, "format", "json", "output format: json or text")
	flags.Var(&options.claudeDirs, "claude-dir", "additional Claude home (repeatable, overrides AGTLOG_CLAUDE_DIRS path list)")
	flags.Var(&options.codexDirs, "codex-dir", "additional Codex home (repeatable, overrides AGTLOG_CODEX_DIRS path list)")
}

func (options commonOptions) validate() error {
	if options.offline && options.refreshPrices {
		return usageError("--offline and --refresh-prices cannot be used together")
	}
	if options.agent != "" && options.agent != string(model.AgentClaude) && options.agent != string(model.AgentCodex) {
		return usageError(fmt.Sprintf("invalid agent %q", options.agent))
	}
	claudeDirs := ResolveDirs(options.claudeDirs, os.Getenv("AGTLOG_CLAUDE_DIRS"))
	codexDirs := ResolveDirs(options.codexDirs, os.Getenv("AGTLOG_CODEX_DIRS"))
	if err := ValidateAgentDirs(options.agent, claudeDirs, codexDirs); err != nil {
		return usageError(err.Error())
	}
	if options.format != "json" && options.format != "text" {
		return usageError(fmt.Sprintf("invalid format %q", options.format))
	}
	return nil
}

func (options commonOptions) registryOptions() Options {
	return Options{
		Offline:       !options.refreshPrices,
		RefreshPrices: options.refreshPrices,
		Agent:         options.agent,
		ClaudeDirs:    append([]string(nil), options.claudeDirs...),
		CodexDirs:     append([]string(nil), options.codexDirs...),
	}
}

func parseFlexible(flags *flag.FlagSet, args []string, operands int) ([]string, error) {
	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0, operands)
	parsing := true
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if parsing && argument == "--" {
			parsing = false
			continue
		}
		if parsing && (argument == "-h" || argument == "--help") {
			flags.Usage()
			return nil, flag.ErrHelp
		}
		if parsing && strings.HasPrefix(argument, "-") && argument != "-" {
			flagArgs = append(flagArgs, argument)
			name, hasValue := flagName(argument)
			value := flags.Lookup(name)
			if value != nil && !hasValue && !isBoolFlag(value.Value) {
				if index+1 >= len(args) {
					return nil, usageError(fmt.Sprintf("flag needs an argument: -%s", name))
				}
				index++
				flagArgs = append(flagArgs, args[index])
			}
			continue
		}
		positionals = append(positionals, argument)
	}
	usage := flags.Usage
	flags.Usage = func() {}
	err := flags.Parse(flagArgs)
	flags.Usage = usage
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, flag.ErrHelp
		}
		return nil, usageError(err.Error())
	}
	if len(positionals) != operands {
		if len(positionals) < operands {
			return nil, usageError("missing operand")
		}
		return nil, usageError(fmt.Sprintf("unexpected argument %q", positionals[operands]))
	}
	return positionals, nil
}

func flagName(argument string) (string, bool) {
	name := strings.TrimLeft(argument, "-")
	if index := strings.IndexByte(name, '='); index >= 0 {
		return name[:index], true
	}
	return name, false
}

func isBoolFlag(value flag.Value) bool {
	getter, ok := value.(interface{ IsBoolFlag() bool })
	return ok && getter.IsBoolFlag()
}

func newFlagSet(name string, output io.Writer, usage func(io.Writer)) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		usage(output)
		flags.SetOutput(output)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	return flags
}

func parseTimeFilter(value string, now time.Time, location *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation(time.DateOnly, value, location); err == nil {
		return parsed, nil
	}
	duration, err := relativeDuration(value)
	if err != nil {
		return time.Time{}, usageError(fmt.Sprintf("invalid time %q", value))
	}
	return now.Add(-duration), nil
}

func relativeDuration(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil || days <= 0 {
			return 0, errors.New("invalid duration")
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errors.New("invalid duration")
	}
	return duration, nil
}

func cwdContains(root, candidate string) bool {
	if candidate == "" {
		return false
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func discoveryWarnings(diagnostics []commandDiagnostic) []Warning {
	warnings := make([]Warning, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		code := diagnostic.code
		verb := "read"
		if code == "unaddressable_session" {
			verb = "address"
		}
		warnings = append(warnings, Warning{
			Code:    code,
			Message: fmt.Sprintf("could not %s %s session: %v", verb, diagnostic.agent, diagnostic.err),
			Path:    diagnostic.path,
		})
	}
	return nonNil(warnings)
}

func usageError(message string) *ExitError {
	return &ExitError{Status: 2, Detail: ErrorDetail{Code: "usage", Message: message}}
}

func runtimeError(code, message string) *ExitError {
	return &ExitError{Status: 1, Detail: ErrorDetail{Code: code, Message: message}}
}

func resolutionError(code, message string, candidates []ErrorCandidate) *ExitError {
	return &ExitError{Status: 3, Detail: ErrorDetail{Code: code, Message: message, Candidates: candidates}}
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
