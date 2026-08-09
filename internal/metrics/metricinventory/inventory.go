// Package metricinventory extracts the public metric declaration surface from
// the metrics package. It deliberately reads source instead of importing the
// package: most collectors use the process-global Prometheus registry and some
// are registered lazily, so one in-process scrape cannot be a complete census.
package metricinventory

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Entry struct {
	Name          string
	Kind          string
	Backend       string
	Labels        []string
	Profile       string
	Help          string
	Compatibility string
	Source        string
}

type constructor struct {
	kind string
	otel bool
}

var constructors = map[string]constructor{
	"NewGauge":               {kind: "gauge"},
	"NewGaugeFunc":           {kind: "gauge"},
	"NewGaugeVec":            {kind: "gauge"},
	"Float64ObservableGauge": {kind: "gauge", otel: true},
	"Int64ObservableGauge":   {kind: "gauge", otel: true},
	"NewCounter":             {kind: "counter"},
	"NewCounterFunc":         {kind: "counter"},
	"NewCounterVec":          {kind: "counter"},
	"Float64Counter":         {kind: "counter", otel: true},
	"Int64Counter":           {kind: "counter", otel: true},
	"NewHistogram":           {kind: "histogram"},
	"NewHistogramVec":        {kind: "histogram"},
	"Float64Histogram":       {kind: "histogram", otel: true},
	"Int64Histogram":         {kind: "histogram", otel: true},
}

// OTel callback instruments acquire labels at observation time rather than at
// construction. Keep the bounded, production-emitted union explicit here;
// the docs test also rejects the high-risk labels retired by the metrics audit.
var otelLabels = map[string][]string{
	"hl_consensus_heartbeat_ack_observed":                   {"name", "signer", "validator"},
	"hl_consensus_heartbeat_sent_total":                     {"name", "signer", "validator"},
	"hl_consensus_heartbeat_status":                         {"name", "signer", "status_type", "validator"},
	"hl_consensus_monitor_errors_total":                     {"monitor_type"},
	"hl_consensus_monitor_last_processed":                   {"monitor_type"},
	"hl_consensus_monitor_lines_processed_total":            {"monitor_type"},
	"hl_consensus_proposer_count_total":                     {"name", "signer", "validator"},
	"hl_consensus_qc_participation_percent":                 {"name", "signer", "validator"},
	"hl_consensus_qc_signatures_total":                      {"name", "signer", "validator"},
	"hl_consensus_tc_blocks_total":                          {"name", "signer", "validator"},
	"hl_consensus_tc_participation_total":                   {"name", "signer", "validator"},
	"hl_consensus_validator_active_status":                  {"name", "signer", "validator"},
	"hl_consensus_validator_api_active_and_unjailed_status": {"name", "signer", "validator"},
	"hl_consensus_validator_connectivity":                   {"name", "reporter_name", "reporter_signer", "reporter_validator", "signer", "validator"},
	"hl_consensus_validator_disconnected_since_round":       {"name", "reporter_name", "reporter_signer", "reporter_validator", "signer", "validator"},
	"hl_consensus_validator_jailed_status":                  {"name", "signer", "validator"},
	"hl_consensus_validator_latency_ema_seconds":            {"name", "signer", "validator"},
	"hl_consensus_validator_latency_round":                  {"name", "signer", "validator"},
	"hl_consensus_validator_latency_seconds":                {"name", "signer", "validator"},
	"hl_consensus_validator_rtt":                            {"ip", "moniker", "validator"},
	"hl_consensus_validator_stake":                          {"name", "signer", "validator"},
	"hl_consensus_vote_round":                               {"name", "signer", "validator"},
	"hl_consensus_vote_time_diff_seconds":                   {"name", "signer", "validator"},
	"hl_core_block_time_milliseconds":                       {"state_type"},
	"hl_core_operations_total":                              {"category", "type"},
	"hl_core_tx_total":                                      {"type"},
	"hl_evm_tx_type_total":                                  {"type"},
	"hl_metal_apply_duration_milliseconds":                  {"state_type"},
	"hl_p2p_non_val_peer_connections":                       {"verified"},
	"hl_software_version":                                   {"commit", "date"},
}

var compatibilityAliases = map[string]struct{}{
	"hl_consensus_validator_rtt":            {},
	"hl_evm_db_checkpoint_height":           {},
	"hl_evm_db_checkpoint_lag_blocks":       {},
	"hl_exporter_monitor_last_tick_seconds": {},
	"hl_node_child_crashes":                 {},
	"hl_node_child_last_crash_seconds":      {},
	"hl_node_child_starts":                  {},
	"hl_node_rate_limited_files":            {},
	"hl_p2p_lz4_bytes_total":                {},
	"hl_p2p_lz4_compression_ratio":          {},
	"hl_p2p_lz4_global_bytes_total":         {},
	"hl_p2p_lz4_global_packets_total":       {},
	"hl_p2p_lz4_global_ratio":               {},
	"hl_p2p_lz4_packets_total":              {},
	"hl_p2p_non_val_connections":            {},
	"hl_p2p_non_val_peer_connections":       {},
	"hl_p2p_non_val_peers_total":            {},
	"hl_p2p_peer_count":                     {},
	"hl_p2p_peer_traffic":                   {},
	"hl_p2p_peers":                          {},
	"hl_p2p_peers_added_total":              {},
	"hl_p2p_peers_evicted_total":            {},
	"hl_p2p_sample_age_seconds":             {},
	"hl_p2p_tcp_connections":                {},
	"hl_p2p_total_traffic":                  {},
	"hl_p2p_unique_peers_seen":              {},
	"hl_visor_blocks_above_freeze":          {},
	"hl_visor_freeze_abci_height":           {},
	"hl_visor_scheduled_freeze_height":      {},
}

func Scan(dir string) ([]Entry, error) {
	fset := token.NewFileSet()
	files, err := parseMetricFiles(fset, dir)
	if err != nil {
		return nil, fmt.Errorf("parse metrics package: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("metrics package not found in %s", dir)
	}
	labelSets := collectLabelSets(files)

	entries := make(map[string]Entry)
	for fileName, file := range files {
		source := filepath.Base(fileName)
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				if err := inspectDeclaration(declaration, source, "base", labelSets, entries); err != nil {
					return nil, err
				}
			case *ast.FuncDecl:
				profile := profileForFunction(declaration.Name.Name)
				if err := inspectDeclaration(declaration.Body, source, profile, labelSets, entries); err != nil {
					return nil, err
				}
			}
		}
	}

	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	for alias := range compatibilityAliases {
		if _, ok := entries[alias]; !ok {
			return nil, fmt.Errorf("compatibility alias manifest names undeclared metric %s", alias)
		}
	}
	for name := range otelLabels {
		entry, ok := entries[name]
		if !ok {
			return nil, fmt.Errorf("OTel label manifest names undeclared metric %s", name)
		}
		if entry.Backend != "OTel bridge" {
			return nil, fmt.Errorf("OTel label manifest names non-OTel metric %s", name)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// parseMetricFiles intentionally parses every non-test Go source file without
// applying host build tags. The generated public inventory describes all
// supported platform declarations, not only the platform running generation.
func parseMetricFiles(fset *token.FileSet, dir string) (map[string]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make(map[string]*ast.File)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if file.Name.Name == "metrics" {
			files[path] = file
		}
	}
	return files, nil
}

func inspectDeclaration(node ast.Node, source, profile string, labelSets map[string][]string, entries map[string]Entry) error {
	var inspectErr error
	ast.Inspect(node, func(node ast.Node) bool {
		if inspectErr != nil {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ctor, ok := constructors[selector.Sel.Name]
		if !ok {
			return true
		}
		name := metricNameFromCall(call)
		if name == "" {
			return true
		}
		if ctor.otel && ctor.kind == "counter" && !strings.HasSuffix(name, "_total") {
			name += "_total"
		}
		labels := labelsFromCall(call, labelSets)
		if ctor.otel {
			labels = append([]string(nil), otelLabels[name]...)
		}
		sort.Strings(labels)
		compatibility := "current"
		if _, ok := compatibilityAliases[name]; ok {
			compatibility = "deprecated; remove next major"
		}
		entry := Entry{
			Name: name, Kind: ctor.kind, Backend: "Prometheus", Labels: labels, Profile: profile,
			Help: helpFromCall(call), Compatibility: compatibility, Source: source,
		}
		if ctor.otel {
			entry.Backend = "OTel bridge"
		}
		if previous, exists := entries[name]; exists {
			if previous.Kind != entry.Kind || previous.Backend != entry.Backend || strings.Join(previous.Labels, "\x00") != strings.Join(entry.Labels, "\x00") || previous.Profile != entry.Profile {
				inspectErr = fmt.Errorf("conflicting declarations for %s: %+v versus %+v", name, previous, entry)
			}
			return true
		}
		entries[name] = entry
		return true
	})
	return inspectErr
}

func profileForFunction(name string) string {
	switch name {
	case "InitInfoProbeStatusInstruments", "InitInfoProbeInstruments", "InitExchangeStatusDeltaInstrument":
		return "info-probe"
	case "InitJailingConfigInstruments":
		return "jailing"
	default:
		return "base"
	}
}

func metricNameFromCall(call *ast.CallExpr) string {
	for _, arg := range call.Args {
		if literal, ok := arg.(*ast.BasicLit); ok && literal.Kind == token.STRING {
			value, err := strconv.Unquote(literal.Value)
			if err == nil && strings.HasPrefix(value, "hl_") {
				return value
			}
		}
		composite, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}
		if value := stringField(composite, "Name"); strings.HasPrefix(value, "hl_") {
			return value
		}
	}
	return ""
}

func helpFromCall(call *ast.CallExpr) string {
	for _, arg := range call.Args {
		if composite, ok := arg.(*ast.CompositeLit); ok {
			if value := stringField(composite, "Help"); value != "" {
				return value
			}
		}
		inner, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := inner.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "WithDescription" || len(inner.Args) != 1 {
			continue
		}
		if value := stringLiteral(inner.Args[0]); value != "" {
			return value
		}
	}
	return ""
}

func labelsFromCall(call *ast.CallExpr, labelSets map[string][]string) []string {
	for _, arg := range call.Args {
		if ident, ok := arg.(*ast.Ident); ok {
			if labels, exists := labelSets[ident.Name]; exists {
				return append([]string(nil), labels...)
			}
		}
		composite, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}
		arrayType, ok := composite.Type.(*ast.ArrayType)
		if !ok {
			continue
		}
		ident, ok := arrayType.Elt.(*ast.Ident)
		if !ok || ident.Name != "string" {
			continue
		}
		labels := make([]string, 0, len(composite.Elts))
		for _, element := range composite.Elts {
			if value := stringLiteral(element); value != "" {
				labels = append(labels, value)
			}
		}
		return labels
	}
	return nil
}

func collectLabelSets(files map[string]*ast.File) map[string][]string {
	result := map[string][]string{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			valueSpec, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for index, name := range valueSpec.Names {
				if index >= len(valueSpec.Values) {
					continue
				}
				composite, ok := valueSpec.Values[index].(*ast.CompositeLit)
				if !ok {
					continue
				}
				arrayType, ok := composite.Type.(*ast.ArrayType)
				if !ok {
					continue
				}
				ident, ok := arrayType.Elt.(*ast.Ident)
				if !ok || ident.Name != "string" {
					continue
				}
				labels := make([]string, 0, len(composite.Elts))
				for _, element := range composite.Elts {
					if value := stringLiteral(element); value != "" {
						labels = append(labels, value)
					}
				}
				result[name.Name] = labels
			}
			return true
		})
	}
	return result
}

func stringField(composite *ast.CompositeLit, field string) string {
	for _, element := range composite.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != field {
			continue
		}
		return stringLiteral(pair.Value)
	}
	return ""
}

func stringLiteral(expr ast.Expr) string {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	return value
}
