package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

// Config holds configuration values
type Config struct {
        HomeDir          string
	NodeHome         string
        BinaryHome       string
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

        binaryHome := os.Getenv("BINARY_HOME")
        if binaryHome == "" {
            binaryHome = homeDir
        }

	nodeBinary := os.Getenv("NODE_BINARY")
	if nodeBinary == "" {
		nodeBinary = binaryHome + "/hl-node"
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
		BinaryHome:       binaryHome,
		NodeBinary:       nodeBinary,
		IsValidator:      isValidator,
		ValidatorAddress: validatorAddress,
	}
}
