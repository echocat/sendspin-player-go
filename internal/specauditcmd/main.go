// Command specauditcmd checks RC1 traceability and records/report test evidence.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/echocat/sendspin-player-go/internal/specaudit"
)

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("expected check, generate, run, report or verify-spec")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", ".", "Repository root")
	suite := flags.String("suite", "", "Evidence suite to execute")
	coverage := flags.Bool("coverage", false, "Record unit coverage in var/coverage/unit.cov")
	evidence := flags.String("evidence", "", "Evidence directory (default: var/audit/evidence)")
	output := flags.String("output", "var/audit", "Generated report directory")
	profile := flags.String("profile", "catalog", "catalog, full or native evidence gate")
	goos := flags.String("goos", "", "Execution OS filter")
	arch := flags.String("goarch", "", "Execution architecture filter")
	variant := flags.String("variant", "default", "Execution variant (stable CI Go is separate from the pinned run)")
	version := flags.String("go-version", "", "Execution Go version filter, e.g. go1.27.3")
	conformance := flags.Bool("require-conformance", false, "Fail while review/mandatory evidence remains incomplete")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	project, err := specaudit.Load(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "check":
		return project.CheckGenerated()
	case "generate":
		data, err := project.RenderAudit()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(project.Root, specaudit.AuditPath), data, 0644)
	case "run":
		if err = project.CheckGenerated(); err != nil {
			return err
		}
		return project.Run(ctx, *suite, *coverage)
	case "verify-spec":
		return project.VerifySpec(ctx)
	case "report":
		if err = project.CheckGenerated(); err != nil {
			return err
		}
		report, err := project.Report(specaudit.ReportOptions{EvidenceDir: *evidence, Profile: *profile, GOOS: *goos, GOARCH: *arch, GoVersion: *version, Variant: *variant})
		if err != nil {
			return err
		}
		directory := *output
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(project.Root, directory)
		}
		if err = specaudit.WriteReport(directory, report); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[audit] report: %s; %d execution problems; %d conformance blockers\n", directory, len(report.Problems), len(report.ConformanceBlockers))
		if len(report.Problems) > 0 {
			return errors.New("execution evidence gate failed; inspect report.md")
		}
		if *conformance && len(report.ConformanceBlockers) > 0 {
			return errors.New("conformance gate blocked; passing software tests are insufficient")
		}
		return nil
	default:
		return fmt.Errorf("unknown audit command %q", args[0])
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
