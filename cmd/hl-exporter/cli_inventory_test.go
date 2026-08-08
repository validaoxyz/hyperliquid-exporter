package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type cliFlag struct {
	typeName string
	defValue string
	help     string
}

func TestStartCLIInventoryMatchesDocs(t *testing.T) {
	fs, _ := newStartFlagSet(flag.ContinueOnError, io.Discard)
	want := visitFlags(fs)
	if len(want) != 23 {
		t.Fatalf("start flags = %d, want 23", len(want))
	}
	for _, pathMarker := range []struct {
		path, begin, end string
	}{
		{"README.md", "<!-- BEGIN START FLAG INVENTORY -->", "<!-- END START FLAG INVENTORY -->"},
		{"UPGRADING.md", "<!-- BEGIN CURRENT START FLAG INVENTORY -->", "<!-- END CURRENT START FLAG INVENTORY -->"},
	} {
		path := repoFile(t, pathMarker.path)
		got := readFlagTable(t, path, pathMarker.begin, pathMarker.end)
		if diff := cliInventoryDiff(want, got); diff != "" {
			t.Fatalf("%s start flag inventory drift:\n%s", pathMarker.path, diff)
		}
	}
}

func TestValsCLIInventory(t *testing.T) {
	t.Setenv("NODE_HOME", "/inventory/hl")
	fs, _ := newValsFlagSet(flag.ContinueOnError, io.Discard)
	got := visitFlags(fs)
	want := map[string]cliFlag{
		"addr":             {"string", "0.0.0.0:8087", ""},
		"backfill":         {"bool", "false", ""},
		"chain":            {"string", "testnet", ""},
		"interval":         {"duration", "1h0m0s", ""},
		"node-home":        {"string", "/inventory/hl", ""},
		"out":              {"string", "", ""},
		"peer-counter-url": {"string", "http://127.0.0.1:19046/snapshot", ""},
		"serve":            {"bool", "false", ""},
		"since":            {"string", "2025-05-31", ""},
		"sleep":            {"duration", "2s", ""},
	}
	for name, expected := range want {
		actual, ok := got[name]
		if !ok {
			t.Errorf("missing vals flag %s", name)
			continue
		}
		if actual.typeName != expected.typeName || actual.defValue != expected.defValue {
			t.Errorf("vals flag %s = %s/%q, want %s/%q", name, actual.typeName, actual.defValue, expected.typeName, expected.defValue)
		}
	}
	if len(got) != len(want) {
		t.Errorf("vals flags = %d, want %d", len(got), len(want))
	}
}

func TestValsCLIInventoryMatchesDocs(t *testing.T) {
	t.Setenv("NODE_HOME", "/inventory/hl")
	fs, _ := newValsFlagSet(flag.ContinueOnError, io.Discard)
	want := visitFlags(fs)
	got := readFlagTable(t, repoFile(t, "README.md"), "<!-- BEGIN VALS FLAG INVENTORY -->", "<!-- END VALS FLAG INVENTORY -->")
	if got["node-home"].typeName != "string" || got["node-home"].defValue != "environment-derived" {
		t.Fatalf("README node-home default must be documented as environment-derived: %+v", got["node-home"])
	}
	delete(got, "node-home")
	delete(want, "node-home")
	if diff := cliInventoryDiff(want, got); diff != "" {
		t.Fatalf("README vals flag inventory drift:\n%s", diff)
	}
}

func TestStartCLIHelpSemanticBoundaries(t *testing.T) {
	fs, _ := newStartFlagSet(flag.ContinueOnError, io.Discard)
	checks := map[string][]string{
		"contract-metrics":  {"canonical recipient-address", "no contract identity"},
		"per-peer-metrics":  {"16 current explicit child identities", "child_peers"},
		"validator-rtt":     {"TCP-connect", "not protocol RTT"},
		"disable-tcp6":      {"unavailable enabled source", "unhealthy"},
		"tcp-service-ports": {"1..16"},
	}
	for name, fragments := range checks {
		entry := fs.Lookup(name)
		if entry == nil {
			t.Fatalf("missing start flag %s", name)
		}
		for _, fragment := range fragments {
			if !strings.Contains(entry.Usage, fragment) {
				t.Errorf("%s help %q is missing %q", name, entry.Usage, fragment)
			}
		}
	}
	for _, retired := range []string{"evm-block-type-metrics", "evm", "enable-otlp", "enable-prom", "disable-prom"} {
		if fs.Lookup(retired) != nil {
			t.Errorf("retired flag returned: %s", retired)
		}
	}
}

func TestRootUsageListsEveryStartFlag(t *testing.T) {
	fs, _ := newStartFlagSet(flag.ContinueOnError, io.Discard)
	var output bytes.Buffer
	printRootUsage(&output)
	text := output.String()
	fs.VisitAll(func(entry *flag.Flag) {
		if !strings.Contains(text, "-"+entry.Name) {
			t.Errorf("root usage omitted -%s", entry.Name)
		}
	})
	for _, command := range []string{"start", "vals", "version"} {
		if !strings.Contains(text, command) {
			t.Errorf("root usage omitted command %s", command)
		}
	}
}

func TestCLIHelpAndVersionSmoke(t *testing.T) {
	const childEnv = "HL_EXPORTER_CLI_SMOKE_CHILD"
	if scenario := os.Getenv(childEnv); scenario != "" {
		switch scenario {
		case "root":
			os.Args = []string{"hl_exporter"}
		case "start-help":
			os.Args = []string{"hl_exporter", "start", "-h"}
		case "vals-help":
			os.Args = []string{"hl_exporter", "vals", "-h"}
		case "version":
			os.Args = []string{"hl_exporter", "version"}
		default:
			t.Fatalf("unknown CLI smoke scenario %q", scenario)
		}
		main()
		return
	}

	tests := []struct {
		name       string
		exitCode   int
		fragments  []string
		flagSource *flag.FlagSet
	}{
		{name: "root", exitCode: 1, fragments: []string{"Usage: hl_exporter <command> [options]", "start", "vals", "version"}},
		{name: "start-help", fragments: []string{"Usage of start:"}, flagSource: mustStartFlagSet()},
		{name: "vals-help", fragments: []string{"Usage of vals:"}, flagSource: mustValsFlagSet()},
		{name: "version", fragments: []string{"hl_exporter dev (commit unknown, go"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestCLIHelpAndVersionSmoke$", "-test.count=1")
			command.Env = append(os.Environ(), childEnv+"="+test.name)
			output, err := command.CombinedOutput()
			actualExit := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run CLI smoke: %v", err)
				}
				actualExit = exitErr.ExitCode()
			}
			if actualExit != test.exitCode {
				t.Fatalf("exit code = %d, want %d\n%s", actualExit, test.exitCode, output)
			}
			text := string(output)
			for _, fragment := range test.fragments {
				if !strings.Contains(text, fragment) {
					t.Errorf("output is missing %q\n%s", fragment, text)
				}
			}
			if test.flagSource != nil {
				test.flagSource.VisitAll(func(entry *flag.Flag) {
					if !strings.Contains(text, "-"+entry.Name) {
						t.Errorf("help omitted -%s", entry.Name)
					}
				})
			}
		})
	}
}

func mustStartFlagSet() *flag.FlagSet {
	fs, _ := newStartFlagSet(flag.ContinueOnError, io.Discard)
	return fs
}

func mustValsFlagSet() *flag.FlagSet {
	fs, _ := newValsFlagSet(flag.ContinueOnError, io.Discard)
	return fs
}

func TestArchivedV3RelabelRegex(t *testing.T) {
	data, err := os.ReadFile(repoFile(t, "UPGRADING.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	begin := strings.Index(text, "<!-- BEGIN ARCHIVED V3 RELABEL REGEX -->")
	end := strings.Index(text, "<!-- END ARCHIVED V3 RELABEL REGEX -->")
	if begin < 0 || end < begin {
		t.Fatal("missing archived v3 relabel regex markers")
	}
	match := regexp.MustCompile("(?s)```regex\\s*([^\\n]+)\\s*```").FindStringSubmatch(text[begin:end])
	if len(match) != 2 {
		t.Fatal("could not parse archived v3 relabel regex")
	}
	re, err := regexp.Compile(match[1])
	if err != nil {
		t.Fatalf("compile archived regex: %v", err)
	}
	positive := []string{
		"hl_visor_height", "hl_p2p_tcp_connections", "hl_node_snapshot_last_height",
		"hl_node_process_up", "hl_node_disk_free_bytes", "hl_node_bugs", "hl_node_crits",
		"hl_node_crit_locations", "hl_node_observed_runs", "hl_node_observed_run_start_seconds",
		"hl_node_subsystem_latency_mean_seconds", "hl_node_parent_peer_info",
		"hl_evm_db_checkpoint_height", "hl_exporter_ready",
	}
	negative := []string{
		"hl_p2p_tcp_connections_extra", "hl_node_snapshot", "hl_node_observed_run_start_seconds_extra",
		"hl_exporterx_ready", "hl_core_block_height", "hl_evm_block_height",
	}
	for _, value := range positive {
		if !re.MatchString(value) {
			t.Errorf("archived regex should match %s", value)
		}
	}
	for _, value := range negative {
		if re.MatchString(value) {
			t.Errorf("archived regex should not match %s", value)
		}
	}
}

func TestCurrentDocumentationExamplesUseKnownFlags(t *testing.T) {
	startFlags := mustStartFlagSet()
	valsFlags := mustValsFlagSet()
	codeBlocks := regexp.MustCompile("(?ms)^```(?:bash|sh|ini)\\s*\\n(.*?)^```$")
	invocation := regexp.MustCompile(`hl_exporter\s+(start|vals|version)\b([^\n]*)`)

	for _, name := range []string{"README.md", "UPGRADING.md"} {
		data, err := os.ReadFile(repoFile(t, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if name == "UPGRADING.md" {
			text = strings.Split(text, "## Archived release migrations")[0]
		}
		for _, block := range codeBlocks.FindAllStringSubmatch(text, -1) {
			normalized := strings.ReplaceAll(block[1], "\\\n", " ")
			for _, match := range invocation.FindAllStringSubmatch(normalized, -1) {
				var fs *flag.FlagSet
				switch match[1] {
				case "start":
					fs = startFlags
				case "vals":
					fs = valsFlags
				case "version":
					continue
				}
				for _, token := range strings.Fields(match[2]) {
					if !strings.HasPrefix(token, "--") {
						continue
					}
					flagName := strings.TrimPrefix(strings.SplitN(token, "=", 2)[0], "--")
					if fs.Lookup(flagName) == nil {
						t.Errorf("%s %s example uses unknown flag --%s", name, match[1], flagName)
					}
				}
			}
		}
	}
}

func TestCurrentDocumentationOmitsRetiredFlags(t *testing.T) {
	for _, name := range []string{"README.md", "docs/metrics.md", "UPGRADING.md"} {
		data, err := os.ReadFile(repoFile(t, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if name == "UPGRADING.md" {
			text = strings.Split(text, "## Archived release migrations")[0]
		}
		for _, retired := range []string{"evm-block-type-metrics", "evm", "enable-otlp", "enable-prom", "disable-prom"} {
			pattern := regexp.MustCompile(`(^|[^[:alnum:]-])--` + regexp.QuoteMeta(retired) + `([^[:alnum:]-]|$)`)
			if pattern.MatchString(text) {
				t.Errorf("%s current documentation contains retired flag --%s", name, retired)
			}
		}
	}
}

func visitFlags(fs *flag.FlagSet) map[string]cliFlag {
	result := map[string]cliFlag{}
	fs.VisitAll(func(entry *flag.Flag) {
		result[entry.Name] = cliFlag{
			typeName: flagType(entry),
			defValue: entry.DefValue,
			help:     normalizeHelp(entry.Usage),
		}
	})
	return result
}

func flagType(entry *flag.Flag) string {
	getter, ok := entry.Value.(flag.Getter)
	if !ok {
		return "unknown"
	}
	switch getter.Get().(type) {
	case bool:
		return "bool"
	case int:
		return "int"
	case string:
		return "string"
	case time.Duration:
		return "duration"
	default:
		return "unknown"
	}
}

func normalizeHelp(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func readFlagTable(t *testing.T, path, beginMarker, endMarker string) map[string]cliFlag {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	begin := strings.Index(text, beginMarker)
	end := strings.Index(text, endMarker)
	if begin < 0 || end < begin {
		t.Fatalf("%s is missing flag inventory markers", path)
	}
	result := map[string]cliFlag{}
	for _, line := range strings.Split(text[begin:end], "\n") {
		if !strings.HasPrefix(line, "| `--") {
			continue
		}
		parts := strings.Split(strings.Trim(line, "|"), "|")
		if len(parts) < 3 {
			t.Fatalf("malformed flag row: %s", line)
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		name := strings.TrimPrefix(strings.Trim(parts[0], "`"), "--")
		typeName := parts[1]
		defValue := strings.Trim(parts[2], "`")
		if typeName == "string" && strings.HasPrefix(defValue, "\"") {
			decoded, err := strconv.Unquote(defValue)
			if err != nil {
				t.Fatalf("decode default for %s: %v", name, err)
			}
			defValue = decoded
		}
		if _, duplicate := result[name]; duplicate {
			t.Fatalf("duplicate flag row %s", name)
		}
		result[name] = cliFlag{typeName: typeName, defValue: defValue}
	}
	return result
}

func cliInventoryDiff(want, got map[string]cliFlag) string {
	var problems []string
	for name, expected := range want {
		actual, ok := got[name]
		if !ok {
			problems = append(problems, "missing --"+name)
			continue
		}
		if actual.typeName != expected.typeName || actual.defValue != expected.defValue {
			problems = append(problems, "--"+name+" got "+actual.typeName+"/"+strconv.Quote(actual.defValue)+" want "+expected.typeName+"/"+strconv.Quote(expected.defValue))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			problems = append(problems, "unexpected --"+name)
		}
	}
	sort.Strings(problems)
	return strings.Join(problems, "\n")
}

func repoFile(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve cli inventory test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", name))
}
