package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
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
	debugLogger = log.New(os.Stdout, "", 0)
	infoLogger = log.New(os.Stdout, "", 0)
	warningLogger = log.New(os.Stdout, "", 0)
	errorLogger = log.New(os.Stderr, "", 0)
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

func logWithLevel(logger *log.Logger, level string, format string, v ...interface{}) {
	timestamp := time.Now().Format("2006/01/02 15:04:05.000000")
	message := fmt.Sprintf(format, v...)
	logger.Printf("%s %s %s", timestamp, level, message)
}

func Debug(format string, v ...interface{}) {
	if currentLevel <= DEBUG {
		logWithLevel(debugLogger, "DEBUG", format, v...)
	}
}

func Info(format string, v ...interface{}) {
	if currentLevel <= INFO {
		logWithLevel(infoLogger, "INFO", format, v...)
	}
}

func Warning(format string, v ...interface{}) {
	if currentLevel <= WARNING {
		logWithLevel(warningLogger, "WARNING", format, v...)
	}
}

func Error(format string, v ...interface{}) {
	if currentLevel <= ERROR {
		logWithLevel(errorLogger, "ERROR", format, v...)
	}
}
