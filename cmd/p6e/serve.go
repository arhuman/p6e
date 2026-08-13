package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arhuman/p6e/internal/daemon"
)

// splitServeArgs parses serve's options, hand rolled for the same reason
// splitRunArgs is: the verb comes first, which flag.FlagSet does not model.
func splitServeArgs(args []string) (dirs []string, opts daemon.Options, unknown string, err error) { //nolint:gocognit,funlen,gocyclo,dupl // A hand-rolled parser is flat by nature; the doc comment says why flag.FlagSet does not fit.
	take := func(i *int, arg string) (string, bool) {
		if _, value, found := strings.Cut(arg, "="); found {
			return value, true
		}
		if *i+1 >= len(args) {
			return "", false
		}
		*i++
		return args[*i], true
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, _, _ := strings.Cut(arg, "=")

		switch {
		case name == "--listen":
			value, ok := take(&i, arg)
			if !ok {
				return nil, opts, arg, nil
			}
			opts.Addr = value

		case name == "--admin-listen":
			value, ok := take(&i, arg)
			if !ok {
				return nil, opts, arg, nil
			}
			opts.AdminAddr = value

		// The next two cases parse into different types with different
		// validation and different messages. Factoring the shape they share
		// would leave a helper taking a parse function and an error string,
		// which is longer than the duplication it removes.
		case name == "--max-concurrency": //nolint:dupl
			value, ok := take(&i, arg)
			if !ok {
				return nil, opts, arg, nil
			}
			n, convErr := strconv.Atoi(value)
			if convErr != nil || n <= 0 {
				return nil, opts, "", fmt.Errorf("--max-concurrency wants a positive number, got %q", value)
			}
			opts.MaxConcurrency = n

		case name == "--drain": //nolint:dupl
			value, ok := take(&i, arg)
			if !ok {
				return nil, opts, arg, nil
			}
			d, convErr := time.ParseDuration(value)
			if convErr != nil || d <= 0 {
				return nil, opts, "", fmt.Errorf("--drain wants a positive duration such as \"30s\", got %q", value)
			}
			opts.DrainTimeout = d

		// Accepts a negative value, unlike --max-concurrency, because removing
		// the cap is a real choice behind something that already limits rate.
		case name == "--max-runs": //nolint:dupl
			value, ok := take(&i, arg)
			if !ok {
				return nil, opts, arg, nil
			}
			n, convErr := strconv.Atoi(value)
			if convErr != nil || n == 0 {
				return nil, opts, "", fmt.Errorf(
					"--max-runs wants a positive number, or a negative one to remove the cap, got %q", value)
			}
			opts.MaxRunsPerPipeline = n

		case strings.HasPrefix(arg, "-"):
			return nil, opts, arg, nil

		default:
			dirs = append(dirs, arg)
		}
	}
	return dirs, opts, "", nil
}

// serveCommand loads a directory and serves whatever in it can be served.
//
// A file that will not compile is reported and skipped rather than fatal: one
// typo must not stop every unrelated pipeline in the directory from answering.
// Use `p6e check --dir` when the opposite is wanted, which is what CI wants.
func serveCommand(ctx context.Context, dir string, opts daemon.Options, stdout, stderr io.Writer) int {
	loaded, err := daemon.Load(dir, registries())
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitFailure
	}
	reportLoad(loaded, stdout, stderr)

	if len(loaded.Served) == 0 {
		fmt.Fprintf(stderr, "nothing to serve: no pipeline in %s both compiles and declares a trigger\n", dir)
		return exitFailure
	}

	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	}

	// SIGTERM drains rather than kills: the runs already going get to finish,
	// which is what makes a rolling restart survivable.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.New(loaded.Served, opts).Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitFailure
	}
	return exitOK
}

// checkDirCommand compiles a whole directory and additionally reports the one
// thing no single-file check can see: two pipelines claiming one route.
//
// Unlike serve, any rejection fails the command. This is the CI gate, and the
// point of it is that a collision is discoverable before a deploy rather than
// by noticing afterwards that a webhook stopped firing.
func checkDirCommand(dir string, stdout, stderr io.Writer) int {
	loaded, err := daemon.Load(dir, registries())
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitFailure
	}
	reportLoad(loaded, stdout, stderr)

	if len(loaded.Rejected) > 0 {
		return exitFailure
	}
	fmt.Fprintf(stdout, "ok: %s (%d servable, %d to run by hand)\n",
		dir, len(loaded.Served), len(loaded.Untriggered))
	return exitOK
}

// reportLoad prints what a directory yielded, one line per pipeline, so that
// what is being served and what is not are both visible without a log level.
func reportLoad(loaded *daemon.Loaded, stdout, stderr io.Writer) {
	for _, p := range loaded.Served {
		fmt.Fprintf(stdout, "  serving   %-24s %s\n", p.Name, p.Trigger().Claim())
	}
	for _, path := range loaded.Untriggered {
		fmt.Fprintf(stdout, "  by hand   %-24s no trigger\n", nameOf(path))
	}
	for _, rejected := range loaded.Rejected {
		fmt.Fprintf(stderr, "  rejected  %-24s %v\n", nameOf(rejected.Path), rejected.Err)
	}
}
