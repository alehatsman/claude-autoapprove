package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"gopkg.in/yaml.v3"
)

// State represents the current state of the wrapper
type State int

const (
	StateRunning State = iota
	StateWaitingForPrompt
	StateIdleNudge
	StateManualMode
)

func (s State) String() string {
	switch s {
	case StateRunning:
		return "RUNNING"
	case StateWaitingForPrompt:
		return "WAITING_FOR_PROMPT"
	case StateIdleNudge:
		return "IDLE_NUDGE"
	case StateManualMode:
		return "MANUAL_MODE"
	default:
		return "UNKNOWN"
	}
}

// PromptRule defines a regex pattern and response
type PromptRule struct {
	MatchRegex  string        `yaml:"match_regex"`
	Send        string        `yaml:"send"`
	Description string        `yaml:"description"`
	Cooldown    time.Duration `yaml:"cooldown"`
	regex       *regexp.Regexp
	lastSent    time.Time
}

// Config holds the wrapper configuration
type Config struct {
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	MinSendInterval   time.Duration `yaml:"min_send_interval"`
	NudgeInitialDelay time.Duration `yaml:"nudge_initial_delay"`
	NudgeMaxRetries   int           `yaml:"nudge_max_retries"`
	PromptRules       []PromptRule  `yaml:"prompt_rules"`
}

// DangerousPatterns that should trigger manual mode
var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`rm\s+-rf\s+/`),
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bdd\s+if=`),
	regexp.MustCompile(`\bshutdown\b`),
	regexp.MustCompile(`\breboot\b`),
	regexp.MustCompile(`:\(\)\{:\|:&\};:`),
	regexp.MustCompile(`curl[^|]*\|\s*sh`),
	regexp.MustCompile(`wget[^|]*\|\s*sh`),
	regexp.MustCompile(`/etc/sudoers`),
	regexp.MustCompile(`chmod\s+777\s+/`),
	regexp.MustCompile(`chown\s+.*\s+/`),
	regexp.MustCompile(`\bformat\s+[Cc]:`),
	regexp.MustCompile(`git\s+push\s+--force`),
	regexp.MustCompile(`docker\s+rm\s+-f.*-v`),
}

// DefaultConfig returns sensible defaults
func DefaultConfig() Config {
	return Config{
		IdleTimeout:       15 * time.Second,
		MinSendInterval:   500 * time.Millisecond,
		NudgeInitialDelay: 2 * time.Second,
		NudgeMaxRetries:   3,
		PromptRules: []PromptRule{
			{
				MatchRegex:  `(?i)(continue|proceed)\s*\?`,
				Send:        "y\n",
				Description: "Continue/Proceed prompt",
				Cooldown:    2 * time.Second,
			},
			{
				MatchRegex:  `(?i)\[y/n\]`,
				Send:        "y\n",
				Description: "Yes/No prompt",
				Cooldown:    2 * time.Second,
			},
			{
				MatchRegex:  `(?i)press\s+(enter|return)`,
				Send:        "\n",
				Description: "Press Enter prompt",
				Cooldown:    1 * time.Second,
			},
			{
				MatchRegex:  `(?i)approve\s+this`,
				Send:        "y\n",
				Description: "Approval request",
				Cooldown:    2 * time.Second,
			},
			{
				MatchRegex:  `(?i)do\s+you\s+want\s+to`,
				Send:        "yes\n",
				Description: "Want to... question",
				Cooldown:    2 * time.Second,
			},
		},
	}
}

// Wrapper manages the PTY and auto-approval logic
type Wrapper struct {
	config         Config
	state          State
	stateMu        sync.RWMutex
	lastOutput     time.Time
	lastOutputMu   sync.RWMutex
	lastSend       time.Time
	lastSendMu     sync.RWMutex
	outputBuffer   *CircularBuffer
	ptmx           *os.File
	nudgeCount     int
	structuredLog  *log.Logger
}

// CircularBuffer keeps recent output for pattern matching
type CircularBuffer struct {
	data []byte
	size int
	mu   sync.Mutex
}

func NewCircularBuffer(size int) *CircularBuffer {
	return &CircularBuffer{
		data: make([]byte, 0, size),
		size: size,
	}
}

func (cb *CircularBuffer) Write(p []byte) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.data = append(cb.data, p...)
	if len(cb.data) > cb.size {
		cb.data = cb.data[len(cb.data)-cb.size:]
	}
}

func (cb *CircularBuffer) GetContent() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return string(cb.data)
}

// NewWrapper creates a new wrapper instance
func NewWrapper(config Config) *Wrapper {
	// Compile regex patterns
	for i := range config.PromptRules {
		re, err := regexp.Compile(config.PromptRules[i].MatchRegex)
		if err != nil {
			log.Fatalf("Invalid regex pattern '%s': %v", config.PromptRules[i].MatchRegex, err)
		}
		config.PromptRules[i].regex = re
	}

	return &Wrapper{
		config:        config,
		state:         StateRunning,
		lastOutput:    time.Now(),
		lastSend:      time.Time{},
		outputBuffer:  NewCircularBuffer(4096),
		structuredLog: log.New(os.Stderr, "[AUTOAPPROVE] ", log.Ldate|log.Ltime|log.Lmicroseconds),
	}
}

func (w *Wrapper) setState(s State) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.state != s {
		w.structuredLog.Printf("STATE_TRANSITION: %s -> %s", w.state, s)
		w.state = s
	}
}

func (w *Wrapper) getState() State {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.state
}

func (w *Wrapper) updateLastOutput() {
	w.lastOutputMu.Lock()
	defer w.lastOutputMu.Unlock()
	w.lastOutput = time.Now()
}

func (w *Wrapper) getTimeSinceLastOutput() time.Duration {
	w.lastOutputMu.RLock()
	defer w.lastOutputMu.RUnlock()
	return time.Since(w.lastOutput)
}

func (w *Wrapper) canSend() bool {
	w.lastSendMu.RLock()
	defer w.lastSendMu.RUnlock()
	return time.Since(w.lastSend) >= w.config.MinSendInterval
}

func (w *Wrapper) recordSend() {
	w.lastSendMu.Lock()
	defer w.lastSendMu.Unlock()
	w.lastSend = time.Now()
}

// checkDangerous scans output for dangerous patterns
func (w *Wrapper) checkDangerous(text string) bool {
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(text) {
			w.structuredLog.Printf("DANGER_DETECTED: pattern='%s' matched in output", pattern.String())
			return true
		}
	}
	return false
}

// checkPrompts scans for known prompt patterns
func (w *Wrapper) checkPrompts(text string) *PromptRule {
	now := time.Now()
	for i := range w.config.PromptRules {
		rule := &w.config.PromptRules[i]
		if rule.regex.MatchString(text) {
			// Check cooldown
			if !rule.lastSent.IsZero() && now.Sub(rule.lastSent) < rule.Cooldown {
				continue
			}
			return rule
		}
	}
	return nil
}

// sendToChild sends input to the PTY
func (w *Wrapper) sendToChild(input string, reason string) error {
	if !w.canSend() {
		return fmt.Errorf("rate limit: too soon since last send")
	}

	w.structuredLog.Printf("SEND_INPUT: reason='%s' input=%q", reason, input)
	_, err := w.ptmx.Write([]byte(input))
	if err != nil {
		return err
	}

	w.recordSend()
	return nil
}

// handlePrompt responds to a detected prompt
func (w *Wrapper) handlePrompt(rule *PromptRule) {
	err := w.sendToChild(rule.Send, rule.Description)
	if err != nil {
		w.structuredLog.Printf("ERROR_SEND: %v", err)
		return
	}
	rule.lastSent = time.Now()
	w.setState(StateRunning)
	w.nudgeCount = 0
}

// handleIdleTimeout performs nudge sequence
func (w *Wrapper) handleIdleTimeout() {
	if w.nudgeCount >= w.config.NudgeMaxRetries {
		w.structuredLog.Printf("IDLE_TIMEOUT: max nudges reached, switching to manual mode")
		w.setState(StateManualMode)
		fmt.Fprintf(os.Stderr, "\n\n⚠️  AUTO-APPROVE DISABLED: Too many idle timeouts. Switching to manual mode.\n\n")
		return
	}

	w.setState(StateIdleNudge)
	w.nudgeCount++

	w.structuredLog.Printf("IDLE_NUDGE: attempt=%d/%d", w.nudgeCount, w.config.NudgeMaxRetries)

	// Nudge sequence: newline -> y -> continue
	nudgeSequence := []struct {
		input string
		delay time.Duration
	}{
		{"\n", 1 * time.Second},
		{"y\n", 1 * time.Second},
		{"continue\n", 2 * time.Second},
	}

	for _, nudge := range nudgeSequence {
		err := w.sendToChild(nudge.input, fmt.Sprintf("idle_nudge_%d", w.nudgeCount))
		if err != nil {
			w.structuredLog.Printf("ERROR_NUDGE: %v", err)
		}
		time.Sleep(nudge.delay)
	}

	w.setState(StateRunning)
}

// Run starts the wrapper
func (w *Wrapper) Run(command string, args []string) error {
	// Start the command in a PTY
	cmd := exec.Command(command, args...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start PTY: %w", err)
	}
	w.ptmx = ptmx
	defer ptmx.Close()

	w.structuredLog.Printf("STARTED: command='%s' args=%v", command, args)

	// Handle window size changes
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				w.structuredLog.Printf("ERROR_RESIZE: %v", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH // Initial resize
	defer func() { signal.Stop(ch); close(ch) }()

	// Handle Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		w.structuredLog.Printf("INTERRUPT: user interrupt received")
		w.setState(StateManualMode)
	}()
	defer func() { signal.Stop(sigCh); close(sigCh) }()

	// Goroutine to read PTY output
	outputCh := make(chan []byte, 100)
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				outputCh <- data
			}
			if err != nil {
				if err != io.EOF {
					errCh <- err
				}
				return
			}
		}
	}()

	// Main event loop
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case data := <-outputCh:
			// Write to stdout
			os.Stdout.Write(data)

			// Update state
			w.updateLastOutput()
			w.outputBuffer.Write(data)

			// Check for dangerous patterns
			if w.getState() != StateManualMode {
				recentOutput := w.outputBuffer.GetContent()
				if w.checkDangerous(recentOutput) {
					w.setState(StateManualMode)
					fmt.Fprintf(os.Stderr, "\n\n🚨 DANGER DETECTED: Auto-approve disabled. Switching to manual mode.\n\n")
					continue
				}

				// Check for prompts
				if rule := w.checkPrompts(recentOutput); rule != nil {
					w.structuredLog.Printf("PROMPT_DETECTED: rule='%s'", rule.Description)
					w.handlePrompt(rule)
				}
			}

		case err := <-errCh:
			w.structuredLog.Printf("ERROR_READ: %v", err)
			return err

		case <-ticker.C:
			// Check for idle timeout
			if w.getState() == StateRunning {
				idleTime := w.getTimeSinceLastOutput()
				if idleTime > w.config.IdleTimeout {
					w.structuredLog.Printf("IDLE_DETECTED: idle_time=%v threshold=%v", idleTime, w.config.IdleTimeout)
					w.handleIdleTimeout()
				}
			}

		case <-time.After(100 * time.Millisecond):
			// Check if process exited
			err := cmd.Process.Signal(syscall.Signal(0))
			if err != nil {
				// Process exited
				cmd.Wait()
				w.structuredLog.Printf("EXITED: command finished")
				return nil
			}
		}
	}
}

// LoadConfig loads configuration from YAML file
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return DefaultConfig(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return Config{}, err
	}

	// Set defaults for missing values
	defaults := DefaultConfig()
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaults.IdleTimeout
	}
	if config.MinSendInterval == 0 {
		config.MinSendInterval = defaults.MinSendInterval
	}
	if config.NudgeInitialDelay == 0 {
		config.NudgeInitialDelay = defaults.NudgeInitialDelay
	}
	if config.NudgeMaxRetries == 0 {
		config.NudgeMaxRetries = defaults.NudgeMaxRetries
	}
	if len(config.PromptRules) == 0 {
		config.PromptRules = defaults.PromptRules
	}

	return config, nil
}

func main() {
	configPath := flag.String("config", "", "Path to rules.yaml config file")
	idleTimeout := flag.Duration("idle", 0, "Idle timeout (e.g., 15s)")
	showDefaults := flag.Bool("show-defaults", false, "Print default config and exit")
	flag.Parse()

	if *showDefaults {
		config := DefaultConfig()
		data, _ := yaml.Marshal(config)
		fmt.Println(string(data))
		return
	}

	// Load config
	config, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Override idle timeout if provided
	if *idleTimeout > 0 {
		config.IdleTimeout = *idleTimeout
	}

	// Get command to run
	args := flag.Args()
	if len(args) == 0 {
		log.Fatal("Usage: autoapprove [options] -- <command> [args...]")
	}

	command := args[0]
	commandArgs := args[1:]

	// Create and run wrapper
	wrapper := NewWrapper(config)
	err = wrapper.Run(command, commandArgs)
	if err != nil {
		log.Fatalf("Wrapper failed: %v", err)
	}
}
