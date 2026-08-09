package monitors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestParsePublicIP(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
		ok   bool
	}{
		{"json-string", `"203.0.113.51"`, "203.0.113.51", true},
		{"json-string with trailing newline", "\"1.2.3.4\"\n", "1.2.3.4", true},
		{"bare string", "1.2.3.4", "1.2.3.4", true},
		{"quoted bare", "'5.6.7.8'", "5.6.7.8", true},
		{"empty", "", "", false},
		{"whitespace only", "   \n", "", false},
		{"json null", "null", "", false},
		{"json object", `{"ip":"1.2.3.4"}`, "", false},
		{"not an ip", "example.com", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parsePublicIP([]byte(c.data))
			if ok != c.ok {
				t.Fatalf("ok=%v want %v (data %q)", ok, c.ok, c.data)
			}
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestPublicIPMissingFailureAndRecreationLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last_known_public_ip.json")
	now := time.Now().UTC().Truncate(time.Second)
	if err := os.WriteFile(path, []byte(`"203.0.113.7"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-30*time.Second), now.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	current := ""
	if err := tickPublicIP(path, &current, now); err != nil {
		t.Fatal(err)
	}
	if value, ok := b03CollectorValue(t, metrics.HLNodePublicIPAgeSeconds, nil); !ok || value != 30 {
		t.Fatalf("public-IP file age = %v, %v", value, ok)
	}
	if !b03CollectorHasLabels(t, metrics.HLNodePublicIPInfo, map[string]string{"ip": "203.0.113.7"}) {
		t.Fatal("public-IP info missing")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := tickPublicIP(path, &current, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if current != "" || len(b03CollectorRows(t, metrics.HLNodePublicIPAgeSeconds)) != 0 || b03CollectorHasLabels(t, metrics.HLNodePublicIPInfo, map[string]string{"ip": "203.0.113.7"}) {
		t.Fatal("confirmed deletion retained public-IP state")
	}
	if b03GatherHasFamily(t, "hl_node_public_ip_age_seconds") {
		t.Fatal("confirmed deletion left public-IP age in gathered exposition")
	}

	if err := os.WriteFile(path, []byte(`"203.0.113.8"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(2*time.Second), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tickPublicIP(path, &current, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if current != "203.0.113.8" || len(b03CollectorRows(t, metrics.HLNodePublicIPAgeSeconds)) != 1 {
		t.Fatal("recreated public-IP file was not collected")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := tickPublicIP(path, &current, now.Add(4*time.Second)); err == nil {
		t.Fatal("directory-as-file did not produce a transient read failure")
	}
	if current != "203.0.113.8" || !b03CollectorHasLabels(t, metrics.HLNodePublicIPInfo, map[string]string{"ip": "203.0.113.8"}) {
		t.Fatal("transient read failure cleared last valid public-IP info")
	}
}

func TestPublicIPAgeHelpMakesNoCadencePromise(t *testing.T) {
	metrics.HLNodePublicIPAgeSeconds.WithLabelValues().Set(0)
	t.Cleanup(func() { metrics.HLNodePublicIPAgeSeconds.DeleteLabelValues() })
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "hl_node_public_ip_age_seconds" {
			continue
		}
		help := strings.ToLower(family.GetHelp())
		for _, forbidden := range []string{"heartbeat", "periodic", "startup-only", "13 min"} {
			if strings.Contains(help, forbidden) {
				t.Fatalf("public-IP age HELP contains cadence promise %q: %q", forbidden, help)
			}
		}
		return
	}
	t.Fatal("public-IP age family not registered")
}
