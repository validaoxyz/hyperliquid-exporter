package exporter

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/monitors"
)

func TestBinaryMonitorOptIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("binary execution fixtures require /bin/sh")
	}
	tests := []struct {
		name                    string
		enabled, skipVersion    bool
		skipUpdate, direct      bool
		wantVersion, wantUpdate bool
	}{
		{name: "default"},
		{name: "default_direct", direct: true},
		{name: "enabled", enabled: true, wantVersion: true, wantUpdate: true},
		{name: "skip_version", enabled: true, skipVersion: true, wantUpdate: true},
		{name: "skip_update", enabled: true, skipUpdate: true, wantVersion: true},
		{name: "skip_both", enabled: true, skipVersion: true, skipUpdate: true},
		{name: "skip_both_direct", enabled: true, skipVersion: true, skipUpdate: true, direct: true},
	}
	const childEnv = "HL_EXPORTER_BINARY_MONITOR_CHILD"
	child := os.Getenv(childEnv)
	for _, test := range tests {
		if child == "" {
			t.Run(test.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBinaryMonitorOptIn$", "-test.count=1")
				command.Env = append(os.Environ(), childEnv+"="+test.name)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("isolated binary monitor test: %v\n%s", err, output)
				}
			})
			continue
		}
		if child != test.name {
			continue
		}

		root := t.TempDir()
		t.Setenv("HL_EXPORTER_BINARY_TEST_DIR", root)
		for name, contents := range map[string]string{
			"hl-node":                   binaryProbeFixture("node", "nodecommit"),
			"hl-visor":                  binaryProbeFixture("visor", "visorcommit"),
			"last_known_public_ip.json": `"192.0.2.1"`,
		} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0700); err != nil {
				t.Fatal(err)
			}
		}

		var requests atomic.Int64
		originalTransport := http.DefaultTransport
		http.DefaultTransport = binaryProbeTransport(func(request *http.Request) (*http.Response, error) {
			requests.Add(1)
			if request.Method != http.MethodGet || request.URL.String() != "https://binaries.hyperliquid-testnet.xyz/Testnet/hl-visor" {
				return nil, fmt.Errorf("unexpected HTTP request: %s %s", request.Method, request.URL)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Etag": []string{`"fixture"`}},
				Body:       io.NopCloser(strings.NewReader(binaryProbeFixture("downloaded", "visorcommit"))),
				Request:    request,
			}, nil
		})
		t.Cleanup(func() { http.DefaultTransport = originalTransport })

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		owner, err := metrics.InitMetrics(ctx, metrics.MetricsConfig{
			Chain: "testnet", NodeHome: root, EnablePrometheus: true, PrometheusPort: port,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cancel()
			waitForMonitorWorkers()
			monitors.WaitForWorkers()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := owner.Shutdown(shutdownCtx); err != nil {
				t.Errorf("metrics shutdown: %v", err)
			}
		})

		cfg := config.Config{
			Chain: "testnet", NodeHome: root, BinaryHome: root, NodeBinary: filepath.Join(root, "hl-node"),
			EnableBinaryMetrics: test.enabled, SkipVersionCheck: test.skipVersion, SkipUpdateCheck: test.skipUpdate,
		}
		versionErrors, updateErrors := make(chan error, 4), make(chan error, 4)
		if test.direct {
			monitors.StartVersionMonitor(ctx, cfg, versionErrors)
			monitors.StartUpdateChecker(ctx, cfg, updateErrors)
		} else {
			startBinaryMonitors(ctx, cfg, versionErrors, updateErrors)
		}
		waitForMonitorWorkers()

		// Race instrumentation and concurrent package builds can delay fixture
		// processes. Keep publication bounded within the parent's 30s deadline.
		deadline := time.Now().Add(15 * time.Second)
		for {
			families := gatherBinaryProbeMetrics(t)
			if (!test.wantVersion || families["hl_software_version"] != nil) && (!test.wantUpdate || families["hl_software_up_to_date"] != nil) {
				break
			}
			select {
			case err := <-versionErrors:
				t.Fatalf("version probe: %v", err)
			case err := <-updateErrors:
				t.Fatalf("update probe: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Logf("software metrics: version=%v update=%v; HTTP requests=%d",
					families["hl_software_version"], families["hl_software_up_to_date"], requests.Load())
				for _, name := range []string{"node", "visor", "downloaded"} {
					body, err := os.ReadFile(filepath.Join(root, name+".executed"))
					t.Logf("%s execution marker: %q; read error: %v", name, body, err)
				}
				for name, errors := range map[string]chan error{"version": versionErrors, "update": updateErrors} {
					select {
					case err := <-errors:
						t.Logf("%s probe error: %v", name, err)
					default:
						t.Logf("%s probe: no queued error", name)
					}
				}
				t.Fatal("enabled probes did not publish their software metrics")
			}
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
		monitors.WaitForWorkers()
		for _, errors := range []chan error{versionErrors, updateErrors} {
			select {
			case err := <-errors:
				t.Fatalf("unexpected probe error: %v", err)
			default:
			}
		}

		families := gatherBinaryProbeMetrics(t)
		for name, wanted := range map[string]bool{"hl_software_version": test.wantVersion, "hl_software_up_to_date": test.wantUpdate} {
			family := families[name]
			if (family != nil) != wanted {
				t.Fatalf("%s presence = %v, want %v", name, family != nil, wanted)
			}
			if wanted && (len(family.Metric) != 1 || family.Metric[0].GetGauge().GetValue() != 1) {
				t.Fatalf("%s must contain one successful observation: %v", name, family)
			}
		}
		if test.wantVersion {
			labels := families["hl_software_version"].Metric[0].GetLabel()
			if binaryProbeLabel(labels, "commit") != "nodecommit" || binaryProbeLabel(labels, "date") != "2026-09-09" {
				t.Fatalf("local version labels = %v", labels)
			}
		}
		for name, wanted := range map[string]bool{"node": test.wantVersion, "visor": test.wantUpdate, "downloaded": test.wantUpdate} {
			body, err := os.ReadFile(filepath.Join(root, name+".executed"))
			if !wanted {
				if !os.IsNotExist(err) {
					t.Fatalf("disabled %s fixture executed or marker unreadable: %q, %v", name, body, err)
				}
			} else if err != nil || string(body) != "--version\n" {
				t.Fatalf("%s fixture execution = %q, %v; want one --version call", name, body, err)
			}
		}
		wantRequests := int64(0)
		if test.wantUpdate {
			wantRequests = 1
		}
		if got := requests.Load(); got != wantRequests {
			t.Fatalf("HTTP requests = %d, want %d", got, wantRequests)
		}
		for monitor, wanted := range map[string]bool{"version": test.wantVersion, "update_checker": test.wantUpdate} {
			started := false
			for _, sample := range families["hl_exporter_monitor_started_seconds"].GetMetric() {
				if binaryProbeLabel(sample.GetLabel(), "monitor") == monitor {
					started = sample.GetGauge().GetValue() > 0
				}
			}
			if started != wanted {
				t.Fatalf("%s monitor started = %v, want %v", monitor, started, wanted)
			}
		}
		return
	}
	if child != "" {
		t.Fatalf("unknown binary monitor test scenario %q", child)
	}
}

type binaryProbeTransport func(*http.Request) (*http.Response, error)

func (transport binaryProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func binaryProbeFixture(name, commit string) string {
	return "#!/bin/sh\n" +
		"[ \"$#\" -eq 1 ] && [ \"$1\" = \"--version\" ] || exit 9\n" +
		"printf '%s\\n' \"$1\" >> \"$HL_EXPORTER_BINARY_TEST_DIR/" + name + ".executed\"\n" +
		"printf '%s\\n' 'commit " + commit + " | 2026-09-09 | fixture'\n"
}

func gatherBinaryProbeMetrics(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()
	metrics.PublishMonitorHealthSnapshot()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]*dto.MetricFamily, len(families))
	for _, family := range families {
		result[family.GetName()] = family
	}
	return result
}

func binaryProbeLabel(labels []*dto.LabelPair, name string) string {
	for _, label := range labels {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}
