package utils

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// LatestFlatFile returns the lexicographically-greatest regular file directly
// inside dir. Suits date-named flat layouts (YYYYMMDD) such as
// data/node_fast_block_times. Returns os.ErrNotExist when dir has no files.
func LatestFlatFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name := e.Name(); name > best {
			best = name
		}
	}
	if best == "" {
		return "", os.ErrNotExist
	}
	return filepath.Join(dir, best), nil
}

// LatestNumericFile returns the regular file inside dir whose name (minus
// extension) parses as the greatest integer. Height-named files are not
// zero-padded, so a lexicographic pick would put "999..." above "1000...".
func LatestNumericFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	bestName, bestVal := "", int64(-1)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		v, err := strconv.ParseInt(stem, 10, 64)
		if err != nil {
			continue
		}
		if v > bestVal {
			bestVal, bestName = v, name
		}
	}
	if bestName == "" {
		return "", os.ErrNotExist
	}
	return filepath.Join(dir, bestName), nil
}

// sortedDirsDesc returns dir's subdirectory names in lexicographically
// descending order. Both YYYYMMDD date dirs and ISO-timestamp run dirs
// sort chronologically under this ordering.
func sortedDirsDesc(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// LatestDateNumericFile resolves the <root>/<YYYYMMDD>/<numeric>[.ext]
// layout (data/periodic_abci_states) to its newest leaf, skipping date
// dirs that exist but hold no parseable files yet.
func LatestDateNumericFile(root string) (string, error) {
	dates, err := sortedDirsDesc(root)
	if err != nil {
		return "", err
	}
	for _, d := range dates {
		if path, err := LatestNumericFile(filepath.Join(root, d)); err == nil {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

// LatestReplicaFile resolves data/replica_cmds' <run>/<date>/<height>
// layout to the newest height file of the newest non-empty date of the
// newest non-empty run. Run dirs are ISO timestamps and date dirs are
// YYYYMMDD (both lex-sortable); height files are bare integers. hl-node
// prunes old runs down to empty skeleton dirs, hence the skip-empty scan.
func LatestReplicaFile(root string) (string, error) {
	runs, err := sortedDirsDesc(root)
	if err != nil {
		return "", err
	}
	for _, run := range runs {
		dates, err := sortedDirsDesc(filepath.Join(root, run))
		if err != nil {
			continue
		}
		for _, d := range dates {
			if path, err := LatestNumericFile(filepath.Join(root, run, d)); err == nil {
				return path, nil
			}
		}
	}
	return "", os.ErrNotExist
}
