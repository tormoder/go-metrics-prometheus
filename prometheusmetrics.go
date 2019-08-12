package prometheusmetrics

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rcrowley/go-metrics"
)

type PrometheusConfig struct {
	namespace string
	subsystem string

	registry     metrics.Registry
	promRegistry prometheus.Registerer

	flushInterval time.Duration

	metricsWhitelist map[string]bool

	gauges        map[string]prometheus.Gauge
	customMetrics map[string]*CustomCollector

	histogramBuckets []float64
	timerBuckets     []float64
}

func NewPrometheusProvider(r metrics.Registry, namespace string, subsystem string, promRegistry prometheus.Registerer, flushInterval time.Duration) *PrometheusConfig {
	return &PrometheusConfig{
		namespace:        flattenKey(namespace),
		subsystem:        flattenKey(subsystem),
		registry:         r,
		promRegistry:     promRegistry,
		flushInterval:    flushInterval,
		gauges:           make(map[string]prometheus.Gauge),
		customMetrics:    make(map[string]*CustomCollector),
		histogramBuckets: []float64{0.05, 0.1, 0.25, 0.50, 0.75, 0.9, 0.95, 0.99},
		timerBuckets:     []float64{0.50, 0.95, 0.99, 0.999},
	}
}

func (c *PrometheusConfig) WithHistogramBuckets(b []float64) *PrometheusConfig {
	c.histogramBuckets = b
	return c
}

func (c *PrometheusConfig) WithTimerBuckets(b []float64) *PrometheusConfig {
	c.timerBuckets = b
	return c
}

func (c *PrometheusConfig) WithMetricsWhitelist(metricsWhitelist map[string]bool) {
	c.metricsWhitelist = metricsWhitelist
}

func (c *PrometheusConfig) createKey(name string) string {
	return fmt.Sprintf("%s_%s_%s", c.namespace, c.subsystem, name)
}

func (c *PrometheusConfig) gaugeFromNameAndValue(name string, val float64) {
	key := c.createKey(name)
	g, ok := c.gauges[key]
	if !ok {
		g = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: c.namespace,
			Subsystem: c.subsystem,
			Name:      flattenKey(name),
			Help:      name,
		})
		c.promRegistry.Register(g)
		c.gauges[key] = g
	}
	g.Set(val)
}

func (c *PrometheusConfig) histogramFromNameAndMetric(name string, goMetric interface{}, buckets []float64) {
	key := c.createKey(name)

	collector, ok := c.customMetrics[key]
	if !ok {
		collector = &CustomCollector{}
		c.promRegistry.MustRegister(collector)
		c.customMetrics[key] = collector
	}

	var ps []float64
	var count uint64
	var sum float64
	var typeName string

	switch metric := goMetric.(type) {
	case metrics.Histogram:
		snapshot := metric.Snapshot()
		ps = snapshot.Percentiles(buckets)
		count = uint64(snapshot.Count())
		sum = float64(snapshot.Sum())
		typeName = "histogram"
	case metrics.Timer:
		snapshot := metric.Snapshot()
		ps = snapshot.Percentiles(buckets)
		count = uint64(snapshot.Count())
		sum = float64(snapshot.Sum())
		typeName = "timer"
	default:
		panic(fmt.Sprintf("unexpected metric type %T", goMetric))
	}

	bucketVals := make(map[float64]uint64)

	for ii, bucket := range buckets {
		bucketVals[bucket] = uint64(ps[ii])
	}

	desc := prometheus.NewDesc(
		prometheus.BuildFQName(
			flattenKey(c.namespace),
			flattenKey(c.subsystem),
			fmt.Sprintf("%s_%s", flattenKey(name), typeName),
		),
		name,
		[]string{},
		map[string]string{},
	)

	constHistogram, err := prometheus.NewConstHistogram(
		desc,
		count,
		sum,
		bucketVals,
	)

	if err == nil {
		collector.metric = constHistogram
	}
}

func (c *PrometheusConfig) UpdatePrometheusMetrics() {
	for _ = range time.Tick(c.flushInterval) {
		c.UpdatePrometheusMetricsOnce()
	}
}

func (c *PrometheusConfig) UpdatePrometheusMetricsOnce() error {
	c.registry.Each(func(name string, i interface{}) {
		if c.metricsWhitelist != nil {
			if _, found := c.metricsWhitelist[name]; !found {
				return
			}
		}

		switch metric := i.(type) {
		case metrics.Counter:
			c.gaugeFromNameAndValue(name, float64(metric.Count()))
		case metrics.Gauge:
			c.gaugeFromNameAndValue(name, float64(metric.Value()))
		case metrics.GaugeFloat64:
			c.gaugeFromNameAndValue(name, float64(metric.Value()))
		case metrics.Histogram:
			samples := metric.Snapshot().Sample().Values()
			if len(samples) > 0 {
				lastSample := samples[len(samples)-1]
				c.gaugeFromNameAndValue(name, float64(lastSample))
			}
			c.histogramFromNameAndMetric(name, metric, c.histogramBuckets)
		case metrics.Meter:
			lastSample := metric.Snapshot().Rate1()
			c.gaugeFromNameAndValue(name, float64(lastSample))
		case metrics.Timer:
			lastSample := metric.Snapshot().Rate1()
			c.gaugeFromNameAndValue(name, float64(lastSample))
			c.histogramFromNameAndMetric(name, metric, c.timerBuckets)
		}
	})
	return nil
}

// for collecting prometheus.constHistogram objects
type CustomCollector struct {
	prometheus.Collector

	metric prometheus.Metric
}

func (c *CustomCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- c.metric
}

func (p *CustomCollector) Describe(ch chan<- *prometheus.Desc) {
	// Empty method to fulfill prometheus.Collector interface.
}

var onlyAlphaNumChars = regexp.MustCompile("[^a-zA-Z0-9]+")

func flattenKey(name string) string {
	return strings.Trim(onlyAlphaNumChars.ReplaceAllString(strings.ToLower(name), "_"), "_")
}
