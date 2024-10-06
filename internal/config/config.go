package config

import (
        "fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

// Config holds configuration values
type Config struct {
        HomeDir          string
	NodeHome         string
	NodeBinary       string
	IsValidator      bool
	ValidatorAddress string
}

// LoadConfig loads environment variables and returns a Config struct
func LoadConfig() Config {
	err := godotenv.Load()
	if err != nil {
		logger.Warning("No .env file found, using default environment variables")
	}

        homeDir := os.Getenv("HOME")
        nodeHome := os.Getenv("NODE_HOME")
        if nodeHome == "" {
            nodeHome = homeDir + "/hl"
        }
	fmt.Println(nodeHome)

	nodeBinary := os.Getenv("NODE_BINARY")
	if nodeBinary == "" {
		nodeBinary = homeDir + "/hl-visor"
	}

	isValidatorEnv := os.Getenv("IS_VALIDATOR")
	isValidator, err := strconv.ParseBool(isValidatorEnv)
	if err != nil {
		isValidator = false
	}

	validatorAddress := os.Getenv("VALIDATOR_ADDRESS")

	return Config{
                HomeDir:          homeDir,
		NodeHome:         nodeHome,
		NodeBinary:       nodeBinary,
		IsValidator:      isValidator,
		ValidatorAddress: validatorAddress,
	}
}
