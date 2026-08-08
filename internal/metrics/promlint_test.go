package metrics

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil/promlint"
)

type lintProblem struct {
	metric string
	text   string
}

const gaugeTotalLintText = `non-counter metrics should not have "_total" suffix`
const gaugeCountLintText = `non-histogram and non-summary metrics should not have "_count" suffix`

// legacyGaugeTotalExceptions is the exact frozen compatibility debt permitted
// by H09. Every entry is a pre-existing process/source-resettable gauge. New
// instruments must use a semantically correct name/type instead of extending
// this manifest.
var legacyGaugeTotalExceptions = map[string]struct{}{
	"hl_node_process_cpu_seconds_total":     {},
	"hl_node_subsystem_samples_total":       {},
	"hl_p2p_lz4_bytes_total":                {},
	"hl_p2p_lz4_global_bytes_total":         {},
	"hl_p2p_lz4_global_packets_total":       {},
	"hl_p2p_lz4_packets_total":              {},
	"hl_p2p_non_val_peers_total":            {},
	"hl_rocksdb_write_stalls_total":         {},
	"hl_tokio_task_dropped_total":           {},
	"hl_tokio_task_fast_polls_total":        {},
	"hl_tokio_task_idle_seconds_total":      {},
	"hl_tokio_task_long_delays_total":       {},
	"hl_tokio_task_poll_seconds_total":      {},
	"hl_tokio_task_polls_total":             {},
	"hl_tokio_task_scheduled_seconds_total": {},
	"hl_tokio_task_scheduled_total":         {},
	"hl_tokio_task_short_delays_total":      {},
	"hl_tokio_task_slow_polls_total":        {},
}

// hl_p2p_peer_count is a retained public compatibility name. Its value is a
// current reported-row count, so changing it to a Counter would be incorrect;
// the next major compatibility cleanup may rename it instead.
var legacyGaugeCountExceptions = map[string]struct{}{
	"hl_p2p_dominant_inbound_tie_count": {},
	"hl_p2p_peer_count":                 {},
}

func TestPrometheusMetadataLintManifest(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	problems, err := promlint.NewWithMetricFamilies(families).Lint()
	if err != nil {
		t.Fatalf("lint metrics: %v", err)
	}

	var unexpected []string
	for _, problem := range problems {
		if problem.Text == gaugeTotalLintText {
			if _, ok := legacyGaugeTotalExceptions[problem.Metric]; ok {
				continue
			}
		}
		if problem.Text == gaugeCountLintText {
			if _, ok := legacyGaugeCountExceptions[problem.Metric]; ok {
				continue
			}
		}
		unexpected = append(unexpected, formatLintProblem(lintProblem{metric: problem.Metric, text: problem.Text}))
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		t.Fatalf("unexpected Prometheus lint problems: %v", unexpected)
	}
}

func TestLegacyGaugeTotalManifestMatchesDeclarations(t *testing.T) {
	declared := declaredMetricKinds(t)
	actual := make(map[string]struct{})
	actualGaugeCounts := make(map[string]struct{})
	var badCounters, badGaugeCounts []string
	for name, kind := range declared {
		if kind == "gauge" && strings.HasSuffix(name, "_total") {
			actual[name] = struct{}{}
		}
		if kind == "gauge" && strings.HasSuffix(name, "_count") {
			actualGaugeCounts[name] = struct{}{}
			if _, ok := legacyGaugeCountExceptions[name]; !ok {
				badGaugeCounts = append(badGaugeCounts, name)
			}
		}
		// Native client_golang counters own their exposed metric name and
		// therefore must declare the _total suffix. OTel counters deliberately
		// omit it at instrument creation; the Prometheus bridge appends it when
		// translating the metric family.
		if kind == "counter" && !strings.HasSuffix(name, "_total") {
			badCounters = append(badCounters, name)
		}
	}
	if diff := stringSetDiff(legacyGaugeTotalExceptions, actual); diff != "" {
		t.Fatalf("legacy gauge-total manifest mismatch:\n%s", diff)
	}
	if diff := stringSetDiff(legacyGaugeCountExceptions, actualGaugeCounts); diff != "" {
		t.Fatalf("legacy gauge-count manifest mismatch:\n%s", diff)
	}
	if len(badGaugeCounts) > 0 {
		sort.Strings(badGaugeCounts)
		t.Fatalf("gauge names ending in _count: %v", badGaugeCounts)
	}
	if len(badCounters) > 0 {
		sort.Strings(badCounters)
		t.Fatalf("counter names missing _total: %v", badCounters)
	}
}

func declaredMetricKinds(t *testing.T) map[string]string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file")
	}
	dir := filepath.Dir(currentFile)
	packages, err := parser.ParseDir(token.NewFileSet(), dir, func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse metrics package: %v", err)
	}
	pkg := packages["metrics"]
	if pkg == nil {
		t.Fatal("parsed metrics package is missing")
	}

	constructors := map[string]string{
		"NewGauge":               "gauge",
		"NewGaugeFunc":           "gauge",
		"NewGaugeVec":            "gauge",
		"Float64ObservableGauge": "gauge",
		"Int64ObservableGauge":   "gauge",
		"NewCounter":             "counter",
		"NewCounterFunc":         "counter",
		"NewCounterVec":          "counter",
		"Float64Counter":         "otel_counter",
		"Int64Counter":           "otel_counter",
	}
	declared := make(map[string]string)
	for _, file := range pkg.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			kind, ok := constructors[selector.Sel.Name]
			if !ok {
				return true
			}
			name := metricNameFromCall(call)
			if name == "" {
				return true
			}
			if previous, exists := declared[name]; exists && previous != kind {
				t.Fatalf("metric %s declared as both %s and %s", name, previous, kind)
			}
			declared[name] = kind
			return true
		})
	}
	return declared
}

func metricNameFromCall(call *ast.CallExpr) string {
	for _, arg := range call.Args {
		if literal, ok := arg.(*ast.BasicLit); ok && literal.Kind == token.STRING {
			name, err := strconv.Unquote(literal.Value)
			if err == nil && strings.HasPrefix(name, "hl_") {
				return name
			}
		}
		composite, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range composite.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			literal, literalOK := pair.Value.(*ast.BasicLit)
			if !ok || !literalOK || key.Name != "Name" || literal.Kind != token.STRING {
				continue
			}
			name, err := strconv.Unquote(literal.Value)
			if err == nil {
				return name
			}
		}
	}
	return ""
}

func lintManifestDiff(want, got map[lintProblem]struct{}) string {
	var missing, unexpected []string
	for problem := range want {
		if _, ok := got[problem]; !ok {
			missing = append(missing, formatLintProblem(problem))
		}
	}
	for problem := range got {
		if _, ok := want[problem]; !ok {
			unexpected = append(unexpected, formatLintProblem(problem))
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	if len(missing) == 0 && len(unexpected) == 0 {
		return ""
	}
	return fmt.Sprintf("missing exceptions: %v\nunexpected problems: %v", missing, unexpected)
}

func stringSetDiff(want, got map[string]struct{}) string {
	var missing, unexpected []string
	for value := range want {
		if _, ok := got[value]; !ok {
			missing = append(missing, value)
		}
	}
	for value := range got {
		if _, ok := want[value]; !ok {
			unexpected = append(unexpected, value)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	if len(missing) == 0 && len(unexpected) == 0 {
		return ""
	}
	return fmt.Sprintf("missing: %v\nunexpected: %v", missing, unexpected)
}

func formatLintProblem(problem lintProblem) string {
	return fmt.Sprintf("%s: %s", problem.metric, problem.text)
}

func TestLintManifestRejectsUnlistedProblem(t *testing.T) {
	want := map[lintProblem]struct{}{}
	got := map[lintProblem]struct{}{{metric: "hl_new_bad_total", text: "new violation"}: {}}
	if diff := lintManifestDiff(want, got); diff == "" {
		t.Fatal("unlisted lint problem was accepted")
	}
}
