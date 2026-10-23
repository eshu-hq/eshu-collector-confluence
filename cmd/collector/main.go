// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Command collector serves one Eshu collector SDK claim for Confluence.
//
//	collector --sdk-stdio     read one Request from stdin, write one Result
//	collector seed --dest DIR copy this binary and its manifest into DIR
//
// The seed subcommand exists so a digest-pinned image with no shell can
// populate a component home: an init container runs it, and the Eshu CLI then
// installs, enables, and grants the component.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	confluence "github.com/eshu-hq/eshu-collector-confluence"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "collector: %v\n", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	getenv func(string) string,
) error {
	if len(args) > 0 && args[0] == "seed" {
		return runSeed(args[1:], stdout, getenv)
	}
	flags := flag.NewFlagSet("collector", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sdkStdio := flags.Bool("sdk-stdio", false, "read one collector SDK host request from stdin and write one result to stdout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*sdkStdio {
		return errors.New("usage: collector --sdk-stdio | collector seed --dest DIR")
	}
	return runStdio(ctx, stdin, stdout, stderr, getenv)
}

// runStdio serves one request. The result is the only thing written to stdout;
// the health line goes to stderr and carries no credential or provider data.
func runStdio(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer, getenv func(string) string) error {
	request, err := confluence.DecodeRequest(stdin)
	if err != nil {
		return err
	}
	startedAt := time.Now()
	handled, err := confluence.Handle(ctx, request, confluence.HandleOptions{Getenv: getenv})
	if err != nil {
		return err
	}
	if line, err := confluence.HealthLine(handled, time.Since(startedAt)); err == nil {
		fmt.Fprintf(stderr, "collector health: %s\n", line)
	}
	if handled.Detail != "" {
		fmt.Fprintf(stderr, "collector detail: %s\n", handled.Detail)
	}
	// Compact output: the host caps stdout, and the result byte budget is
	// measured on the compact encoding.
	return json.NewEncoder(stdout).Encode(handled.Result)
}
