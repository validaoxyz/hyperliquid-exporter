package monitors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestReadSingleInt(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    int64
		ok      bool
	}{
		{"plain", "1007295000", 1007295000, true},
		{"trailing newline", "1009950000\n", 1009950000, true},
		{"leading whitespace", "  42\n", 42, true},
		{"empty", "", 0, false},
		{"non-numeric", "garbage", 0, false},
		{"float — rejected", "1.5", 0, false}, // must be integer
		{"negative — rejected", "-1", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name)
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := readSingleInt(path)
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestReadSingleInt_Missing(t *testing.T) {
	_, ok := readSingleInt(filepath.Join(t.TempDir(), "no-such-file"))
	if ok {
		t.Fatal("expected ok=false on missing file")
	}
}

func TestTickNodeStatePublishesAccuratePersistedFamiliesAndWithdraws(t *testing.T) {
	dir := t.TempDir()
	freezePath := filepath.Join(dir, "freeze_abci_height")
	fastPath := filepath.Join(dir, "fast_cp_checkpoint_height")
	slowPath := filepath.Join(dir, "slow_cp_checkpoint_height")
	for path, value := range map[string]string{freezePath: "100\n", fastPath: "120\n", slowPath: "118\n"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	SetLatestVisorHeight(125)
	tickNodeState(freezePath, fastPath, slowPath)
	if got := validatorMetricValue(t, metrics.HLNodePersistedFreezeABCIHeight.WithLabelValues("freeze_abci_height")); got != 100 {
		t.Fatalf("persisted freeze = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLNodePersistedABCIHeight.WithLabelValues("evm_db_hub_fast_cp_checkpoint_height")); got != 120 {
		t.Fatalf("fast persisted ABCI height = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLNodePersistedABCIHeightGap.WithLabelValues("fast_minus_slow")); got != 2 {
		t.Fatalf("persisted gap = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLNodeVisorHeightAbovePersistedFreeze.WithLabelValues("visor_minus_persisted_freeze")); got != 25 {
		t.Fatalf("visor above persisted freeze = %v", got)
	}

	if err := os.Remove(freezePath); err != nil {
		t.Fatal(err)
	}
	tickNodeState(freezePath, fastPath, slowPath)
	if validatorCollectorHasLabels(metrics.HLNodePersistedFreezeABCIHeight, map[string]string{"source": "freeze_abci_height"}) {
		t.Fatal("removed freeze file left a current replacement series")
	}
	if validatorCollectorHasLabels(metrics.HLNodeVisorHeightAbovePersistedFreeze, map[string]string{"comparison": "visor_minus_persisted_freeze"}) {
		t.Fatal("removed freeze file left a derived current series")
	}
	if got := validatorMetricValue(t, metrics.HLVisorFreezeAbciHeight); got != 0 {
		t.Fatalf("removed freeze file left legacy value %v", got)
	}

	if err := os.Remove(fastPath); err != nil {
		t.Fatal(err)
	}
	tickNodeState(freezePath, fastPath, slowPath)
	if validatorCollectorHasLabels(metrics.HLEVMDBCheckpointHeight, map[string]string{"tier": "fast"}) {
		t.Fatal("removed fast checkpoint file left deprecated series")
	}
	if got := validatorMetricValue(t, metrics.HLEVMDBCheckpointLagBlocks); got != 0 {
		t.Fatalf("incomplete checkpoint pair left legacy lag %v", got)
	}
}
