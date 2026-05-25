package main

// `hl_exporter vals` — extract the full Hyperliquid validator set (IP, moniker,
// address) straight from the node's ABCI state files and emit the
// `ip,moniker,address,vp` CSV the validao.xyz node-map pipeline consumes.
//
// This deliberately parses the .rmp data-dir files directly (reusing the
// exporter's internal/abci reader) rather than scraping the Prometheus
// metrics: the metrics only surface the RTT-probed subset, not the complete
// validator IP list.
//
// Modes:
//
//	hl_exporter vals --node-home /home/ubuntu/hl                 # one-shot CSV to stdout (or --out)
//	hl_exporter vals --serve --addr 0.0.0.0:8087 --node-home ... # serve CSV at /vals/<chain>, regenerated hourly
//	  (--peer-counter-url sets the local peer-counter snapshot URL feeding /nodes; default http://127.0.0.1:19046/snapshot)
//	hl_exporter vals --backfill --since 2025-05-31 --out f.jsonl # daily validator-count history from historical states
//
// vp is left empty (not "0"): the website map renderer and node-count chart
// never read the vp column (only ip is used, for geolocation + count), and an
// empty value lets the map popup omit the Voting Power row rather than render
// "Voting Power: 0" for every validator. A populated vp can be added later by
// joining the public validatorSummaries API on address.

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/abci"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

const stateSubdir = "data/periodic_abci_states"

func runVals(args []string) {
	fs := flag.NewFlagSet("vals", flag.ExitOnError)
	nodeHome := fs.String("node-home", defaultNodeHome(), "Node home directory (contains data/periodic_abci_states)")
	chain := fs.String("chain", "testnet", "Chain label, used only in the --serve route path")
	out := fs.String("out", "", "Output file (default stdout)")
	serve := fs.Bool("serve", false, "Run an HTTP server exposing the CSV at /vals/<chain>")
	addr := fs.String("addr", "0.0.0.0:8087", "Listen address for --serve")
	interval := fs.Duration("interval", time.Hour, "Regeneration interval for --serve")
	peerCounterURL := fs.String("peer-counter-url", "http://127.0.0.1:19046/snapshot", "Local peer-counter snapshot URL feeding /nodes in --serve")
	backfill := fs.Bool("backfill", false, "Emit one validator-count row per day from historical state files (JSONL)")
	since := fs.String("since", "2025-05-31", "Backfill start date (YYYY-MM-DD)")
	sleep := fs.Duration("sleep", 2*time.Second, "Sleep between files during --backfill (be gentle on the validator)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	stateDir := filepath.Join(*nodeHome, stateSubdir)

	switch {
	case *backfill:
		if err := runBackfill(stateDir, *since, *out, *sleep); err != nil {
			fmt.Fprintf(os.Stderr, "backfill: %v\n", err)
			os.Exit(1)
		}
	case *serve:
		runServe(stateDir, *chain, *addr, *interval, *peerCounterURL)
	default:
		csvBytes, err := buildCSV(stateDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "extract: %v\n", err)
			os.Exit(1)
		}
		if err := writeOut(*out, csvBytes); err != nil {
			fmt.Fprintf(os.Stderr, "write: %v\n", err)
			os.Exit(1)
		}
	}
}

func defaultNodeHome() string {
	if h := os.Getenv("NODE_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "hl")
	}
	return "/home/ubuntu/hl"
}

// buildCSV reads the latest state file and returns the ip,moniker,address,vp CSV.
func buildCSV(stateDir string) ([]byte, error) {
	latest, err := utils.GetLatestFile(stateDir)
	if err != nil {
		return nil, fmt.Errorf("find latest state: %w", err)
	}
	if latest == "" {
		return nil, fmt.Errorf("no state files under %s", stateDir)
	}
	profiles, err := abci.NewReader(8).ReadValidatorProfiles(latest)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", latest, err)
	}

	var sb strings.Builder
	w := csv.NewWriter(&sb)
	_ = w.Write([]string{"ip", "moniker", "address", "vp"})
	for _, p := range profiles {
		// vp left empty (not "0") so the map popup omits the Voting Power row
		// rather than rendering "Voting Power: 0" for every validator.
		_ = w.Write([]string{p.IP, p.Moniker, p.Address, ""})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

func writeOut(path string, data []byte) error {
	if path == "" {
		_, err := os.Stdout.Write(data)
		return err
	}
	// Atomic write: tmp then rename, so readers never see a partial file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fetchPeerNodes returns this box's own 24h peer set from the local
// peer-counter (listen-on-box — the "all nodes" view), one IP per line. It's
// the testnet analogue of the mainnet sentinel's node list. peerCounterURL is
// the snapshot endpoint (configurable via --peer-counter-url). Returns nil on
// error so the serve cache keeps its last good value.
func fetchPeerNodes(peerCounterURL string) []byte {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(peerCounterURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "peer-counter fetch: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	var snap struct {
		PeerIPs []string `json:"peer_ips_24h"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		fmt.Fprintf(os.Stderr, "peer-counter decode: %v\n", err)
		return nil
	}
	var sb strings.Builder
	for _, ip := range snap.PeerIPs {
		if ip == "" {
			continue
		}
		sb.WriteString(ip)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// --- serve mode -------------------------------------------------------------

func runServe(stateDir, chain, addr string, interval time.Duration, peerCounterURL string) {
	cache := &csvCache{}
	nodeCache := &csvCache{}
	regen := func() {
		b, err := buildCSV(stateDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "regen: %v\n", err)
			return
		}
		cache.set(b)
		// /nodes is the "all nodes" view: this box's own 24h peer set from the
		// local peer-counter (listen-on-box, the same method as the mainnet
		// sentinel) — deliberately distinct from the validator set at /vals.
		if peers := fetchPeerNodes(peerCounterURL); peers != nil {
			nodeCache.set(peers)
		}
		fmt.Fprintf(os.Stderr, "regenerated CSV (%d bytes) at %s\n", len(b), time.Now().UTC().Format(time.RFC3339))
	}
	regen() // populate before first request
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			regen()
		}
	}()

	route := "/vals/" + chain
	http.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		b := cache.get()
		if b == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write(b)
	})
	// Flat IP-per-line list (no header) for the node-map "all nodes" pipeline,
	// which expects <DA_SERVER>/<chain>.txt. Served from this box's own
	// peer-counter 24h peer set (listen-on-box) — the testnet analogue of the
	// mainnet sentinel's node list, distinct from the validator set at /vals.
	nodesRoute := "/nodes/" + chain + ".txt"
	http.HandleFunc(nodesRoute, func(w http.ResponseWriter, r *http.Request) {
		b := nodeCache.get()
		if b == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(b)
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if cache.get() == nil {
			http.Error(w, "no data", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	fmt.Fprintf(os.Stderr, "serving %s on %s (regen every %s)\n", route, addr, interval)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

type csvCache struct {
	mu  sync.RWMutex
	buf []byte
}

func (c *csvCache) set(b []byte) {
	c.mu.Lock()
	c.buf = b
	c.mu.Unlock()
}

func (c *csvCache) get() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.buf
}

// --- backfill mode ----------------------------------------------------------

// runBackfill walks day directories ascending from `since`, decodes one
// representative (highest-height) state file per day, and writes a daily
// validator-count JSONL row. It avoids utils.GetLatestFile (which walks the
// whole tree on every call) by listing day dirs directly.
func runBackfill(stateDir, since, outPath string, sleep time.Duration) error {
	start, err := time.Parse("2006-01-02", since)
	if err != nil {
		return fmt.Errorf("bad --since %q: %w", since, err)
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", stateDir, err)
	}
	var days []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d, perr := time.Parse("20060102", e.Name())
		if perr != nil {
			continue // skip non-date dirs
		}
		if d.Before(start) {
			continue
		}
		days = append(days, e.Name())
	}
	sort.Strings(days)
	if len(days) == 0 {
		return fmt.Errorf("no day directories >= %s under %s", since, stateDir)
	}

	var w *os.File
	if outPath == "" {
		w = os.Stdout
	} else {
		w, err = os.Create(outPath)
		if err != nil {
			return err
		}
		defer w.Close()
	}

	reader := abci.NewReader(8)
	enc := json.NewEncoder(w)
	ok, fail := 0, 0
	for _, day := range days {
		f, ferr := latestRmpInDir(filepath.Join(stateDir, day))
		if ferr != nil || f == "" {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", day, ferr)
			fail++
			continue
		}
		profiles, legacy, perr := reader.ReadValidatorProfilesWithSource(f)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "skip %s (%s): %v\n", day, filepath.Base(f), perr)
			fail++
			continue
		}
		ts := day[0:4] + "-" + day[4:6] + "-" + day[6:8] + "T00:00:00Z"
		// legacy reflects which ABCI schema produced the profiles: true for the
		// pre-2026 exchange.consensus path, false for the current c_staking path.
		row := struct {
			TS     string `json:"ts"`
			Count  int    `json:"count"`
			Legacy bool   `json:"legacy"`
		}{ts, len(profiles), legacy}
		if err := enc.Encode(&row); err != nil {
			return err
		}
		ok++
		fmt.Fprintf(os.Stderr, "%s -> %d validators (%s)\n", day, len(profiles), filepath.Base(f))
		time.Sleep(sleep)
	}
	fmt.Fprintf(os.Stderr, "backfill done: %d days written, %d skipped\n", ok, fail)
	return nil
}

// latestRmpInDir returns the path to the highest-height <height>.rmp in dir.
func latestRmpInDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var bestPath string
	var bestHeight int64 = -1
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".rmp") {
			continue
		}
		h, perr := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".rmp"), 10, 64)
		if perr != nil {
			continue
		}
		if h > bestHeight {
			bestHeight = h
			bestPath = filepath.Join(dir, e.Name())
		}
	}
	if bestPath == "" {
		return "", fmt.Errorf("no .rmp files in %s", dir)
	}
	return bestPath, nil
}
