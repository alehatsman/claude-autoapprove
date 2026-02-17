package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/alehatsman/claude-autoapprove/internal/debug"
	"github.com/alehatsman/claude-autoapprove/internal/wrapper"
)

func main() {
	// Parse flags
	delay := flag.Int("delay", 3, "Countdown delay in seconds before auto-approving")
	help := flag.Bool("help", false, "Show help message")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] [--] [CLAUDE_ARGS...]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "A wrapper for Claude Code that automatically approves permission prompts.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s --delay 1\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --delay 5 -- 'help me write code'\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -- --help\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nKeyboard Controls:\n")
		fmt.Fprintf(os.Stderr, "  Ctrl+A      : Toggle auto-approve on/off\n")
		fmt.Fprintf(os.Stderr, "  Ctrl+Up     : Increase countdown delay\n")
		fmt.Fprintf(os.Stderr, "  Ctrl+Down   : Decrease countdown delay\n")
		fmt.Fprintf(os.Stderr, "  Enter       : Approve immediately during countdown\n")
		fmt.Fprintf(os.Stderr, "  Any key     : Cancel countdown\n")
	}

	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	// Validate delay
	if *delay < 1 {
		fmt.Fprintf(os.Stderr, "Error: delay must be at least 1 second\n")
		os.Exit(1)
	}
	if *delay > 60 {
		fmt.Fprintf(os.Stderr, "Error: delay must be at most 60 seconds\n")
		os.Exit(1)
	}

	// Remaining args are for Claude
	claudeArgs := flag.Args()

	debug.Init()

	cfg := wrapper.Config{
		CountdownSeconds: *delay,
	}

	w := wrapper.NewWithConfig(&cfg)
	exitCode := w.Run(claudeArgs)
	os.Exit(exitCode)
}
