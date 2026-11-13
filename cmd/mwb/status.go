// cmd/mwb/status.go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bketelsen/mwb/internal/status"
)

// runStatus implements "mwb status": it prints the state of the running mwb
// as a JSON line, or with -watch, one line per change until interrupted.
func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	watch := fs.Bool("watch", false, "print a line per change until interrupted")
	_ = fs.Parse(args)

	path := status.SocketPath()
	if *watch {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := status.Watch(ctx, path, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	}

	s, err := status.Current(path)
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(s)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
