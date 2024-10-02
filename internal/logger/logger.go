package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
)

var (
	debugLogger   *log.Logger
	infoLogger    *log.Logger
	warningLogger *log.Logger
	errorLogger   *log.Logger
	currentLevel  int
)

const (
	DEBUG = iota
	INFO
	WARNING
	ERROR
)

func init() {
	flags := log.Ldate | log.Ltime | log.Lmicroseconds

	debugLogger = log.New(os.Stdout, "", flags)
	infoLogger = log.New(os.Stdout, "", flags)
	warningLogger = log.New(os.Stdout, "", flags)
	errorLogger = log.New(os.Stderr, "", flags)
	currentLevel = DEBUG // Default log level
}

func SetLogLevel(level string) error {
	switch strings.ToLower(level) {
	case "debug":
		currentLevel = DEBUG
	case "info":
		currentLevel = INFO
	case "warning":
		currentLevel = WARNING
	case "error":
		currentLevel = ERROR
	default:
		return fmt.Errorf("invalid log level: %s", level)
	}
	return nil
}

func Debug(format string, v ...interface{}) {
	if currentLevel <= DEBUG {
		debugLogger.Printf(format, v...)
	}
}

func Info(format string, v ...interface{}) {
	if currentLevel <= INFO {
		infoLogger.Printf(format, v...)
	}
}

func Warning(format string, v ...interface{}) {
	if currentLevel <= WARNING {
		warningLogger.Printf(format, v...)
	}
}

func Error(format string, v ...interface{}) {
	if currentLevel <= ERROR {
		errorLogger.Printf(format, v...)
	}
}
