package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Kminhas21/kv/internal/mockhv"
)

type hvClient struct {
	base string
	http *http.Client
}

func newHVClient(base string) *hvClient {
	return &hvClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 5 * time.Second}}
}

// call sends body (if non-nil) as JSON and decodes a JSON response into out
// (if non-nil). Any status >= 300 becomes an error carrying the body text.
func (c *hvClient) call(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s: mockhv returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *app) hvCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError{"hv: missing subcommand (vms, task, faults, kill)"}
	}
	sub, args := args[0], args[1:]

	switch sub {
	case "vms":
		fs := flag.NewFlagSet("vms", flag.ContinueOnError)
		externalID := fs.String("external-id", "", "only VMs with this external ID")
		if _, err := parseSub(fs, args, 0, 0, "hv vms [-external-id ID]"); err != nil {
			return err
		}
		var vms []mockhv.VM
		if err := a.hv.call(ctx, "GET", "/vms?external_id="+url.QueryEscape(*externalID), nil, &vms); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tEXTERNAL-ID\tNAME\tHOST\tCPU\tMEM(MB)\tSTATUS\tPOWER")
		for _, vm := range vms {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
				vm.ID, vm.ExternalID, vm.Name, vm.HostID, vm.CPU, vm.MemoryMB, vm.Status, vm.Power)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(a.errOut, "(%d VMs)\n", len(vms))
		return nil

	case "task":
		pos, err := parseSub(flag.NewFlagSet("task", flag.ContinueOnError), args, 1, 1, "hv task <task-id>")
		if err != nil {
			return err
		}
		var t mockhv.Task
		if err := a.hv.call(ctx, "GET", "/tasks/"+url.PathEscape(pos[0]), nil, &t); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s  kind=%s  vm=%s  state=%s", t.ID, t.Kind, t.VMID, t.State)
		if t.Error != "" {
			fmt.Fprintf(a.out, "  error=%q", t.Error)
		}
		fmt.Fprintln(a.out)
		return nil

	case "faults":
		return a.hvFaults(ctx, args)

	case "kill":
		pos, err := parseSub(flag.NewFlagSet("kill", flag.ContinueOnError), args, 1, 1, "hv kill <vm-id>")
		if err != nil {
			return err
		}
		if err := a.hv.call(ctx, "DELETE", "/vms/"+url.PathEscape(pos[0])+"?out_of_band=true", nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s deleted out-of-band (the control plane was not told)\n", pos[0])
		return nil

	default:
		return usageError{fmt.Sprintf("hv: unknown subcommand %q", sub)}
	}
}

// hvFaults shows the fault switches, or changes only the flags given.
func (a *app) hvFaults(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("faults", flag.ContinueOnError)
	outage := fs.Bool("outage", false, "every API call returns 503")
	failRate := fs.Float64("fail-rate", 0, "probability 0..1 that a task fails")
	createDelay := fs.Duration("create-delay", 0, "extra time added to every create task")
	lost := fs.Int("lost-responses", 0, "next N creates succeed but answer too late")
	if _, err := parseSub(fs, args, 0, 0, "hv faults [-outage=true|false] [-fail-rate F] [-create-delay D] [-lost-responses N]"); err != nil {
		return err
	}

	// Visit walks only the flags that were actually set, so unset ones are
	// left out of the update and keep their current value on the server.
	changes := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "outage":
			changes["outage"] = *outage
		case "fail-rate":
			changes["fail_rate"] = *failRate
		case "create-delay":
			changes["create_delay_ms"] = createDelay.Milliseconds()
		case "lost-responses":
			changes["timeout_after_success_count"] = *lost
		}
	})

	var f mockhv.Faults
	if len(changes) == 0 {
		if err := a.hv.call(ctx, "GET", "/faults", nil, &f); err != nil {
			return err
		}
	} else if err := a.hv.call(ctx, "PUT", "/faults", changes, &f); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "outage\t%v\n", f.Outage)
	fmt.Fprintf(tw, "fail-rate\t%v\n", f.FailRate)
	fmt.Fprintf(tw, "create-delay\t%v\n", time.Duration(f.CreateDelayMS)*time.Millisecond)
	fmt.Fprintf(tw, "lost-responses\t%d remaining\n", f.TimeoutAfterSuccessCount)
	return tw.Flush()
}
