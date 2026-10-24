package monitors

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

func TestOptionalStreamRecordBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       bool
	}{
		{"object", "{\"ok\":1}\n", true},
		{"array", "[null,true,1,\"x\",{},[]]\n", true},
		{"crlf", " \t{\"ok\":1}\r\n", true},
		{"blank suffix", "{} \t\r\n\n \t\r\n", true},
		{"multiple records", "{bad}\n{\"ok\":1}\n", true},
		{"escaped newline", "{\"value\":\"a\\nb\"}\n", true},
		{"empty", "", false},
		{"blank", " \t\r\n\n", false},
		{"null", "null\n", false},
		{"boolean", "true\n", false},
		{"number", "123\n", false},
		{"string", "\"string\"\n", false},
		{"missing newline", "{\"ok\":1}", false},
		{"incomplete last record", "{\"ok\":1}\n{\"pending\":", false},
		{"malformed last record", "{\"ok\":1}\n{\"bad\":}\n", false},
		{"multiple values", "{} []\n", false},
		{"trailing junk", "{} junk\n", false},
		{"invalid escape", "{\"x\":\"\\q\"}\n", false},
		{"multiline JSON", "{\n\"x\":1\n}\n", false},
		{"valid suffix of malformed record", "junk" + strings.Repeat(" ", 256<<10) + "{}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid, err := validateOptionalStreamRecord(strings.NewReader(tc.data), int64(len(tc.data)))
			if err != nil || valid != tc.want {
				t.Fatalf("valid=%v err=%v, want %v", valid, err, tc.want)
			}
		})
	}
}

// This reader synthesizes large whitespace records without keeping another
// record-sized allocation beside the validator's buffer.
type optionalRecordTestReader struct {
	size, readBytes int64
	prefix, suffix  string
}

func (r *optionalRecordTestReader) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 || offset >= r.size {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.size-offset)
	for i := int64(0); i < n; i++ {
		position := offset + i
		switch {
		case position < int64(len(r.prefix)):
			p[i] = r.prefix[position]
		case position >= r.size-int64(len(r.suffix)):
			p[i] = r.suffix[position-(r.size-int64(len(r.suffix)))]
		default:
			p[i] = ' '
		}
	}
	r.readBytes += n
	if n != int64(len(p)) {
		return int(n), io.EOF
	}
	return int(n), nil
}

func TestOptionalStreamRecordLimits(t *testing.T) {
	for _, tc := range []struct {
		name           string
		size           int64
		prefix, suffix string
		want           bool
	}{
		{"exact cap at start", optionalStreamRecordLimit + 1, "{", "}\n", true},
		{"exact cap after separator", optionalStreamRecordLimit + 4, "{}\n{", "}\n", true},
		{"cap with blank suffix", optionalStreamRecordLimit + 6, "{", "}\n\n \t\r\n", true},
		{"over cap", optionalStreamRecordLimit + 2, "{", "}\n", false},
		{"over cap with trailing space", optionalStreamRecordLimit + 2, "{", "} \n", false},
		{"valid suffix without separator", optionalStreamScanLimit + 10, "junk", "{}\n", false},
		{"blank suffix exceeds scan", optionalStreamScanLimit + 10, "{}\n", "\n", false},
		{"small record after large prefix", optionalStreamScanLimit + 10, "junk", "\n{}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &optionalRecordTestReader{size: tc.size, prefix: tc.prefix, suffix: tc.suffix}
			valid, err := validateOptionalStreamRecord(r, r.size)
			if err != nil || valid != tc.want {
				t.Fatalf("valid=%v err=%v, want %v", valid, err, tc.want)
			}
			limit := optionalStreamScanLimit + 1
			if tc.want {
				limit += optionalStreamRecordLimit
			}
			if r.readBytes > limit {
				t.Fatalf("read %d bytes beyond bound %d", r.readBytes, limit)
			}
		})
	}
}

func TestOptionalStreamLargeFundingRecord(t *testing.T) {
	// This matches the largest record size observed on 2026-10-24. All values
	// are synthetic; only the Funding shape and total byte count are retained.
	const recordBytes = int64(44443849)
	path := filepath.Join(t.TempDir(), "funding")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(f, 64<<10)
	prefix := `{"hash":"0x0000","time":"2026-10-24T12:00:00","inner":{"Funding":{"deltas":[`
	entry := `{"user":"0x0000000000000000000000000000000000000000","coin":"HYPE","funding_amount":"0.123","szi":"2.5","funding_rate":"0.001"}`
	suffix := "]}}}\n"
	remaining := recordBytes - int64(len(prefix)+len(suffix))
	count := (remaining + 1) / int64(len(entry)+1)
	padding := remaining - count*int64(len(entry)+1) + 1
	fmt.Fprint(writer, prefix)
	for i := int64(0); i < count; i++ {
		if i > 0 {
			writer.WriteByte(',')
		}
		fmt.Fprint(writer, entry)
	}
	fmt.Fprint(writer, strings.Repeat(" ", int(padding)), suffix)
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tail := make([]byte, 256<<10)
	if _, err := f.ReadAt(tail, recordBytes-int64(len(tail))); err != nil {
		t.Fatal(err)
	}
	if json.Valid(bytes.TrimSpace(tail)) {
		t.Fatal("fixture did not reproduce rejection of a partial record suffix")
	}
	valid, info, err := optionalStreamHasCompleteRecord(path)
	if err != nil || !valid || info.Size() != recordBytes {
		t.Fatalf("large record valid=%v info=%v err=%v", valid, info, err)
	}
}

func TestOptionalStreamDescriptorSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hour")
	oldTime := time.Unix(1700000000, 0)
	if err := os.WriteFile(path, []byte("{\"old\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{malformed}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if valid, err := validateOptionalStreamRecord(f, info.Size()); !valid || err != nil {
		t.Fatalf("held descriptor after rotation: valid=%v err=%v", valid, err)
	}
	valid, replacementInfo, err := optionalStreamHasCompleteRecord(path)
	if valid || err != nil || os.SameFile(info, replacementInfo) || replacementInfo.ModTime().Equal(oldTime) {
		t.Fatalf("replacement snapshot valid=%v info=%v err=%v", valid, replacementInfo, err)
	}
	appendFile, err := os.OpenFile(path+".old", os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	appended := false
	appendDuringRead := optionalRecordReaderAtFunc(func(p []byte, offset int64) (int, error) {
		n, err := f.ReadAt(p, offset)
		if !appended {
			if _, writeErr := appendFile.WriteString("{\"unfinished\":"); writeErr != nil {
				t.Fatal(writeErr)
			}
			appended = true
		}
		return n, err
	})
	if valid, err := validateOptionalStreamRecord(appendDuringRead, info.Size()); !valid || err != nil {
		t.Fatalf("append extended the captured snapshot: valid=%v err=%v", valid, err)
	}
	appendFile.Close()
	valid, appendedInfo, err := optionalStreamHasCompleteRecord(path + ".old")
	if valid || err != nil || appendedInfo.Size() <= info.Size() {
		t.Fatalf("new snapshot missed incomplete suffix: valid=%v info=%v err=%v", valid, appendedInfo, err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatal("captured metadata changed after append")
	}
	truncated := false
	truncateDuringRead := optionalRecordReaderAtFunc(func(p []byte, offset int64) (int, error) {
		n, err := f.ReadAt(p, offset)
		if !truncated {
			if truncateErr := os.Truncate(path+".old", 0); truncateErr != nil {
				t.Fatal(truncateErr)
			}
			truncated = true
		}
		return n, err
	})
	if valid, err := validateOptionalStreamRecord(truncateDuringRead, info.Size()); valid || !errors.Is(err, io.EOF) {
		t.Fatalf("truncation was not a read failure: valid=%v err=%v", valid, err)
	}
}

type optionalRecordReaderAtFunc func([]byte, int64) (int, error)

func (f optionalRecordReaderAtFunc) ReadAt(p []byte, offset int64) (int, error) {
	return f(p, offset)
}

func TestOptionalStreamFailureKeepsValidatedAge(t *testing.T) {
	const stream = "misc_events"
	source := metrics.SourceOptionalMisc
	metrics.RegisterSource(source, true)
	dataRoot := t.TempDir()
	states := newOptionalStreamStates()
	now := time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(dataRoot, stream, "hourly", now.Format("20060102"), "1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(data string, modified time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	assertState := func(readOK, schemaOK, age float64) {
		t.Helper()
		metrics.PublishMonitorHealthSnapshot()
		for _, check := range []struct {
			got, want float64
		}{
			{validatorMetricValue(t, metrics.HLExporterSourcePresent.WithLabelValues(string(source))), 1},
			{validatorMetricValue(t, metrics.HLExporterSourceReadOK.WithLabelValues(string(source))), readOK},
			{validatorMetricValue(t, metrics.HLExporterSourceSchemaOK.WithLabelValues(string(source))), schemaOK},
			{validatorMetricValue(t, metrics.HLExporterSourceTimestampSeconds.WithLabelValues(string(source))), float64(now.Unix())},
			{validatorMetricValue(t, metrics.HLNodeStreamAgeSeconds.WithLabelValues(stream)), age},
		} {
			if check.got != check.want {
				t.Fatalf("got %v, want %v", check.got, check.want)
			}
		}
		if !states[stream].lastValid.Equal(now) {
			t.Fatal("failure advanced the last valid timestamp")
		}
	}
	write("{}\n", now)
	tickOptionalStreams(dataRoot, states, now.Add(time.Second))
	assertState(1, 1, 1)
	schemaErrors := metrics.HLExporterSourceErrorsTotal.WithLabelValues(string(source), string(metrics.SourceFailureSchema))
	readErrors := metrics.HLExporterSourceErrorsTotal.WithLabelValues(string(source), string(metrics.SourceFailureRead))
	beforeSchema, beforeRead := validatorMetricValue(t, schemaErrors), validatorMetricValue(t, readErrors)
	for _, data := range []string{"{\"partial\":", "{bad}\n", "null\n"} {
		write(data, now.Add(2*time.Second))
		tickOptionalStreams(dataRoot, states, now.Add(3*time.Second))
		assertState(1, 0, 3)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, optionalStreamRecordLimit+2); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("\n"), optionalStreamRecordLimit+1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tickOptionalStreams(dataRoot, states, now.Add(4*time.Second))
	assertState(1, 0, 4)
	if delta := validatorMetricValue(t, schemaErrors) - beforeSchema; delta != 4 {
		t.Fatalf("schema error delta=%v, want 4", delta)
	}
	if validatorMetricValue(t, readErrors) != beforeRead {
		t.Fatal("schema rejection counted as a read failure")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataRoot, "missing"), path); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(5*time.Second))
	assertState(0, -1, 5)
	if delta := validatorMetricValue(t, readErrors) - beforeRead; delta != 1 {
		t.Fatalf("read error delta=%v, want 1", delta)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write("{}\n", now.Add(6*time.Second))
	tickOptionalStreams(dataRoot, states, now.Add(7*time.Second))
	metrics.PublishMonitorHealthSnapshot()
	if !states[stream].lastValid.Equal(now.Add(6*time.Second)) ||
		validatorMetricValue(t, metrics.HLNodeStreamAgeSeconds.WithLabelValues(stream)) != 1 ||
		validatorMetricValue(t, metrics.HLExporterSourceReadOK.WithLabelValues(string(source))) != 1 ||
		validatorMetricValue(t, metrics.HLExporterSourceSchemaOK.WithLabelValues(string(source))) != 1 {
		t.Fatal("valid record did not restore source health and age")
	}
}

func newOptionalStreamStates() map[string]*optionalStreamState {
	states := make(map[string]*optionalStreamState, len(optionalStreamDirs))
	for _, stream := range optionalStreamDirs {
		states[stream.name] = &optionalStreamState{}
	}
	return states
}

func TestOptionalStreamLifecycleAndFixedAllowlist(t *testing.T) {
	dataRoot := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	states := newOptionalStreamStates()
	stream := "node_fills_streaming"

	tickOptionalStreams(dataRoot, states, now)
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}) {
		t.Fatal("never-seen optional stream emitted an age")
	}
	emptyRoot := filepath.Join(dataRoot, stream, "hourly")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(time.Second))
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}) {
		t.Fatal("present-empty optional stream emitted an age")
	}

	hour := filepath.Join(emptyRoot, now.Format("20060102"), "1")
	if err := os.MkdirAll(filepath.Dir(hour), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hour, []byte(`{"record":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now, now); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(2*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}); !ok || value != 2 {
		t.Fatalf("valid optional stream age = %v, %v", value, ok)
	}

	// An incomplete record cannot advance freshness; age continues from the
	// last complete record instead.
	if err := os.WriteFile(hour, []byte(`{"record":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now.Add(3*time.Second), now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(5*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}); !ok || value != 5 {
		t.Fatalf("partial record advanced freshness: age=%v, %v", value, ok)
	}

	// encoding/json considers null syntactically valid, but it is not a
	// structured stream record and cannot refresh a fixed source.
	if err := os.WriteFile(hour, []byte("null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(hour, now.Add(6*time.Second), now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(7*time.Second))
	if value, ok := b03CollectorValue(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}); !ok || value != 7 {
		t.Fatalf("JSON null advanced optional-stream freshness: age=%v, %v", value, ok)
	}

	if err := os.RemoveAll(filepath.Join(dataRoot, stream)); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(8*time.Second))
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": stream}) {
		t.Fatal("deleted optional stream retained age")
	}

	unknown := filepath.Join(dataRoot, "future_unverified_stream", "hourly", now.Format("20060102"), "1")
	if err := os.MkdirAll(filepath.Dir(unknown), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknown, []byte(`{"record":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tickOptionalStreams(dataRoot, states, now.Add(9*time.Second))
	if b03CollectorHasLabels(t, metrics.HLNodeStreamAgeSeconds, map[string]string{"stream": "future_unverified_stream"}) {
		t.Fatal("arbitrary directory expanded the optional-stream label set")
	}
}
