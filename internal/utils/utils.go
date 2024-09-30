package utils

import (
	"os"
	"path/filepath"
	"time"
)

// GetLatestFile returns the path to the latest modified file in the directory
func GetLatestFile(directory string) (string, error) {
	var latestFile string
	var latestModTime time.Time

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if info.ModTime().After(latestModTime) {
				latestModTime = info.ModTime()
				latestFile = path
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}
	return latestFile, nil
}
