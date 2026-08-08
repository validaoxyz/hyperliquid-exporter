package monitors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

type validatorConnectionObservation struct {
	sourceTime time.Time
	event      string
	result     string
	subsystem  string
	known      bool
}

func monitorValidatorConnectionLogs(ctx context.Context, cfg *config.Config, _ chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "validator_connections", "hourly")
	tailStream(ctx, tailStreamOpts{
		component:    "consensus",
		name:         "validator connections stream",
		resolveState: func() tailStreamResolution { return resolveValidatorConnectionStream(root) },
		rescanEvery:  10 * time.Second,
		eofSleep:     500 * time.Millisecond,
		onSwitch:     func(string) { metrics.MarkSourceAvailable(metrics.SourceValidatorConnections) },
		onUnavailable: func(kind tailStreamUnavailable) {
			markValidatorConnectionUnavailable(kind)
		},
		onFailure: func(failure tailStreamFailure) {
			markTailSourceFailure(metrics.SourceValidatorConnections, failure)
		},
		onLine: func(line string) {
			observation, err := parseValidatorConnectionLine([]byte(strings.TrimSpace(line)))
			if err != nil {
				metrics.HLValidatorConnectionParse.WithLabelValues("malformed").Inc()
				metrics.MarkSourceError(metrics.SourceValidatorConnections, metrics.SourceFailureSchema)
				return
			}
			result := "unknown_tag"
			if observation.known {
				result = "ok"
			}
			metrics.HLValidatorConnectionParse.WithLabelValues(result).Inc()
			metrics.HLValidatorConnectionEvents.WithLabelValues(observation.event, observation.result, observation.subsystem).Inc()
			metrics.MarkSourceValidObservation(metrics.SourceValidatorConnections, observation.sourceTime)
			metrics.MarkSourcePublication(metrics.SourceValidatorConnections)
		},
	})
}

func resolveValidatorConnectionStream(root string) tailStreamResolution {
	return resolveValidatorConnectionStreamWith(root, os.ReadDir)
}

func resolveValidatorConnectionStreamWith(root string, readDir func(string) ([]os.DirEntry, error)) tailStreamResolution {
	metrics.MarkSourceAttempt(metrics.SourceValidatorConnections)
	result := resolveLatestHourlyStreamWith(root, readDir)
	if result.err != nil {
		metrics.MarkSourceError(metrics.SourceValidatorConnections, metrics.SourceFailureDiscovery)
	}
	return result
}

func markValidatorConnectionUnavailable(kind tailStreamUnavailable) {
	if kind == tailStreamUnavailableAbsent {
		metrics.MarkSourceAbsent(metrics.SourceValidatorConnections)
		return
	}
	metrics.MarkSourceAvailable(metrics.SourceValidatorConnections)
}

func parseValidatorConnectionLine(line []byte) (validatorConnectionObservation, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return validatorConnectionObservation{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if len(outer) != 2 {
		return validatorConnectionObservation{}, fmt.Errorf("envelope length %d", len(outer))
	}
	var timestamp string
	if err := json.Unmarshal(outer[0], &timestamp); err != nil {
		return validatorConnectionObservation{}, fmt.Errorf("invalid timestamp")
	}
	sourceTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestamp)
	if err != nil {
		return validatorConnectionObservation{}, fmt.Errorf("parse timestamp: %w", err)
	}
	var payload []json.RawMessage
	if err := json.Unmarshal(outer[1], &payload); err != nil || len(payload) == 0 {
		return validatorConnectionObservation{}, fmt.Errorf("invalid payload")
	}
	var tag string
	if err := json.Unmarshal(payload[0], &tag); err != nil || tag == "" {
		return validatorConnectionObservation{}, fmt.Errorf("invalid tag")
	}
	observation := validatorConnectionObservation{
		sourceTime: sourceTime,
		event:      "other",
		result:     "other",
		subsystem:  "other",
	}
	if tag != "handle_stream_connection" {
		return observation, nil
	}
	if len(payload) != 3 {
		return validatorConnectionObservation{}, fmt.Errorf("handle_stream_connection shape")
	}
	var endpoint, subsystem string
	if json.Unmarshal(payload[1], &endpoint) != nil || endpoint == "" || json.Unmarshal(payload[2], &subsystem) != nil || subsystem == "" {
		return validatorConnectionObservation{}, fmt.Errorf("invalid handle_stream_connection fields")
	}
	observation.event = "handle_stream_connection"
	observation.result = "observed"
	if subsystem == "validator" {
		observation.subsystem = "validator"
	}
	observation.known = true
	return observation, nil
}
