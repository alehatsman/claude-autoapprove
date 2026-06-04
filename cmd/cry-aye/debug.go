package main

import (
	"log"
	"os"
	"path/filepath"
)

var debugLog *log.Logger
var debugFile *os.File

func initDebug() {
	if os.Getenv("DEBUG_AUTOAPPROVE") != "1" {
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(homeDir, ".cry-aye-debug.log")
	debugFile, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	debugLog = log.New(debugFile, "", log.LstdFlags|log.Lmicroseconds)
	debugLog.Printf("=== Debug session started ===")
}

func closeDebug() {
	if debugFile != nil {
		if debugLog != nil {
			debugLog.Printf("=== Debug session ended ===")
		}
		debugFile.Close()
		debugFile = nil
	}
}

func logf(format string, args ...any) {
	if debugLog != nil {
		debugLog.Printf(format, args...)
	}
}
