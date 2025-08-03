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
	enableColors  bool = true
)

const (
	DEBUG = iota
	INFO
	WARNING
	ERROR
)

// ANSI color codes
const (
	ColorReset = "\033[0m"
	ColorBold  = "\033[1m"
	ColorDim   = "\033[2m"

	// text colors
	ColorRed     = "\033[31m"
	ColorGreen   = "\033[32m"
	ColorYellow  = "\033[33m"
	ColorBlue    = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorCyan    = "\033[36m"
	ColorWhite   = "\033[37m"
	ColorGray    = "\033[90m"

	// background colors
	BgRed    = "\033[41m"
	BgYellow = "\033[43m"
)

// component color mapping for consistent visual organization
var componentColors = map[string]string{
	"CORE":      ColorBlue,
	"EVM":       ColorGreen,
	"REPLICA":   ColorCyan,
	"CONSENSUS": ColorMagenta,
	"METAL":     ColorYellow,
	"SYSTEM":    ColorWhite,
	"ERROR":     ColorRed,
}

func init() {
	debugLogger = log.New(os.Stdout, "", 0)
	infoLogger = log.New(os.Stdout, "", 0)
	warningLogger = log.New(os.Stdout, "", 0)
	errorLogger = log.New(os.Stderr, "", 0)
	currentLevel = DEBUG
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

func SetColorsEnabled(enabled bool) {
	enableColors = enabled
}

func getComponentColor(component string) string {
	if !enableColors {
		return ""
	}

	// map component to category
	comp := strings.ToUpper(component)
	for category, color := range componentColors {
		if strings.Contains(comp, category) {
			return color
		}
	}
	return ColorGray // default gray for unknown components
}

func formatComponent(component string) string {
	if component == "" {
		return ""
	}

	color := getComponentColor(component)
	reset := ""
	if enableColors {
		reset = ColorReset
	}

	// format: [COMPONENT]
	return fmt.Sprintf("%s[%s]%s ", color, strings.ToUpper(component), reset)
}

func logWithLevel(logger *log.Logger, level, component, format string, v ...interface{}) {
	timestamp := time.Now().Format("2006/01/02 15:04:05.000000")
	message := fmt.Sprintf(format, v...)

	// color log level
	levelColor := ""
	reset := ""
	if enableColors {
		reset = ColorReset
		switch level {
		case "DEBUG":
			levelColor = ColorGray
		case "INFO":
			levelColor = ColorGreen
		case "WARNING":
			levelColor = ColorYellow + ColorBold
		case "ERROR":
			levelColor = ColorRed + ColorBold
		}
	}

	componentStr := formatComponent(component)
	logger.Printf("%s %s%s%s %s%s",
		timestamp,
		levelColor, level, reset,
		componentStr,
		message)
}

// original functions for backward compatibility
func Debug(format string, v ...interface{}) {
	if currentLevel <= DEBUG {
		logWithLevel(debugLogger, "DEBUG", "", format, v...)
	}
}

func Info(format string, v ...interface{}) {
	if currentLevel <= INFO {
		logWithLevel(infoLogger, "INFO", "", format, v...)
	}
}

func Warning(format string, v ...interface{}) {
	if currentLevel <= WARNING {
		logWithLevel(warningLogger, "WARNING", "", format, v...)
	}
}

func Error(format string, v ...interface{}) {
	if currentLevel <= ERROR {
		logWithLevel(errorLogger, "ERROR", "", format, v...)
	}
}

// new component-aware logging functions
func DebugComponent(component, format string, v ...interface{}) {
	if currentLevel <= DEBUG {
		logWithLevel(debugLogger, "DEBUG", component, format, v...)
	}
}

func InfoComponent(component, format string, v ...interface{}) {
	if currentLevel <= INFO {
		logWithLevel(infoLogger, "INFO", component, format, v...)
	}
}

func WarningComponent(component, format string, v ...interface{}) {
	if currentLevel <= WARNING {
		logWithLevel(warningLogger, "WARNING", component, format, v...)
	}
}

func ErrorComponent(component, format string, v ...interface{}) {
	if currentLevel <= ERROR {
		logWithLevel(errorLogger, "ERROR", component, format, v...)
	}
}
