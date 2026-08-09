package metrics

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics/metricinventory"
	"go.opentelemetry.io/otel/attribute"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	api "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

type documentedMetric struct {
	kind          string
	backend       string
	labels        []string
	profile       string
	help          string
	source        string
	compatibility string
}

func TestMetricInventoryMatchesDeclarations(t *testing.T) {
	metricsDir, docsPath := inventoryPaths(t)
	declared, err := metricinventory.Scan(metricsDir)
	if err != nil {
		t.Fatalf("scan metric declarations: %v", err)
	}
	documented := readDocumentedMetrics(t, docsPath)
	if len(documented) != len(declared) {
		t.Fatalf("documented metric count = %d, declaration count = %d", len(documented), len(declared))
	}

	for _, entry := range declared {
		got, ok := documented[entry.Name]
		if !ok {
			t.Errorf("declaration missing from docs: %s", entry.Name)
			continue
		}
		if got.kind != entry.Kind || got.backend != entry.Backend || got.profile != entry.Profile || got.help != entry.Help || got.source != entry.Source || got.compatibility != entry.Compatibility || strings.Join(got.labels, "\x00") != strings.Join(entry.Labels, "\x00") {
			t.Errorf("inventory drift for %s:\n got  %+v\n want %+v", entry.Name, got, entry)
		}
		if strings.TrimSpace(entry.Help) == "" {
			t.Errorf("%s has empty HELP", entry.Name)
		}
	}
}

func TestMetricInventoryRuntimeFamiliesAreDeclared(t *testing.T) {
	metricsDir, _ := inventoryPaths(t)
	declared, err := metricinventory.Scan(metricsDir)
	if err != nil {
		t.Fatalf("scan metric declarations: %v", err)
	}
	want := make(map[string]metricinventory.Entry, len(declared))
	for _, entry := range declared {
		want[entry.Name] = entry
	}

	// Exercise every lazy registration profile. This cannot make an
	// unpopulated vector appear, but it does validate all scalar families and
	// the fixed outcome vectors seeded by their initializer.
	InitInfoProbeInstruments()
	InitInfoProbeStatusInstruments()
	InitExchangeStatusDeltaInstrument()
	InitJailingConfigInstruments()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather runtime registry: %v", err)
	}
	for _, family := range families {
		name := family.GetName()
		if !strings.HasPrefix(name, "hl_") {
			continue
		}
		entry, ok := want[name]
		if !ok {
			t.Errorf("runtime family has no declaration inventory row: %s", name)
			continue
		}
		gotKind := strings.ToLower(family.GetType().String())
		if gotKind != entry.Kind {
			t.Errorf("runtime type for %s = %s, declaration = %s", name, gotKind, entry.Kind)
		}
		if family.GetHelp() != entry.Help {
			t.Errorf("runtime HELP drift for %s:\n got  %q\n want %q", name, family.GetHelp(), entry.Help)
		}
	}
}

func TestMetricInventoryOTelRuntimeProfile(t *testing.T) {
	const childEnv = "HL_EXPORTER_OTEL_INVENTORY_CHILD"
	if os.Getenv(childEnv) != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestMetricInventoryOTelRuntimeProfile$", "-test.count=1")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated OTel inventory profile failed: %v\n%s", err, output)
		}
		return
	}

	metricsDir, _ := inventoryPaths(t)
	declared, err := metricinventory.Scan(metricsDir)
	if err != nil {
		t.Fatalf("scan metric declarations: %v", err)
	}
	want := make(map[string]metricinventory.Entry)
	for _, entry := range declared {
		if entry.Backend == "OTel bridge" {
			want[entry.Name] = entry
		}
	}

	registry := prometheus.NewRegistry()
	exporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registry),
		otelprometheus.WithoutScopeInfo(),
		prometheusTranslationCompatibilityOption(),
		prometheusUnitCompatibilityOption(),
	)
	if err != nil {
		t.Fatalf("create isolated OTel Prometheus exporter: %v", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown isolated OTel provider: %v", err)
		}
	})

	metricsMutex.Lock()
	previousMeter, previousCurrent, previousLabeled, previousCallbacks := meter, currentValues, labeledValues, callbacks
	meter = provider.Meter("metric-inventory")
	currentValues = make(map[api.Observable]interface{})
	labeledValues = make(map[api.Observable]map[string]labeledValue)
	callbacks = nil
	metricsMutex.Unlock()
	t.Cleanup(func() {
		metricsMutex.Lock()
		meter, currentValues, labeledValues, callbacks = previousMeter, previousCurrent, previousLabeled, previousCallbacks
		metricsMutex.Unlock()
	})

	if err := createInstruments(); err != nil {
		t.Fatalf("create OTel instruments: %v", err)
	}
	if err := RegisterCallbacks(); err != nil {
		t.Fatalf("register OTel callbacks: %v", err)
	}

	metricsMutex.Lock()
	for _, observable := range getAllObservables() {
		switch observable.(type) {
		case api.Int64Observable:
			currentValues[observable] = int64(1)
		case api.Float64Observable:
			currentValues[observable] = float64(1)
		}
	}
	metricsMutex.Unlock()
	seedOTelCountersAndHistograms()

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather isolated OTel registry: %v", err)
	}
	seen := make(map[string]struct{}, len(families))
	for _, family := range families {
		name := family.GetName()
		if !strings.HasPrefix(name, "hl_") {
			continue
		}
		entry, ok := want[name]
		if !ok {
			t.Errorf("OTel runtime family has no OTel declaration row: %s", name)
			continue
		}
		seen[name] = struct{}{}
		if got := strings.ToLower(family.GetType().String()); got != entry.Kind {
			t.Errorf("OTel runtime type for %s = %s, declaration = %s", name, got, entry.Kind)
		}
		if family.GetHelp() != entry.Help {
			t.Errorf("OTel runtime HELP drift for %s:\n got  %q\n want %q", name, family.GetHelp(), entry.Help)
		}
	}
	for name := range want {
		if _, ok := seen[name]; !ok {
			t.Errorf("OTel declaration did not expose a runtime family after synthetic observation: %s", name)
		}
	}
}

func seedOTelCountersAndHistograms() {
	ctx := context.Background()
	nameLabels := api.WithAttributes(
		attribute.String("validator", "validator"),
		attribute.String("signer", "signer"),
		attribute.String("name", "name"),
	)
	HLConsensusProposerCounter.Add(ctx, 1, nameLabels)
	HLCoreTxCounter.Add(ctx, 1, api.WithAttributes(attribute.String("type", "other")))
	HLCoreOrdersCounter.Add(ctx, 1)
	HLCoreOperationsCounter.Add(ctx, 1, api.WithAttributes(attribute.String("type", "other"), attribute.String("category", "other")))
	HLCoreBlocksProcessedCounter.Add(ctx, 1)
	HLEVMTxTypeCounter.Add(ctx, 1, api.WithAttributes(attribute.String("type", "other")))
	HLConsensusHeartbeatSentCounter.Add(ctx, 1, nameLabels)
	HLConsensusQCSignaturesCounter.Add(ctx, 1, nameLabels)
	HLConsensusTCBlocksCounter.Add(ctx, 1, nameLabels)
	HLConsensusTCParticipationCounter.Add(ctx, 1, nameLabels)
	HLConsensusMonitorLinesCounter.Add(ctx, 1, api.WithAttributes(attribute.String("monitor_type", "test")))
	HLConsensusMonitorErrorsCounter.Add(ctx, 1, api.WithAttributes(attribute.String("monitor_type", "test")))

	HLCoreBlockTimeHistogram.Record(ctx, 1, api.WithAttributes(attribute.String("state_type", "fast")))
	HLCoreTxPerBlockHistogram.Record(ctx, 1)
	HLCoreOrdersPerBlockHistogram.Record(ctx, 1)
	HLCoreOperationsPerBlockHistogram.Record(ctx, 1)
	HLMetalApplyDurationHistogram.Record(ctx, 1, api.WithAttributes(attribute.String("state_type", "fast")))
	HLEVMBlockTimeHistogram.Record(ctx, 1)
	HLEVMTxPerBlockHistogram.Record(ctx, 1)
	HLEVMBaseFeeHistogram.Record(ctx, 1)
	HLEVMPriorityFeeHistogram.Record(ctx, 1)
	HLConsensusQCSizeHist.Record(ctx, 1)
	HLConsensusTCSizeHist.Record(ctx, 1)
}

func TestMetricInventoryRejectsRetiredEVMAndUnboundedLabels(t *testing.T) {
	metricsDir, _ := inventoryPaths(t)
	declared, err := metricinventory.Scan(metricsDir)
	if err != nil {
		t.Fatalf("scan metric declarations: %v", err)
	}
	retiredNames := map[string]struct{}{
		"hl_consensus_heartbeat_ack_delay_ms":       {},
		"hl_consensus_heartbeat_ack_received_total": {},
		"hl_evm_contract_create_total":              {},
		"hl_evm_contract_tx_total":                  {},
	}
	forbiddenLabels := map[string]struct{}{
		"block_type":       {},
		"contract_address": {},
		"contract_name":    {},
		"is_token":         {},
		"symbol":           {},
	}
	for _, entry := range declared {
		if _, retired := retiredNames[entry.Name]; retired {
			t.Errorf("retired metric declaration returned: %s", entry.Name)
		}
		for _, label := range entry.Labels {
			if _, forbidden := forbiddenLabels[label]; forbidden {
				t.Errorf("%s exposes retired or mutable label %q", entry.Name, label)
			}
		}
	}
}

func TestMetricInventoryLazyProfiles(t *testing.T) {
	metricsDir, _ := inventoryPaths(t)
	declared, err := metricinventory.Scan(metricsDir)
	if err != nil {
		t.Fatalf("scan metric declarations: %v", err)
	}
	got := map[string][]string{}
	for _, entry := range declared {
		if entry.Profile != "base" {
			got[entry.Profile] = append(got[entry.Profile], entry.Name)
		}
	}
	for profile := range got {
		sort.Strings(got[profile])
	}
	want := map[string][]string{
		"info-probe": {
			"hl_info_endpoint_failures_total",
			"hl_info_endpoint_last_success_seconds",
			"hl_info_endpoint_latency_seconds",
			"hl_info_endpoint_up",
			"hl_info_exchange_status_delta_seconds",
			"hl_info_exchange_status_last_success_age_seconds",
			"hl_info_exchange_status_last_success_seconds",
			"hl_info_exchange_status_outcomes_total",
			"hl_info_exchange_status_up",
			"hl_info_meta_last_success_age_seconds",
			"hl_info_meta_outcomes_total",
		},
		"jailing": {
			"hl_node_jailing_dry_run",
			"hl_node_jailing_threshold_seconds",
		},
	}
	for profile := range want {
		sort.Strings(want[profile])
	}
	if diff := inventoryStringSliceMapDiff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestMetricInventoryLazyRuntimeProfiles(t *testing.T) {
	const (
		childProfileEnv = "HL_EXPORTER_LAZY_INVENTORY_CHILD_PROFILE"
		childOutputEnv  = "HL_EXPORTER_LAZY_INVENTORY_CHILD_OUTPUT"
	)
	if profile := os.Getenv(childProfileEnv); profile != "" {
		switch profile {
		case "base":
		case "info-probe":
			InitInfoProbeInstruments()
			InitInfoProbeStatusInstruments()
			InitExchangeStatusDeltaInstrument()
		case "jailing":
			InitJailingConfigInstruments()
			HLNodeJailingThresholdSeconds.WithLabelValues().Set(0)
			HLNodeJailingDryRun.WithLabelValues().Set(0)
		case "all-lazy":
			InitInfoProbeInstruments()
			InitInfoProbeStatusInstruments()
			InitExchangeStatusDeltaInstrument()
			InitJailingConfigInstruments()
			HLNodeJailingThresholdSeconds.WithLabelValues().Set(0)
			HLNodeJailingDryRun.WithLabelValues().Set(0)
		default:
			t.Fatalf("unknown lazy inventory profile %q", profile)
		}
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(families))
		for _, family := range families {
			if strings.HasPrefix(family.GetName(), "hl_") {
				names = append(names, family.GetName())
			}
		}
		sort.Strings(names)
		data, err := json.Marshal(names)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv(childOutputEnv), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

	metricsDir, _ := inventoryPaths(t)
	declared, err := metricinventory.Scan(metricsDir)
	if err != nil {
		t.Fatalf("scan metric declarations: %v", err)
	}
	lazy := map[string][]string{"info-probe": {}, "jailing": {}}
	allLazy := map[string]struct{}{}
	for _, entry := range declared {
		if entry.Profile == "base" {
			continue
		}
		lazy[entry.Profile] = append(lazy[entry.Profile], entry.Name)
		allLazy[entry.Name] = struct{}{}
	}
	for profile := range lazy {
		sort.Strings(lazy[profile])
	}
	allExpected := append(append([]string{}, lazy["info-probe"]...), lazy["jailing"]...)
	sort.Strings(allExpected)

	profiles := map[string][]string{
		"base":       {},
		"info-probe": lazy["info-probe"],
		"jailing":    lazy["jailing"],
		"all-lazy":   allExpected,
	}
	for profile, expected := range profiles {
		profile, expected := profile, expected
		t.Run(profile, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "families.json")
			command := exec.Command(os.Args[0], "-test.run=^TestMetricInventoryLazyRuntimeProfiles$", "-test.count=1")
			command.Env = append(os.Environ(), childProfileEnv+"="+profile, childOutputEnv+"="+outputPath)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("isolated %s profile failed: %v\n%s", profile, err, output)
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			var exposed []string
			if err := json.Unmarshal(data, &exposed); err != nil {
				t.Fatal(err)
			}
			actual := make([]string, 0, len(expected))
			for _, name := range exposed {
				if _, ok := allLazy[name]; ok {
					actual = append(actual, name)
				}
			}
			if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
				t.Fatalf("lazy families:\n got  %s\n want %s", strings.Join(actual, ","), strings.Join(expected, ","))
			}
		})
	}
}

func TestMetricDocsSemanticBoundaries(t *testing.T) {
	_, docsPath := inventoryPaths(t)
	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	boundaries := []string{
		"report only that every registered worker launched",
		"it is not an observation of the connected node",
		"raw `1e-8 HYPE` units",
		"does not establish committee membership",
		"not protocol response latency",
		"peer votes are leadership-sampled",
		"`RoundCatchUp` direction remains upstream-opaque",
		"not a parent, relay, block source, sync dependency, quality score, or causal signal",
		"formal unit and socket-side ownership are unresolved",
		"only in the lexicographically newest source-date directory",
		"scan the full fixed stream tree; future mtimes are excluded",
		"no universal heartbeat or refresh cadence",
		"not authoritative current HyperEVM head or current scheduled-freeze state",
		"not mempool depth or capacity",
		"Values above 1 are preserved",
		"no contract assertion",
		"does not prove receipt identity or ordering",
	}
	for _, boundary := range boundaries {
		if !strings.Contains(text, boundary) {
			t.Errorf("metrics reference lost semantic boundary %q", boundary)
		}
	}
}

func inventoryPaths(t *testing.T) (string, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve inventory test path")
	}
	metricsDir := filepath.Dir(currentFile)
	return metricsDir, filepath.Clean(filepath.Join(metricsDir, "..", "..", "docs", "metrics.md"))
}

func readDocumentedMetrics(t *testing.T, path string) map[string]documentedMetric {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metric docs: %v", err)
	}
	const begin = "<!-- BEGIN GENERATED METRIC INVENTORY -->"
	const end = "<!-- END GENERATED METRIC INVENTORY -->"
	text := string(data)
	start := strings.Index(text, begin)
	finish := strings.Index(text, end)
	if start < 0 || finish < start {
		t.Fatal("metric docs are missing generated inventory markers")
	}

	result := map[string]documentedMetric{}
	compatibility := "current"
	for _, line := range strings.Split(text[start:finish], "\n") {
		if strings.HasPrefix(line, "### Deprecated compatibility families") {
			compatibility = "deprecated; remove next major"
		}
		if !strings.HasPrefix(line, "| `hl_") {
			continue
		}
		parts := strings.Split(strings.Trim(line, "|"), "|")
		if len(parts) != 7 {
			t.Fatalf("malformed inventory row: %s", line)
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		name := strings.Trim(parts[0], "`")
		if _, duplicate := result[name]; duplicate {
			t.Fatalf("duplicate inventory row: %s", name)
		}
		labels := []string{}
		if parts[3] != "-" {
			for _, raw := range strings.Split(parts[3], ",") {
				labels = append(labels, strings.Trim(strings.TrimSpace(raw), "`"))
			}
		}
		result[name] = documentedMetric{
			kind: strings.ToLower(parts[1]), backend: parts[2], labels: labels, profile: parts[4],
			help: strings.ReplaceAll(parts[5], "&#124;", "|"), source: strings.Trim(parts[6], "`"), compatibility: compatibility,
		}
	}
	return result
}

func inventoryStringSliceMapDiff(want, got map[string][]string) string {
	var problems []string
	for key, values := range want {
		if strings.Join(values, "\x00") != strings.Join(got[key], "\x00") {
			problems = append(problems, key+": got "+strings.Join(got[key], ",")+" want "+strings.Join(values, ","))
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			problems = append(problems, "unexpected profile "+key)
		}
	}
	sort.Strings(problems)
	return strings.Join(problems, "\n")
}
