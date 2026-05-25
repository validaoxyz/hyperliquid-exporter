//go:build windows

package monitors

import "errors"

func statfs(path string) (*fsStats, error) {
	return nil, errors.New("statfs unsupported on windows")
}
