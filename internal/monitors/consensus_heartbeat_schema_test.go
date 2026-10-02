package monitors

import (
	"fmt"
	"testing"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"go.opentelemetry.io/otel"
)

func setupHeartbeatSchemaCounter(t *testing.T) {
	t.Helper()
	sentCounter, err := otel.Meter("heartbeat-schema-test").Int64Counter("test_heartbeat_schema_sent")
	if err != nil {
		t.Fatal(err)
	}
	old := metrics.HLConsensusHeartbeatSentCounter
	metrics.HLConsensusHeartbeatSentCounter = sentCounter
	t.Cleanup(func() { metrics.HLConsensusHeartbeatSentCounter = old })
}

func TestConsensusHeartbeatExecutedRoundJoins(t *testing.T) {
	setupHeartbeatSchemaCounter(t)
	for _, fields := range []string{`"executed_round":77`, `"round":77`, `"round":77,"executed_round":77`} {
		t.Run(fields, func(t *testing.T) {
			m := NewConsensusMonitor(&config.Config{})
			sent := fmt.Sprintf(`["2026-10-02T08:15:10.000000000",["out",{"Heartbeat":{"validator":"0x1111..1111","random_id":424,%s}}]]`, fields)
			ack := fmt.Sprintf(`["2026-10-02T08:15:10.010000000",["in",{"sender":"0x2222..2222","msg":{"HeartbeatAck":{"validator":"0x2222..2222","random_id":424,%s}}}]]`, fields)
			before := validatorHistogramCount(t, metrics.HLConsensusHeartbeatPeerAckDelay)
			if err := m.processConsensusLine(sent); err != nil {
				t.Fatal(err)
			}
			if err := m.processConsensusLine(ack); err != nil {
				t.Fatal(err)
			}
			key := heartbeatKey{validator: "0x1111..1111", randomID: 424, round: 77}
			want := time.Date(2026, 10, 2, 8, 15, 10, 10_000_000, time.UTC)
			if !m.heartbeats[key].matched || !m.heartbeatAcks[heartbeatAckKey{heartbeatKey: key, source: "0x2222..2222"}].Equal(want) {
				t.Fatal("heartbeat acknowledgement did not join the sent heartbeat")
			}
			if got := validatorHistogramCount(t, metrics.HLConsensusHeartbeatPeerAckDelay) - before; got != 1 {
				t.Fatalf("peer delay observation count = %d, want 1", got)
			}
		})
	}
}

func TestConsensusHeartbeatRejectsInvalidRoundAliases(t *testing.T) {
	setupHeartbeatSchemaCounter(t)
	for _, fields := range []string{
		``, `,"executed_round":null`, `,"executed_round":0`, `,"executed_round":-1`,
		`,"executed_round":"77"`, `,"executed_round":1.5`, `,"executed_round":18446744073709551616`,
		`,"round":77,"executed_round":78`, `,"round":77,"executed_round":null`,
		`,"round":77,"executed_round":0`, `,"round":null,"executed_round":77`,
		`,"round":0,"executed_round":77`, `,"round":"77","executed_round":77`,
	} {
		for _, kind := range []string{"Heartbeat", "HeartbeatAck"} {
			t.Run(kind+fields, func(t *testing.T) {
				m := NewConsensusMonitor(&config.Config{})
				key := heartbeatKey{validator: "0x1111..1111", randomID: 424, round: 77}
				if kind == "HeartbeatAck" {
					m.heartbeats[key] = heartbeatInfo{}
				}
				payload := fmt.Sprintf(`{"%s":{"validator":"0x1111..1111","random_id":424%s}}`, kind, fields)
				inner := `["out",` + payload + `]`
				if kind == "HeartbeatAck" {
					inner = `["in",{"sender":"0x1111..1111","msg":` + payload + `}]`
				}
				if err := m.processConsensusLine(`["2026-10-02T08:15:10.000000000",` + inner + `]`); err == nil {
					t.Fatal("accepted an invalid heartbeat round")
				}
				if len(m.heartbeatAcks) != 0 || m.heartbeats[key].matched || (kind == "Heartbeat" && len(m.heartbeats) != 0) {
					t.Fatal("rejected heartbeat changed correlation state")
				}
			})
		}
	}
}
