package metrics

import (
	"sync"

	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

type labeledValue struct {
	value  float64
	labels []attribute.KeyValue
}

type NodeAlias struct {
	ValidatorAddress string
	ServerIP         string
	Alias            string
	IsValidator      bool
	Chain            string
}

// Global variables for metric state management
var (
	currentValues = make(map[api.Observable]interface{})
	labeledValues = make(map[api.Observable]map[string]labeledValue)
	metricsMutex  sync.RWMutex
	callbacks     []api.Registration
	nodeAlias  NodeAlias
)

// TODO CommonLabels holds the common labels to be added to all metrics
var commonLabels []attribute.KeyValue
