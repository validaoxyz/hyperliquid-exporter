package metrics

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
)

var (
	HLExporterConfigInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_config_info",
		Help: "Immutable exporter configuration identity. The chain label is configured and validated; it does not attest the chain observed from the node.",
	}, []string{"chain"})

	configuredChainMu sync.Mutex
	configuredChain   string
)

// SetConfiguredChain publishes exactly one immutable configured-chain series
// for the process lifetime.
func SetConfiguredChain(raw string) error {
	chain, err := config.NormalizeChain(raw)
	if err != nil {
		return err
	}

	configuredChainMu.Lock()
	defer configuredChainMu.Unlock()
	if configuredChain != "" && configuredChain != chain {
		return fmt.Errorf("configured chain is immutable: already %q, got %q", configuredChain, chain)
	}
	if configuredChain == "" {
		configuredChain = chain
		HLExporterConfigInfo.WithLabelValues(chain).Set(1)
	}
	return nil
}
