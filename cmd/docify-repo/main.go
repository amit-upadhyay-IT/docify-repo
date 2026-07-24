package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"docify-repo/internal/app/documentation/transport"
	"docify-repo/internal/config"
)

func main() {
	// When Git invokes this executable as its askpass callback, answer the credential prompt
	// before any command parsing or configuration load.
	if code, handled := transport.HandleAskpass(os.Args, os.LookupEnv, os.Stdout); handled {
		os.Exit(code)
	}
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return transport.ExitCode(err)
	}

	application := transport.New(cfg, os.Stdout, os.Stderr)
	if err := application.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return transport.ExitCode(err)
	}

	return 0
}
