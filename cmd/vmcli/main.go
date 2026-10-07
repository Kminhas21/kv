// Command vmcli drives and inspects the kv store and mock hypervisor by hand.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/Kminhas21/kv/internal/kvclient"
)

const usage = `usage: vmcli [-kv URL] [-hv URL] <command> [flags] [args]

kv store:
  kv get <key>                        print a value (version goes to stderr)
  kv put [-if-match N] <key> <value>  write a value (-if-match 0 = create only)
  kv del [-if-match N] <key>          delete a key
  kv list [prefix]                    list keys under a prefix

mock hypervisor:
  hv vms [-external-id ID]            list hypervisor VMs
  hv task <task-id>                   show a task
  hv faults [flags]                   show fault switches, or change the ones given:
      -outage=true|false  -fail-rate 0.3  -create-delay 3s  -lost-responses N
  hv kill <vm-id>                     delete a VM out-of-band (simulates drift)

Addresses default to $KV_ADDR / $HV_ADDR, else http://localhost:8080 / :8081.
Flags must come before positional arguments.
`

// usageError means the command line was wrong (exit 2), as opposed to the
// command failing at runtime (exit 1).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

type app struct {
	kv     *kvclient.Client
	hv     *hvClient
	out    io.Writer
	errOut io.Writer
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "vmcli:", err)
	if _, ok := errors.AsType[usageError](err); ok {
		fmt.Fprint(os.Stderr, "\n"+usage)
		os.Exit(2)
	}
	os.Exit(1)
}

// run is the whole program minus process concerns (exit codes, real
// stdout), so tests can call it directly.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vmcli", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kvAddr := fs.String("kv", envOr("KV_ADDR", "http://localhost:8080"), "kv base URL")
	hvAddr := fs.String("hv", envOr("HV_ADDR", "http://localhost:8081"), "mockhv base URL")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return nil
		}
		return usageError{err.Error()}
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return usageError{"missing command"}
	}
	a := &app{kv: kvclient.New(*kvAddr), hv: newHVClient(*hvAddr), out: stdout, errOut: stderr}

	switch rest[0] {
	case "kv":
		return a.kvCmd(ctx, rest[1:])
	case "hv":
		return a.hvCmd(ctx, rest[1:])
	case "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		return usageError{fmt.Sprintf("unknown command %q", rest[0])}
	}
}

// parseSub parses a subcommand's flags and checks it got between min and
// max positional arguments.
func parseSub(fs *flag.FlagSet, args []string, min, max int, synopsis string) ([]string, error) {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return nil, usageError{fmt.Sprintf("%s: %v", synopsis, err)}
	}
	if n := fs.NArg(); n < min || n > max {
		return nil, usageError{"usage: vmcli " + synopsis}
	}
	return fs.Args(), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
