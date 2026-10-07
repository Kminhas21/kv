# 07: vmcli skeleton + kv and hv commands

**What to build:** A CLI for poking the system by hand: `vmcli kv get|put|del|list`, `vmcli hv vms|faults [flags]|kill <id>`. Addresses come from flags/env with local defaults, so the same CLI works against `go run` processes or port-forwarded pods.

**Blocked by:** 05, 06

**Status:** resolved

- [x] Subcommand dispatch with stdlib `flag.NewFlagSet` per subcommand
- [x] kv commands use the ticket-05 client
- [x] hv commands show inventory as a readable table and set fault switches
- [x] Non-zero exit code + message on error

**Concepts (walkthrough):** `os.Args` dispatch, `flag.FlagSet`, `text/tabwriter`, exit codes, why this is a thin client over HTTP (same as `kubectl` over the apiserver).

**Manual check:** with kv + mockhv running: `vmcli kv put a 1`, `vmcli kv list`, `vmcli hv faults --create-delay 3s`, `vmcli hv vms`.
