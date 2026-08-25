// Command reasonix-blackboard is the thin JSON gateway to the team
// blackboard store (route P6.1): one request on stdin, one response on
// stdout, business rejections encoded in the response, non-zero exit only
// when the request could not be served at all. It is a subprocess contract
// for Python and shell callers — no daemon, no RPC — and carries no
// business logic of its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"reasonix/internal/team"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reasonix-blackboard", flag.ContinueOnError)
	dbPath := fs.String("db", os.Getenv("REASONIX_BOARD_DB"), "path to the board SQLite database (or REASONIX_BOARD_DB)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(stderr, "reasonix-blackboard: -db or REASONIX_BOARD_DB is required")
		return 2
	}
	in, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "reasonix-blackboard: read stdin: %v\n", err)
		return 1
	}
	ctx := context.Background()
	store, err := team.NewSQLiteStore(ctx, *dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "reasonix-blackboard: open store: %v\n", err)
		return 1
	}
	defer store.Close()
	// Bindings persist in board_bindings (route §4.3): every record change
	// writes through the store, and the previous process's state is restored
	// on startup, so bind/unbind survive the per-request process model.
	registry := team.NewBindingRegistryWithPersister(store)
	records, err := store.LoadBindings(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "reasonix-blackboard: load bindings: %v\n", err)
		return 1
	}
	registry.Restore(records)
	out, err := Handle(ctx, store, registry, in)
	if err != nil {
		fmt.Fprintf(stderr, "reasonix-blackboard: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(out); err != nil {
		fmt.Fprintf(stderr, "reasonix-blackboard: write response: %v\n", err)
		return 1
	}
	if _, err := io.WriteString(stdout, "\n"); err != nil {
		fmt.Fprintf(stderr, "reasonix-blackboard: write response: %v\n", err)
		return 1
	}
	return 0
}
