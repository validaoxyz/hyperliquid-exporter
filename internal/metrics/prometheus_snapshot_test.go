package metrics

import (
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestSnapshotProtectedGathererCannotObservePartialUpdate(t *testing.T) {
	registry := prometheus.NewRegistry()
	first := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_snapshot_first"})
	second := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_snapshot_second"})
	registry.MustRegister(first, second)
	gatherer := protectPrometheusSnapshots(registry)

	midUpdate := make(chan struct{})
	finishUpdate := make(chan struct{})
	updateDone := make(chan struct{})
	go func() {
		WithPrometheusSnapshotUpdate(func() {
			first.Set(1)
			close(midUpdate)
			<-finishUpdate
			second.Set(1)
		})
		close(updateDone)
	}()
	<-midUpdate

	gatherDone := make(chan struct{})
	var gatherErr error
	var gatheredFirst, gatheredSecond float64
	go func() {
		families, err := gatherer.Gather()
		gatherErr = err
		if err == nil {
			for _, family := range families {
				if len(family.Metric) != 1 || family.Metric[0].Gauge == nil {
					continue
				}
				switch family.GetName() {
				case "test_snapshot_first":
					gatheredFirst = family.Metric[0].Gauge.GetValue()
				case "test_snapshot_second":
					gatheredSecond = family.Metric[0].Gauge.GetValue()
				}
			}
		}
		close(gatherDone)
	}()

	select {
	case <-gatherDone:
		t.Fatal("gather completed while a metric generation was half-published")
	case <-time.After(20 * time.Millisecond):
	}
	close(finishUpdate)
	<-updateDone
	<-gatherDone
	if gatherErr != nil {
		t.Fatalf("gather: %v", gatherErr)
	}
	if gatheredFirst != 1 || gatheredSecond != 1 {
		t.Fatalf("gathered mixed generation: first=%v second=%v", gatheredFirst, gatheredSecond)
	}
}

func TestSnapshotProtectedGathererAllowsConcurrentScrapes(t *testing.T) {
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_snapshot_concurrent"})
	registry.MustRegister(gauge)
	gatherer := protectPrometheusSnapshots(registry)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := gatherer.Gather(); err != nil {
				t.Errorf("gather: %v", err)
			}
		}()
	}
	wg.Wait()
}
