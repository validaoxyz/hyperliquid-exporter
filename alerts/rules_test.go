package alerts_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type alertRule struct {
	name        string
	expr        string
	forDuration string
	labels      map[string]string
	annotations map[string]string
}

type ruleContract struct {
	expr        string
	forDuration string
	severity    string
}

var approvedRuleContracts = map[string]map[string]ruleContract{
	"hyperliquid-exporter.rules.yml": {
		"HyperliquidExporterDown": {
			expr:        `up{job=~"hyperliquid-exporter(/.*)?"} == 0`,
			forDuration: "2m",
			severity:    "critical",
		},
		"HyperliquidExporterNotReady": {
			expr:        `(hl_exporter_ready == 0) and on(job, instance) (up{job=~"hyperliquid-exporter(/.*)?"} == 1)`,
			forDuration: "5m",
			severity:    "warning",
		},
		"HyperliquidExporterMonitorExited": {
			expr:        `(hl_exporter_monitor_registered == 1) and (hl_exporter_monitor_running == 0) and (hl_exporter_monitor_exited_seconds > 0)`,
			forDuration: "2m",
			severity:    "warning",
		},
		"HyperliquidExporterMonitorPanicked": {
			expr:     `increase(hl_exporter_monitor_panics_total[10m]) > 0`,
			severity: "warning",
		},
		"HyperliquidExporterErrorReportsDropped": {
			expr:     `increase(hl_exporter_monitor_error_drops_total[10m]) > 0`,
			severity: "warning",
		},
		"HyperliquidExporterSourceReadOrSchemaFailed": {
			expr:        `(hl_exporter_source_enabled == 1) and on(job, instance, source) ( (hl_exporter_source_read_ok == 0) or (hl_exporter_source_schema_ok == 0) )`,
			forDuration: "5m",
			severity:    "warning",
		},
	},
	"hyperliquid-linux.rules.yml": {
		"HyperliquidNodeProcessDown": {
			expr:        `(hl_node_process_up{process="hl-node"} == 0) and on(job, instance) (hl_exporter_source_enabled{source="process"} == 1) and on(job, instance) (hl_exporter_source_read_ok{source="process"} == 1) and on(job, instance) (hl_exporter_source_schema_ok{source="process"} == 1)`,
			forDuration: "2m",
			severity:    "critical",
		},
		"HyperliquidVisorProcessDown": {
			expr:        `(hl_node_process_up{process="hl-visor"} == 0) and on(job, instance) (hl_exporter_source_enabled{source="process"} == 1) and on(job, instance) (hl_exporter_source_read_ok{source="process"} == 1) and on(job, instance) (hl_exporter_source_schema_ok{source="process"} == 1)`,
			forDuration: "2m",
			severity:    "critical",
		},
	},
	"hyperliquid-extended.rules.yml": {
		"HyperliquidTmpMaterialStale": {
			expr:        `(hl_node_tmp_scan_up == 1) and (hl_node_tmp_material_stale_bytes > 0)`,
			forDuration: "15m",
			severity:    "warning",
		},
	},
	"hyperliquid-evm.rules.yml": {
		"HyperliquidEVMTxReceiptCountMismatch": {
			expr:     `increase(hl_evm_tx_receipt_count_mismatches_total[10m]) > 0`,
			severity: "warning",
		},
		"HyperliquidEVMParseErrors": {
			expr:     `sum by(job, instance, stage, reason) ( increase(hl_evm_parse_errors_total[10m]) ) > 0`,
			severity: "warning",
		},
	},
	"hyperliquid-validator-api.rules.yml": {
		"HyperliquidValidatorAPIStaleFallback": {
			expr:        `(hl_validator_api_up == 0) and (hl_validator_api_cache_stale == 1)`,
			forDuration: "5m",
			severity:    "warning",
		},
	},
}

var (
	alertStartRE = regexp.MustCompile(`^\s*-\s+alert:\s*([A-Za-z][A-Za-z0-9]*)\s*$`)
	metricRefRE  = regexp.MustCompile(`\b(?:hl_[A-Za-z0-9_]+|up)\b`)
	functionRE   = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	counterUseRE = regexp.MustCompile(`\b(increase|rate)\s*\(\s*(hl_[A-Za-z0-9_]+)`)
	groupingRE   = regexp.MustCompile(`\b(?:by|without|on|ignoring)\s*\(([^)]*)\)`)
)

func TestRulePacksMatchApprovedContracts(t *testing.T) {
	for filename, contracts := range approvedRuleContracts {
		filename, contracts := filename, contracts
		t.Run(filename, func(t *testing.T) {
			rules := parseRuleFile(t, filepath.Join(".", filename))
			if len(rules) != len(contracts) {
				t.Fatalf("got %d rules, want %d", len(rules), len(contracts))
			}

			seen := make(map[string]bool, len(rules))
			for _, rule := range rules {
				contract, ok := contracts[rule.name]
				if !ok {
					t.Errorf("unapproved rule %q", rule.name)
					continue
				}
				if seen[rule.name] {
					t.Errorf("duplicate rule %q", rule.name)
				}
				seen[rule.name] = true
				if got, want := normalizeExpr(rule.expr), normalizeExpr(contract.expr); got != want {
					t.Errorf("%s expression mismatch\n got: %s\nwant: %s", rule.name, got, want)
				}
				if rule.forDuration != contract.forDuration {
					t.Errorf("%s for=%q, want %q", rule.name, rule.forDuration, contract.forDuration)
				}
				if got := rule.labels["severity"]; got != contract.severity {
					t.Errorf("%s severity=%q, want %q", rule.name, got, contract.severity)
				}
				if len(rule.labels) != 1 {
					t.Errorf("%s has unapproved static labels: %v", rule.name, sortedKeys(rule.labels))
				}
				if strings.TrimSpace(rule.annotations["summary"]) == "" || strings.TrimSpace(rule.annotations["description"]) == "" {
					t.Errorf("%s must have nonempty summary and description", rule.name)
				}
				if len(rule.annotations) != 2 {
					t.Errorf("%s has unexpected annotations: %v", rule.name, sortedKeys(rule.annotations))
				}
			}
			for name := range contracts {
				if !seen[name] {
					t.Errorf("approved rule %q missing", name)
				}
			}
		})
	}
}

func TestRuleMetricAndSafetyContracts(t *testing.T) {
	metricTypes := declaredPrometheusMetricTypes(t, filepath.Join("..", "internal", "metrics"))
	allowedFunctions := map[string]bool{
		"and":      true,
		"by":       true,
		"increase": true,
		"on":       true,
		"or":       true,
		"rate":     true,
		"sum":      true,
		"time":     true,
	}
	bannedAnnotationRE := regexp.MustCompile(`(?i)\b(?:restart|reboot|kill|remediat(?:e|ion|ing)|wrong[- ]chain|parent|committee|offender)\b`)
	rawAnnotationIdentityRE := regexp.MustCompile(`(?i)\$labels\.(?:ip|address)\b`)

	for filename := range approvedRuleContracts {
		rules := parseRuleFile(t, filepath.Join(".", filename))
		for _, rule := range rules {
			rule := rule
			t.Run(rule.name, func(t *testing.T) {
				expr := normalizeExpr(rule.expr)
				if strings.Contains(strings.ToLower(expr), "absent(") {
					t.Fatal("absent() is not approved for these packs")
				}

				for _, match := range functionRE.FindAllStringSubmatch(stripQuotedStrings(expr), -1) {
					if !allowedFunctions[match[1]] {
						t.Errorf("unapproved PromQL function or modifier %q", match[1])
					}
				}

				refs := uniqueStrings(metricRefRE.FindAllString(expr, -1))
				for _, metric := range refs {
					if metric == "up" {
						continue
					}
					metricType, ok := metricTypes[metric]
					if !ok {
						t.Errorf("referenced family %q is absent from internal/metrics declarations", metric)
						continue
					}
					if metricType == "counter" && !regexp.MustCompile(`\b(?:increase|rate)\s*\(\s*`+regexp.QuoteMeta(metric)+`(?:\{|\[)`).MatchString(expr) {
						t.Errorf("counter %q is not used through increase()/rate()", metric)
					}
				}

				for _, match := range counterUseRE.FindAllStringSubmatch(expr, -1) {
					metric, ok := metricTypes[match[2]]
					if !ok {
						t.Errorf("counter function references undeclared metric %q", match[2])
						continue
					}
					if metric != "counter" {
						t.Errorf("%s() applied to non-counter %q of type %s", match[1], match[2], metric)
					}
				}

				for _, match := range groupingRE.FindAllStringSubmatch(expr, -1) {
					for _, label := range strings.Split(match[1], ",") {
						normalizedLabel := strings.ToLower(strings.TrimSpace(label))
						if normalizedLabel == "ip" || normalizedLabel == "address" {
							t.Errorf("raw IP/address label in vector matching or grouping: %q", label)
						}
					}
				}

				for key, text := range rule.annotations {
					if bannedAnnotationRE.MatchString(text) {
						t.Errorf("%s annotation contains a prohibited inference or action verb: %q", key, text)
					}
					if rawAnnotationIdentityRE.MatchString(text) {
						t.Errorf("%s annotation exposes a raw IP/address field: %q", key, text)
					}
				}
			})
		}
	}
}

func TestPromtoolFixturesCoverEveryRule(t *testing.T) {
	for filename, contracts := range approvedRuleContracts {
		fixture := filepath.Join("tests", strings.TrimSuffix(filename, ".rules.yml")+".test.yml")
		contents, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatalf("read %s: %v", fixture, err)
		}
		text := string(contents)
		if strings.Contains(text, "\t") {
			t.Errorf("%s contains a tab; YAML fixtures use spaces only", fixture)
		}
		if !strings.Contains(text, "- ../"+filename) {
			t.Errorf("%s does not load ../%s relative to the fixture", fixture, filename)
		}
		for alertName := range contracts {
			if !strings.Contains(text, "name: "+alertName+" firing") {
				t.Errorf("%s lacks an explicit firing case for %s", fixture, alertName)
			}
			if !strings.Contains(text, "name: "+alertName+" nonfiring") {
				t.Errorf("%s lacks an explicit nonfiring case for %s", fixture, alertName)
			}
			if count := strings.Count(text, "alertname: "+alertName); count < 2 {
				t.Errorf("%s has %d alert assertions for %s, want at least 2", fixture, count, alertName)
			}
		}
	}

	for _, alertName := range []string{
		"HyperliquidExporterMonitorPanicked",
		"HyperliquidExporterErrorReportsDropped",
		"HyperliquidEVMTxReceiptCountMismatch",
		"HyperliquidEVMParseErrors",
	} {
		found := false
		entries, err := filepath.Glob(filepath.Join("tests", "*.test.yml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			contents, readErr := os.ReadFile(entry)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(contents), "name: "+alertName+" nonfiring reset and absent") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("counter alert %s lacks a reset-only and absent-series nonfiring fixture", alertName)
		}
	}
}

func parseRuleFile(t *testing.T, path string) []alertRule {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	var (
		rules         []alertRule
		current       *alertRule
		section       string
		sectionIndent int
		exprBlock     bool
		exprIndent    int
		exprLines     []string
	)
	finishExpr := func() {
		if current != nil && exprBlock {
			current.expr = strings.Join(exprLines, "\n")
		}
		exprBlock = false
		exprLines = nil
	}
	finishRule := func() {
		finishExpr()
		if current != nil {
			rules = append(rules, *current)
		}
		current = nil
	}

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if strings.Contains(line, "\t") {
			t.Fatalf("%s:%d contains a tab", path, lineNumber)
		}
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if match := alertStartRE.FindStringSubmatch(line); match != nil {
			finishRule()
			current = &alertRule{
				name:        match[1],
				labels:      make(map[string]string),
				annotations: make(map[string]string),
			}
			section = ""
			continue
		}
		if current == nil {
			continue
		}

		if exprBlock {
			if trimmed == "" || indent > exprIndent {
				if trimmed != "" {
					exprLines = append(exprLines, trimmed)
				}
				continue
			}
			finishExpr()
		}

		switch {
		case trimmed == "expr: |":
			exprBlock = true
			exprIndent = indent
		case strings.HasPrefix(trimmed, "for:"):
			current.forDuration = strings.TrimSpace(strings.TrimPrefix(trimmed, "for:"))
			section = ""
		case trimmed == "labels:":
			section = "labels"
			sectionIndent = indent
		case trimmed == "annotations:":
			section = "annotations"
			sectionIndent = indent
		case section != "" && indent > sectionIndent:
			key, value, ok := strings.Cut(trimmed, ":")
			if !ok {
				t.Fatalf("%s:%d malformed %s entry", path, lineNumber, section)
			}
			value = parseYAMLScalar(t, path, lineNumber, strings.TrimSpace(value))
			if section == "labels" {
				current.labels[strings.TrimSpace(key)] = value
			} else {
				current.annotations[strings.TrimSpace(key)] = value
			}
		default:
			section = ""
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	finishRule()
	if len(rules) == 0 {
		t.Fatalf("%s contained no alert rules", path)
	}
	return rules
}

func parseYAMLScalar(t *testing.T, path string, line int, value string) string {
	t.Helper()
	if strings.HasPrefix(value, `"`) {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			t.Fatalf("%s:%d invalid quoted scalar: %v", path, line, err)
		}
		return decoded
	}
	return value
}

func normalizeExpr(expr string) string {
	return strings.Join(strings.Fields(expr), " ")
}

func stripQuotedStrings(value string) string {
	var result strings.Builder
	quoted := false
	escaped := false
	for _, char := range value {
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				quoted = false
			}
			continue
		}
		if char == '"' {
			quoted = true
			result.WriteByte(' ')
			continue
		}
		result.WriteRune(char)
	}
	return result.String()
}

func declaredPrometheusMetricTypes(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	metricTypes := make(map[string]string)
	for _, entry := range entries {
		if strings.HasSuffix(entry, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), entry, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", entry, parseErr)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			constructor := callName(call.Fun)
			metricType := ""
			switch constructor {
			case "NewCounter", "NewCounterVec":
				metricType = "counter"
			case "NewGauge", "NewGaugeVec":
				metricType = "gauge"
			case "NewHistogram", "NewHistogramVec":
				metricType = "histogram"
			case "NewSummary", "NewSummaryVec":
				metricType = "summary"
			default:
				return true
			}
			name := metricNameFromOptions(call.Args[0])
			if name == "" || !strings.HasPrefix(name, "hl_") {
				return true
			}
			if previous, exists := metricTypes[name]; exists && previous != metricType {
				t.Errorf("metric %s declared as both %s and %s", name, previous, metricType)
			}
			metricTypes[name] = metricType
			return true
		})
	}
	return metricTypes
}

func callName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.Ident:
		return value.Name
	default:
		return ""
	}
}

func metricNameFromOptions(expr ast.Expr) string {
	composite, ok := expr.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	for _, element := range composite.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := keyValue.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		literal, ok := keyValue.Value.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return ""
		}
		name, err := strconv.Unquote(literal.Value)
		if err != nil {
			return ""
		}
		return name
	}
	return ""
}

func uniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestNoConditionalPolicyRulesSlippedIntoThePack(t *testing.T) {
	for filename := range approvedRuleContracts {
		contents, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		text := string(contents)
		for _, forbidden := range []string{
			"hl_node_disk_free_bytes",
			"hl_node_process_open_fds_ratio",
			"hl_node_snapshot_height_lag_blocks",
			"hl_p2p_dominant_inbound_",
			"hl_node_rate_limit_",
			"hl_info_",
		} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s references conditionally blocked family %s", filename, forbidden)
			}
		}
	}
}
