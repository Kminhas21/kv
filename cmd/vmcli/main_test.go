package main

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
	"github.com/Kminhas21/kv/internal/mockhv"
)

// cli runs vmcli in-process against real kv and mockhv test servers.
func cli(t *testing.T) func(args ...string) (stdout string, err error) {
	t.Helper()
	kvSrv := httptest.NewServer(kv.NewHandler(kv.NewStore()))
	hvSrv := httptest.NewServer(mockhv.New(mockhv.Config{TaskDelay: 10 * time.Millisecond, HangFor: time.Second}).Handler())
	t.Cleanup(kvSrv.Close)
	t.Cleanup(hvSrv.Close)

	return func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		full := append([]string{"-kv", kvSrv.URL, "-hv", hvSrv.URL}, args...)
		err := run(context.Background(), full, &out, &errOut)
		return out.String(), err
	}
}

func TestCLI_KVCommands(t *testing.T) {
	vmcli := cli(t)

	if _, err := vmcli("kv", "put", "vms/a", `{"cpu":2}`); err != nil {
		t.Fatalf("kv put: %v", err)
	}
	out, err := vmcli("kv", "list", "vms/")
	if err != nil || !strings.Contains(out, "vms/a") || !strings.Contains(out, `{"cpu":2}`) {
		t.Fatalf("kv list = %q, %v; want a row for vms/a", out, err)
	}

	_, err = vmcli("kv", "put", "-if-match", "0", "vms/a", "dup")
	if !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("create-only put on existing key err = %v, want kv.ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "current version is 1") {
		t.Errorf("conflict error %q should report the current version", err)
	}

	if _, err := vmcli("kv", "put", "--ttl", "1h", "tmp", "x"); err != nil {
		t.Fatalf("kv put --ttl: %v", err)
	}
	if out, err := vmcli("kv", "get", "tmp"); err != nil || out != "x\n" {
		t.Errorf("kv get tmp after put --ttl 1h = %q, %v; want x", out, err)
	}
}

func TestCLI_FaultsChangeOnlyGivenFlags(t *testing.T) {
	vmcli := cli(t)

	if _, err := vmcli("hv", "faults", "-fail-rate", "0.5"); err != nil {
		t.Fatal(err)
	}
	out, err := vmcli("hv", "faults", "-outage")
	if err != nil {
		t.Fatal(err)
	}
	got := fields(out)
	if got["fail-rate"] != "0.5" || got["outage"] != "true" {
		t.Fatalf("faults after two partial updates:\n%s\nwant fail-rate 0.5 kept and outage true", out)
	}
}

// fields turns "name   value" lines into a map, ignoring column padding.
func fields(out string) map[string]string {
	m := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}

func TestCLI_UsageErrors(t *testing.T) {
	vmcli := cli(t)
	for _, args := range [][]string{
		{},
		{"nope"},
		{"kv", "get"},
		{"kv", "put", "k", "v", "-if-match", "3"},
		{"kv", "put", "-if-match", "abc", "k", "v"},
	} {
		_, err := vmcli(args...)
		if _, ok := errors.AsType[usageError](err); !ok {
			t.Errorf("vmcli %v: err = %v, want a usageError (exit code 2)", args, err)
		}
	}
}
