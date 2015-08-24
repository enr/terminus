// This file contains a collection of helper functions.
package utils

import (
	"io"
	"log"
	"os"
)

var (
	Trace   *log.Logger
	Info    *log.Logger
	Warning *log.Logger
	Error   *log.Logger
)

// LogInit initializing logging commands
func LogInit(traceHandle io.Writer, infoHandle io.Writer, warningHandle io.Writer, errorHandle io.Writer) {
	Trace = log.New(traceHandle, "TRACE: ", 0)
	Info = log.New(infoHandle, "INFO: ", 0)
	Warning = log.New(warningHandle, "WARNING: ", 0)
	Error = log.New(errorHandle, "ERROR: ", 0)
}

// ExitWithError is a helper function that prints an error and then exits with return code 1
func ExitWithError(err error) {
	Error.Println(err)
	os.Exit(1)
}
