package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

var version = "dev"

func main() {
	fmt.Println(version)
}

func run(ctx context.Context, args []string, output io.Writer, registry *source.Registry) error {
	flags := flag.NewFlagSet("agtlog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := flags.Bool("version", false, "print version")
	_ = flags.Bool("no-watch", false, "disable live session following")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(output, version)
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
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%d msgs\t%d tokens\t%s%.4f",
		session.Agent,
		session.Project,
		session.Title,
		strings.Join(session.Models, ","),
		session.Messages,
		usage.TotalTokens(),
		costPrefix,
		cost.USD,
	)
}
