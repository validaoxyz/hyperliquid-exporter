package monitors

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const childStderrPollInterval = 60 * time.Second

// childStderrHeadBytes bounds how much of each stderr file is read for
// classification; the signature is always in the first lines.
const childStderrHeadBytes = 4096

// childCrashReasons is the fixed reason label set, checked in order.
// Sourced from a year of live stderr files on a testnet validator:
// 1552 config errors, 746 sync overflows, 358 hardfork restarts,
// 2 app-hash mismatches, a handful of network errors and raw panics.
var childCrashReasons = []struct {
	reason  string
	needles [][]byte
}{
	{"app_hash_mismatch", [][]byte{[]byte("computed app hash"), []byte("does not match quorum")}},
	{"hardfork_upgrade", [][]byte{[]byte("observed qc for newer hardfork")}},
	{"sync_overflow", [][]byte{[]byte("too many blocks to request")}},
	{"config_error", [][]byte{[]byte("is not in node_ips"), []byte("could not read"), []byte("CONFIGURATION ERROR")}},
	{"network", [][]byte{[]byte("connection timeout"), []byte("upstream connect error"), []byte("invalid ip address")}},
}

// classifyChildStderr maps a stderr head to a bounded reason label.
func classifyChildStderr(head []byte) string {
	for _, c := range childCrashReasons {
		for _, n := range c.needles {
			if bytes.Contains(head, n) {
				return c.reason
			}
		}
	}
	return "panic"
}

// childStderrState caches per-file classification so each stderr file is
// read once; files are write-once (one per child start).
type childStderrState struct {
	reason  string // "" for a clean start (empty file)
	modTime time.Time
}

// StartChildStderrMonitor watches $NODE_HOME/data/visor_child_stderr,
// where hl-visor drops one file per hl-node child start: empty on a clean
// start, the child's dying output otherwise. This is the only place
// app-hash mismatches (consensus divergence) and config rejections like
// "validator is not in node_ips" surface; crit_msg_stats never sees them
// because the process is already dead.
//
// The date-named directories in this tree run WEEKS ahead of wall clock
// (a node-internal schedule, not a bug in our reading), so everything
// here keys off file mtimes, never path names.
func StartChildStderrMonitor(ctx context.Context, cfg config.Config) {
	root := filepath.Join(cfg.NodeHome, "data", "visor_child_stderr")
	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("child_stderr",
			"visor_child_stderr not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("child_stderr", "watching %s", root)

	seen := map[string]*childStderrState{}

	ticker := time.NewTicker(childStderrPollInterval)
	defer ticker.Stop()

	tickChildStderr(root, seen)
	metrics.MarkMonitorTick("child_stderr")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickChildStderr(root, seen)
			metrics.MarkMonitorTick("child_stderr")
		}
	}
}

func tickChildStderr(root string, seen map[string]*childStderrState) {
	live := map[string]bool{}

	// layout: <vdate>/<n>/<restart_ts>; three bounded ReadDir levels
	dateDirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, dd := range dateDirs {
		if !dd.IsDir() {
			continue
		}
		nDirs, err := os.ReadDir(filepath.Join(root, dd.Name()))
		if err != nil {
			continue
		}
		for _, nd := range nDirs {
			if !nd.IsDir() {
				continue
			}
			dir := filepath.Join(root, dd.Name(), nd.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				path := filepath.Join(dir, f.Name())
				live[path] = true
				if _, ok := seen[path]; ok {
					continue // write-once files; classified already
				}
				info, err := f.Info()
				if err != nil {
					continue
				}
				st := &childStderrState{modTime: info.ModTime()}
				if info.Size() > 0 {
					if head, err := readHead(path, childStderrHeadBytes); err == nil {
						st.reason = classifyChildStderr(head)
					} else {
						continue // retry next tick
					}
				}
				seen[path] = st
			}
		}
	}

	// drop pruned files from the cache, then publish retained-state counts
	for path := range seen {
		if !live[path] {
			delete(seen, path)
		}
	}

	starts := 0
	crashes := map[string]int{}
	lastCrash := map[string]time.Time{}
	for _, st := range seen {
		starts++
		if st.reason == "" {
			continue
		}
		crashes[st.reason]++
		if st.modTime.After(lastCrash[st.reason]) {
			lastCrash[st.reason] = st.modTime
		}
	}

	metrics.HLNodeChildStarts.Set(float64(starts))
	for _, c := range childCrashReasons {
		metrics.HLNodeChildCrashes.WithLabelValues(c.reason).Set(float64(crashes[c.reason]))
	}
	metrics.HLNodeChildCrashes.WithLabelValues("panic").Set(float64(crashes["panic"]))
	for reason, t := range lastCrash {
		metrics.HLNodeChildLastCrashSeconds.WithLabelValues(reason).Set(float64(t.Unix()))
	}
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.Read(buf)
	if read > 0 {
		return buf[:read], nil
	}
	return nil, err
}
