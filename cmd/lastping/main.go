// Command lastping is the LastPing CLI. Today it has one subcommand:
//
//	lastping run --monitor <uuid> -- python my_agent.py
//	LASTPING_MONITOR=<uuid> lastping run -- ./nightly.sh
//
// `run` executes the command and reports its whole lifecycle to LastPing: a
// start ping when it launches, and a terminal ping carrying the exit code and
// the tail of stderr when it finishes. See internal/runner for the reasoning
// behind exit-code propagation, TTY passthrough and signal handling — all three
// of which have to be right for this to be safe to put in front of a real job.
//
// There is no credential handling and there must not be: the ping URL is
// unauthenticated by design and the monitor id is the capability.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/tp322d/lastping-app/internal/runner"
)

const usage = `lastping — report what your commands and agents are doing.

Usage:
  lastping run [flags] -- <command> [args...]

Flags for run:
  --monitor <uuid>   monitor to report to (or set LASTPING_MONITOR)
  --ping-url <url>   ping host (or set LASTPING_PING_URL)
                     default: ` + runner.DefaultPingBase + `

Examples:
  lastping run --monitor 0e0f... -- python my_agent.py
  LASTPING_MONITOR=0e0f... lastping run -- ./nightly.sh

The exit code of the wrapped command is this command's exit code, always.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runCmd(os.Args[2:]))
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "lastping: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	// Config resolution order, both flags: the flag wins, then the environment
	// variable, then (for the ping host only) the production default. The env
	// fallback is what lets a CI job set the monitor once for a whole workflow
	// instead of threading a flag through every step.
	monitor := fs.String("monitor", "", "monitor uuid to report to")
	pingURL := fs.String("ping-url", "", "ping host base URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *monitor == "" {
		*monitor = os.Getenv("LASTPING_MONITOR")
	}
	if *pingURL == "" {
		*pingURL = os.Getenv("LASTPING_PING_URL")
	}

	// Buffered, because signal.Notify drops signals on a full channel and a
	// dropped SIGTERM is a run left open until its budget expires.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	code, err := runner.Run(runner.Options{
		MonitorID: *monitor,
		PingBase:  *pingURL,
		Argv:      fs.Args(),
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Signals:   sigs,
	})
	// runner.Run returns an error only for a usage problem it caught before
	// running anything — once the child has started there is nothing left to
	// report but an exit code.
	if err != nil {
		fmt.Fprintf(os.Stderr, "lastping: %v\n\n%s", err, usage)
		return code
	}
	return code
}
