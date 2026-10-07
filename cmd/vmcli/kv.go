package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"text/tabwriter"

	"github.com/Kminhas21/kv/internal/kv"
)

// versionFlag is an optional version number. It implements flag.Value so
// "flag not given" (nil) stays distinct from "-if-match 0" (create only).
type versionFlag struct{ v *uint64 }

func (f *versionFlag) String() string {
	if f.v == nil {
		return ""
	}
	return strconv.FormatUint(*f.v, 10)
}

func (f *versionFlag) Set(s string) error {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return fmt.Errorf("must be a version number, got %q", s)
	}
	f.v = &v
	return nil
}

func (a *app) kvCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError{"kv: missing subcommand (get, put, del, list)"}
	}
	sub, args := args[0], args[1:]

	switch sub {
	case "get":
		pos, err := parseSub(flag.NewFlagSet("get", flag.ContinueOnError), args, 1, 1, "kv get <key>")
		if err != nil {
			return err
		}
		value, ver, err := a.kv.Get(ctx, pos[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, value)
		fmt.Fprintf(a.errOut, "(version %d)\n", ver)
		return nil

	case "put":
		fs := flag.NewFlagSet("put", flag.ContinueOnError)
		var ifMatch versionFlag
		fs.Var(&ifMatch, "if-match", "only write if the key is at this version (0 = create only)")
		pos, err := parseSub(fs, args, 2, 2, "kv put [-if-match N] <key> <value>")
		if err != nil {
			return err
		}
		ver, err := a.kv.Put(ctx, pos[0], pos[1], ifMatch.v)
		if errors.Is(err, kv.ErrConflict) {
			return fmt.Errorf("%w (current version is %d)", err, ver)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "ok, version %d\n", ver)
		return nil

	case "del":
		fs := flag.NewFlagSet("del", flag.ContinueOnError)
		var ifMatch versionFlag
		fs.Var(&ifMatch, "if-match", "only delete if the key is at this version")
		pos, err := parseSub(fs, args, 1, 1, "kv del [-if-match N] <key>")
		if err != nil {
			return err
		}
		if err := a.kv.Delete(ctx, pos[0], ifMatch.v); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "deleted")
		return nil

	case "list":
		pos, err := parseSub(flag.NewFlagSet("list", flag.ContinueOnError), args, 0, 1, "kv list [prefix]")
		if err != nil {
			return err
		}
		prefix := ""
		if len(pos) == 1 {
			prefix = pos[0]
		}
		items, err := a.kv.List(ctx, prefix)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tVERSION\tVALUE")
		for _, it := range items {
			fmt.Fprintf(tw, "%s\t%d\t%s\n", it.Key, it.Version, truncate(it.Value, 70))
		}
		return tw.Flush()

	default:
		return usageError{fmt.Sprintf("kv: unknown subcommand %q", sub)}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
