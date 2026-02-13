package debug

import (
	"log"
	"os"
	"path/filepath"
)

// Logger is a debug logger that writes to a file when enabled
var Logger *log.Logger
var Enabled bool
var logFile *os.File

// Init initializes the debug logger if DEBUG_AUTOAPPROVE=1
func Init() {
	Enabled = os.Getenv("DEBUG_AUTOAPPROVE") == "1"
	if !Enabled {
		return
	}

	// Create log file in home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	logPath := filepath.Join(homeDir, ".claude-autoapprove-debug.log")
	logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}

	Logger = log.New(logFile, "", log.LstdFlags)
	Logger.Printf("=== Debug session started ===")
}

// Close closes the debug log file
func Close() {
	if logFile != nil {
		if Logger != nil {
			Logger.Printf("=== Debug session ended ===")
		}
		logFile.Close()
		logFile = nil
	}
}
