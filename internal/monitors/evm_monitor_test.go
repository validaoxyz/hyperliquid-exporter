package monitors

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
)

type recordedPriority struct {
	value   float64
	observe bool
}

type recordingEVMSink struct {
	heights            []int64
	intervals          []float64
	latestTimes        []int64
	gasRatios          []*float64
	baseFees           []float64
	priorityFees       []recordedPriority
	txCounts           []int
	txTypes            []string
	shapes             map[string]uint64
	contractCreates    uint64
	recipients         []string
	receiptOutcomes    map[string]uint64
	mismatchHeights    []int64
	systemItems        int
	precompileOutcomes map[string]uint64
}

func newRecordingEVMSink() *recordingEVMSink {
	return &recordingEVMSink{
		shapes:             make(map[string]uint64),
		receiptOutcomes:    make(map[string]uint64),
		precompileOutcomes: make(map[string]uint64),
	}
}

func (s *recordingEVMSink) setBlockHeight(height int64) { s.heights = append(s.heights, height) }
func (s *recordingEVMSink) recordBlockTimeMilliseconds(value float64) {
	s.intervals = append(s.intervals, value)
}
func (s *recordingEVMSink) setLatestBlockTime(value int64) {
	s.latestTimes = append(s.latestTimes, value)
}
func (s *recordingEVMSink) setGasSnapshot(_, _ float64, ratio *float64) {
	if ratio == nil {
		s.gasRatios = append(s.gasRatios, nil)
		return
	}
	copyValue := *ratio
	s.gasRatios = append(s.gasRatios, &copyValue)
}
func (s *recordingEVMSink) setBaseFeeGwei(value float64) {
	s.baseFees = append(s.baseFees, value)
}
func (s *recordingEVMSink) setPriorityFeeGwei(value float64, observe bool) {
	s.priorityFees = append(s.priorityFees, recordedPriority{value: value, observe: observe})
}
func (s *recordingEVMSink) recordTxCount(value int) { s.txCounts = append(s.txCounts, value) }
func (s *recordingEVMSink) incrementTxType(value string) {
	s.txTypes = append(s.txTypes, value)
}
func (s *recordingEVMSink) incrementTxShape(shape string, count uint64) {
	s.shapes[shape] += count
}
func (s *recordingEVMSink) incrementContractCreations(count uint64) {
	s.contractCreates += count
}
func (s *recordingEVMSink) incrementRecipient(address string) {
	s.recipients = append(s.recipients, address)
}
func (s *recordingEVMSink) incrementReceiptOutcome(outcome string, count uint64) {
	s.receiptOutcomes[outcome] += count
}
func (s *recordingEVMSink) markCountMismatch(height int64) {
	s.mismatchHeights = append(s.mismatchHeights, height)
}
func (s *recordingEVMSink) addSystemTxItems(count int) { s.systemItems += count }
func (s *recordingEVMSink) addPrecompileCalls(outcome string, count uint64) {
	s.precompileOutcomes[outcome] += count
}

func TestEVMProcessorPreservesFractionalIntervals(t *testing.T) {
	sink := newRecordingEVMSink()
	processor := newEVMProcessor(sink, false, 0)
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:14.100000000", "0x2a", false, `[]`)); err != nil {
		t.Fatal(err)
	}
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:14.100500000", "0x2b", false, `[]`)); err != nil {
		t.Fatal(err)
	}
	if len(sink.intervals) != 1 || sink.intervals[0] != 0.5 {
		t.Fatalf("intervals=%v, want [0.5]", sink.intervals)
	}
}

func TestStartEVMMonitorCancellationInterruptsGracePeriod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		StartEVMMonitor(ctx, config.Config{}, make(chan error, 1))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EVM monitor ignored cancellation during validator-status grace period")
	}
}

func TestEVMProcessorParseFailureLeavesIntervalStateUnchanged(t *testing.T) {
	sink := newRecordingEVMSink()
	processor := newEVMProcessor(sink, false, 0)
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:14.100000000", "0x2a", false, `[]`)); err != nil {
		t.Fatal(err)
	}
	bad := evmFixtureLine("not-a-timestamp", "0x2b", false, `[]`)
	if err := processor.processLine(bad); err == nil {
		t.Fatal("malformed timestamp accepted")
	}
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:14.200000000", "0x2c", false, `[]`)); err != nil {
		t.Fatal(err)
	}
	if len(sink.intervals) != 1 || sink.intervals[0] != 100 {
		t.Fatalf("intervals=%v, want [100]", sink.intervals)
	}
	if len(sink.heights) != 2 {
		t.Fatalf("malformed record partially published a height: %v", sink.heights)
	}
}

func TestEVMProcessorStagesConsumedSiblingsBeforePublication(t *testing.T) {
	sink := newRecordingEVMSink()
	processor := newEVMProcessor(sink, false, 0)
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:14.1", "0x2a", true, `{}`)); err == nil {
		t.Fatal("malformed consumed receipts accepted")
	}
	if len(sink.heights) != 0 || len(sink.txCounts) != 0 || len(sink.txTypes) != 0 {
		t.Fatalf("malformed sibling caused partial publication: heights=%v txCounts=%v txTypes=%v", sink.heights, sink.txCounts, sink.txTypes)
	}
}

func TestEVMProcessorResetsEmptyBlockGaugesWithoutFeeSample(t *testing.T) {
	sink := newRecordingEVMSink()
	processor := newEVMProcessor(sink, false, 0)
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:14.1", "0x2a", true, `[{"success":true}]`)); err != nil {
		t.Fatal(err)
	}
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:15.1", "0x2b", false, `[]`)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sink.txCounts) != "[1 0]" {
		t.Fatalf("tx counts=%v", sink.txCounts)
	}
	if len(sink.baseFees) != 2 || sink.baseFees[0] != 0 || sink.baseFees[1] != 0 {
		t.Fatalf("base fees=%v", sink.baseFees)
	}
	if len(sink.priorityFees) != 2 || !sink.priorityFees[0].observe || sink.priorityFees[1].observe || sink.priorityFees[1].value != 0 {
		t.Fatalf("priority updates=%+v", sink.priorityFees)
	}
}

func TestEVMProcessorCountMismatchPersistsAcrossEqualBlock(t *testing.T) {
	sink := newRecordingEVMSink()
	processor := newEVMProcessor(sink, false, 0)
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:14.1", "0x2a", true, `[]`)); err != nil {
		t.Fatal(err)
	}
	if err := processor.processLine(evmFixtureLine("2026-08-08T03:15:15.1", "0x2b", false, `[]`)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sink.mismatchHeights) != "[42]" {
		t.Fatalf("mismatch heights=%v, equal block must not clear or add", sink.mismatchHeights)
	}
}

func TestRecipientLimiterStickyCapAndOther(t *testing.T) {
	limiter := newRecipientLimiter(true, 2)
	a := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	b := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	c := "0xcccccccccccccccccccccccccccccccccccccccc"
	if got := limiter.label(a); got != a {
		t.Fatalf("first=%q", got)
	}
	if got := limiter.label(b); got != b {
		t.Fatalf("second=%q", got)
	}
	if got := limiter.label(c); got != "other" {
		t.Fatalf("overflow=%q", got)
	}
	if got := limiter.label(a); got != a {
		t.Fatalf("sticky existing=%q", got)
	}
	if len(limiter.seen) != 2 {
		t.Fatalf("seen=%d", len(limiter.seen))
	}
}

func TestEVMProcessorConcurrentCallsAreSerialized(t *testing.T) {
	sink := newRecordingEVMSink()
	processor := newEVMProcessor(sink, true, 3)
	line := evmFixtureLine("2026-08-08T03:15:14.1", "0x2a", true, `[{"success":true}]`)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := processor.processLine(line); err != nil {
				t.Errorf("processLine: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(sink.heights) != 32 || len(sink.recipients) != 32 {
		t.Fatalf("heights=%d recipients=%d", len(sink.heights), len(sink.recipients))
	}
}

func evmFixtureLine(timestamp, height string, withTx bool, receipts string) string {
	transactions := ""
	if withTx {
		transactions = `{"transaction":{"Eip1559":{"maxPriorityFeePerGas":"0x0","maxFeePerGas":"0x0","to":"0x1111111111111111111111111111111111111111"}},"signature":{"r":"0x0","s":"0x0"}}`
	}
	return fmt.Sprintf(
		`["%s",{"block":{"Reth115":{"header":{"header":{"number":"%s","gasLimit":"0x2dc6c0","gasUsed":"0x0","baseFeePerGas":"0x0","timestamp":"0x0"}},"body":{"transactions":[%s]}}},"receipts":%s,"system_txs":[],"read_precompile_calls":[],"highest_precompile_address":null}]`,
		timestamp,
		height,
		transactions,
		receipts,
	)
}
