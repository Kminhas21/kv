package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Kminhas21/kv/internal/vm"
)

func (a *app) vmCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError{"vm: missing subcommand (create, list, get)"}
	}
	sub, args := args[0], args[1:]

	switch sub {
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		var spec vm.Spec
		fs.StringVar(&spec.Name, "name", "", "VM name (a DNS label)")
		fs.IntVar(&spec.CPU, "cpu", 1, "vCPUs")
		fs.IntVar(&spec.MemoryMB, "mem", 1024, "memory in MB")
		key := fs.String("key", "", "idempotency key (default: a new random key)")
		if _, err := parseSub(fs, args, 0, 0, "vm create -name N -cpu C -mem MB [-key K]"); err != nil {
			return err
		}
		if *key == "" {
			*key = randomKey()
		}
		var res vm.CreateResult
		err := a.vmctl.callWithHeaders(ctx, http.MethodPost, "/vms", spec, &res,
			map[string]string{"Idempotency-Key": *key})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "vm %s requested (operation %s)\n", res.VMID, res.OperationID)
		fmt.Fprintf(a.errOut, "idempotency key: %s  (re-run with -key %s to retry safely)\n", *key, *key)
		return nil

	case "list":
		if _, err := parseSub(flag.NewFlagSet("list", flag.ContinueOnError), args, 0, 0, "vm list"); err != nil {
			return err
		}
		var vms []vm.VM
		if err := a.vmctl.call(ctx, http.MethodGet, "/vms", nil, &vms); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tCPU\tMEM(MB)\tPHASE\tDESIRED\tOBSERVED\tHOST")
		for _, v := range vms {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
				v.ID, v.Name, v.CPU, v.MemoryMB, v.Phase, v.DesiredPower, v.ObservedPower, dash(v.HostID))
		}
		return tw.Flush()

	case "get":
		pos, err := parseSub(flag.NewFlagSet("get", flag.ContinueOnError), args, 1, 1, "vm get <vm-id>")
		if err != nil {
			return err
		}
		var v vm.VM
		if err := a.vmctl.call(ctx, http.MethodGet, "/vms/"+url.PathEscape(pos[0]), nil, &v); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
		row := func(k, v string) { fmt.Fprintf(tw, "%s:\t%s\n", k, v) }
		row("id", v.ID)
		row("name", v.Name)
		row("spec", fmt.Sprintf("%d vCPU, %d MB", v.CPU, v.MemoryMB))
		row("phase", string(v.Phase))
		row("desired power", string(v.DesiredPower))
		row("observed power", string(v.ObservedPower))
		row("last observed", when(v.LastObservedAt))
		row("host", dash(v.HostID))
		row("external id", v.ExternalID)
		row("provider vm", dash(v.ProviderVMID))
		row("current op", dash(v.CurrentOpID))
		row("last error", dash(v.LastError))
		row("created", when(v.CreatedAt))
		return tw.Flush()

	case "watch":
		fs := flag.NewFlagSet("watch", flag.ContinueOnError)
		until := fs.String("until", "", "exit 0 once the VM reaches this phase (e.g. Running)")
		timeout := fs.Duration("timeout", 0, "give up after this long (exit 1)")
		pos, err := parseSub(fs, args, 1, 1, "vm watch [-until PHASE] [-timeout D] <vm-id>")
		if err != nil {
			return err
		}
		if *timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *timeout)
			defer cancel()
		}
		return a.watchVM(ctx, pos[0], vm.Phase(*until))

	default:
		return usageError{fmt.Sprintf("vm: unknown subcommand %q", sub)}
	}
}

// watchVM polls a VM and prints one line whenever anything visible changes,
// until ctx ends (Ctrl+C or -timeout) or the VM reaches the until phase.
func (a *app) watchVM(ctx context.Context, id string, until vm.Phase) error {
	start := time.Now()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	fmt.Fprintf(a.errOut, "watching %s (Ctrl+C to stop)\n", id)

	last := ""
	for {
		var v vm.VM
		if err := a.vmctl.call(ctx, http.MethodGet, "/vms/"+url.PathEscape(id), nil, &v); err != nil {
			if ctx.Err() != nil {
				return a.watchEnded(ctx, until)
			}
			return err
		}
		opStatus := "-"
		if v.CurrentOpID != "" {
			var op vm.Operation
			if a.vmctl.call(ctx, http.MethodGet, "/operations/"+url.PathEscape(v.CurrentOpID), nil, &op) == nil {
				opStatus = fmt.Sprintf("%s %s (attempt %d)", op.Type, op.Status, op.Attempts)
			}
		}
		line := fmt.Sprintf("phase=%-12s desired=%-3s observed=%-7s host=%-6s provider=%-7s op=%s",
			v.Phase, v.DesiredPower, v.ObservedPower, dash(v.HostID), dash(v.ProviderVMID), opStatus)
		if v.LastError != "" {
			line += "  error=" + truncate(v.LastError, 60)
		}
		if line != last {
			fmt.Fprintf(a.out, "+%5.1fs  %s\n", time.Since(start).Seconds(), line)
			last = line
		}
		if until != "" && v.Phase == until {
			return nil
		}

		select {
		case <-ctx.Done():
			return a.watchEnded(ctx, until)
		case <-ticker.C:
		}
	}
}

func (a *app) watchEnded(ctx context.Context, until vm.Phase) error {
	if until != "" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out before reaching phase %s", until)
	}
	return nil
}

func (a *app) hostsCmd(ctx context.Context, args []string) error {
	if _, err := parseSub(flag.NewFlagSet("hosts", flag.ContinueOnError), args, 0, 0, "hosts"); err != nil {
		return err
	}
	var hosts []vm.HostView
	if err := a.vmctl.call(ctx, http.MethodGet, "/hosts", nil, &hosts); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tCPU USED/TOTAL\tMEM(MB) USED/TOTAL\tVMS")
	for _, h := range hosts {
		vms := slices.Sorted(maps.Keys(h.Reservations))
		fmt.Fprintf(tw, "%s\t%d/%d\t%d/%d\t%s\n", h.ID,
			h.CPUTotal-h.CPUFree, h.CPUTotal, h.MemoryMBTotal-h.MemoryMBFree, h.MemoryMBTotal,
			dash(strings.Join(vms, ",")))
	}
	return tw.Flush()
}

func (a *app) opCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "get" {
		return usageError{"usage: vmcli op get <op-id>"}
	}
	pos, err := parseSub(flag.NewFlagSet("get", flag.ContinueOnError), args[1:], 1, 1, "op get <op-id>")
	if err != nil {
		return err
	}
	var op vm.Operation
	if err := a.vmctl.call(ctx, http.MethodGet, "/operations/"+url.PathEscape(pos[0]), nil, &op); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s  %s %s  vm=%s  attempts=%d  task=%s",
		op.ID, op.Type, op.Status, op.VMID, op.Attempts, dash(op.ProviderTaskID))
	if op.LastError != "" {
		fmt.Fprintf(a.out, "  error=%q", op.LastError)
	}
	fmt.Fprintln(a.out)
	return nil
}

func randomKey() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func when(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%s (%s ago)", t.Local().Format(time.TimeOnly), time.Since(t).Round(time.Second))
}
