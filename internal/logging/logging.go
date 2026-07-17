package logging

import (
	"fmt"
	"io"
	"log"
	"os"
)

// Logger provides structured logging
type Logger struct {
	logger *log.Logger
}

// Default logger instance
var (
	Info  *log.Logger
	Error *log.Logger
	Debug *log.Logger
)

func init() {
	// Initialize loggers that write to stderr (safe for both stdio and http modes)
	Info = log.New(os.Stderr, "INFO: ", log.LstdFlags)
	Error = log.New(os.Stderr, "ERROR: ", log.LstdFlags|log.Lshortfile)
	Debug = log.New(os.Stderr, "DEBUG: ", log.LstdFlags)
}

// SetOutput sets the output writer for all loggers
func SetOutput(w io.Writer) {
	Info.SetOutput(w)
	Error.SetOutput(w)
	Debug.SetOutput(w)
}

// Info logs info messages
func InfoMsg(format string, v ...interface{}) {
	Info.Output(2, fmt.Sprintf(format, v...))
}

// Error logs error messages
func ErrorMsg(format string, v ...interface{}) {
	Error.Output(2, fmt.Sprintf(format, v...))
}

// Debug logs debug messages
func DebugMsg(format string, v ...interface{}) {
	Debug.Output(2, fmt.Sprintf(format, v...))
}