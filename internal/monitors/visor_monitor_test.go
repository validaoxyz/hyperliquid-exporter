package monitors

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestLatestHourlyFile_NumericHourSort(t *testing.T) {
	// Reproduce the bug we found on a live node: hour names are bare
	// integers ("0".."23") without a leading zero. Lex order would put
	// "10" before "2", which means a naive sort would pick the wrong
	// "latest" file when the day crosses 10:00. Verify our sort uses
	// numeric order.
	root := t.TempDir()
	dateDir := filepath.Join(root, "20260525")
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create files for hours 1, 2, 9, 10, 11 — lex order would pick 9 last;
	// numeric order picks 11.
	for _, h := range []int{1, 2, 9, 10, 11} {
		path := filepath.Join(dateDir, strconv.Itoa(h))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := latestHourlyFile(root)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := "/20260525/11"
	if want := filepath.Join(dateDir, "11"); got != want {
		t.Errorf("got %q, want %q (suffix %q)", got, want, wantSuffix)
	}
}

func TestPublishVisorStateHardforkAndScheduledFreezeWithdrawOnOmission(t *testing.T) {
	freeze := int64(900)
	hardfork := int64(1476)
	publishVisorState(visorState{
		Height: 1000, InitialHeight: 800,
		ScheduledFreezeHeight: &freeze,
		HardforkVersion:       &hardfork,
	}, time.Now())
	if got := validatorMetricValue(t, metrics.HLVisorHardforkVersion.WithLabelValues("visor_state")); got != 1476 {
		t.Fatalf("hardfork version = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLVisorScheduledFreezeHeightCurrent.WithLabelValues("visor_state")); got != 900 {
		t.Fatalf("scheduled freeze = %v", got)
	}

	publishVisorState(visorState{Height: 1001, InitialHeight: 800}, time.Now())
	if validatorCollectorHasLabels(metrics.HLVisorHardforkVersion, map[string]string{"source": "visor_state"}) {
		t.Fatal("omitted hardfork version left a current series")
	}
	if validatorCollectorHasLabels(metrics.HLVisorScheduledFreezeHeightCurrent, map[string]string{"source": "visor_state"}) {
		t.Fatal("null scheduled freeze left a current series")
	}
	if got := validatorMetricValue(t, metrics.HLVisorHardforkVersionAvailable); got != 0 {
		t.Fatalf("hardfork availability = %v", got)
	}
	if got := validatorMetricValue(t, metrics.HLVisorScheduledFreezeHeightAvailable); got != 0 {
		t.Fatalf("freeze availability = %v", got)
	}
}

func TestDecodeVisorStateOptionalVersionFieldsAreIndependent(t *testing.T) {
	for name, tc := range map[string]struct {
		field       string
		wantVersion *int64
	}{
		"present":    {`1476`, int64Pointer(1476)},
		"zero":       {`0`, int64Pointer(0)},
		"negative":   {`-1`, nil},
		"wrong type": {`"1476"`, nil},
		"null":       {`null`, nil},
		"omitted":    {``, nil},
	} {
		t.Run(name, func(t *testing.T) {
			optional := ""
			if tc.field != "" {
				optional = `,"hardfork_version":` + tc.field
			}
			s, err := decodeVisorState([]byte(`{"height":100,"initial_height":90` + optional + `}`))
			if err != nil || s.Height != 100 {
				t.Fatalf("decoded core state = %+v, %v", s, err)
			}
			if tc.wantVersion == nil {
				if s.HardforkVersion != nil {
					t.Fatalf("hardfork version = %v, want unavailable", *s.HardforkVersion)
				}
			} else if s.HardforkVersion == nil || *s.HardforkVersion != *tc.wantVersion {
				t.Fatalf("hardfork version = %v, want %v", s.HardforkVersion, *tc.wantVersion)
			}
		})
	}
}

func TestReadLatestVisorStateUsesValidatedHourlyFallback(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "missing-live.json")
	hourDir := filepath.Join(root, "hourly", "20260808")
	if err := os.MkdirAll(hourDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hour := filepath.Join(hourDir, "18")
	records := "[\"2026-08-08T18:00:00.000000000\",{\"height\":1475,\"initial_height\":1400}]\n" +
		"[\"2026-08-08T18:01:00.000000000\",{\"height\":1476,\"initial_height\":1400,\"hardfork_version\":1476}]\n"
	if err := os.WriteFile(hour, []byte(records), 0o600); err != nil {
		t.Fatal(err)
	}
	s, observedAt, err := readLatestVisorState(snapshot, filepath.Join(root, "hourly"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Height != 1476 || s.HardforkVersion == nil || *s.HardforkVersion != 1476 {
		t.Fatalf("fallback state = %+v", s)
	}
	if observedAt.Format("15:04") != "18:01" {
		t.Fatalf("fallback timestamp = %v", observedAt)
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestLatestHourlyFile_LatestDate(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"20260524", "20260525"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		// older date has hour 23, newer date has only hour 6
		hour := "23"
		if d == "20260525" {
			hour = "6"
		}
		if err := os.WriteFile(filepath.Join(root, d, hour), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := latestHourlyFile(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "20260525", "6")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLatestHourlyFile_MissingRoot(t *testing.T) {
	_, err := latestHourlyFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error on missing root")
	}
}

func TestParseVisorTime(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
	}{
		{"2026-05-25T07:00:09.501967925", true}, // nanoseconds, no zone
		{"2026-05-25T07:00:09Z", true},
		{"2026-05-25T07:00:09.123Z", true},
		{"2026-05-25T07:00:09", true},
		{"", false},
		{"not a time", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			_, ok := parseVisorTime(c.in)
			if ok != c.wantOK {
				t.Errorf("ok=%v want %v for %q", ok, c.wantOK, c.in)
			}
		})
	}
}
