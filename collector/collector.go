package collector

import (
	"os"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "bamboo"

// Exporter collects metrics from Bamboo and exposes them to Prometheus.
type Exporter struct {
	URI               string
	client            *http.Client
	mutex             sync.Mutex
	up                *prometheus.Desc
	failures          prometheus.Counter
	agents            *prometheus.GaugeVec
	queue             prometheus.Gauge
	utilization       prometheus.Gauge
	queueChange       prometheus.Gauge
	logger            *slog.Logger
	previousQueueSize int64
}

// Config holds the configuration for the exporter.
type Config struct {
	ScrapeURI string
	Insecure  bool
}

// BambooAgent represents an agent's data fetched from the Bamboo API.
type BambooAgent struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"active"`
	IsBusy   bool   `json:"busy"`
}

// BambooQueue represents the build queue data fetched from the Bamboo API.
type BambooQueue struct {
	QueuedBuilds struct {
		Size int64 `json:"size"`
	} `json:"queuedBuilds"`
}

// NewExporter creates a new instance of Exporter.
func NewExporter(config *Config, logger *slog.Logger) *Exporter {
	return &Exporter{
		URI: config.ScrapeURI,
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: config.Insecure},
			},
		},
		up: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "up"),
			"Whether the Bamboo API is reachable.",
			nil,
			nil,
		),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_failures_total",
			Help:      "Total number of scrape failures.",
		}),
		agents: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "agents_status",
			Help:      "Status of Bamboo agents (active/busy).",
		}, []string{"name", "status"}),
		queue: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "queue_size",
			Help:      "Number of builds in the Bamboo queue.",
		}),
		utilization: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "agent_utilization",
			Help:      "Utilization rate of Bamboo agents (busy/active ratio).",
		}),
		queueChange: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "queue_change",
			Help:      "Change in the Bamboo build queue size since the last scrape.",
		}),
		logger: logger,
	}
}

// Describe describes the Prometheus metrics for the exporter.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.up
	e.failures.Describe(ch)
	e.agents.Describe(ch)
	e.queue.Describe(ch)
	e.utilization.Describe(ch)
	e.queueChange.Describe(ch)
}

// Collect collects metrics from Bamboo and sends them to Prometheus.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	success := e.scrapeMetrics(ch)
	ch <- prometheus.MustNewConstMetric(e.up, prometheus.GaugeValue, success)
	e.failures.Collect(ch)
	e.agents.Collect(ch)
	e.queue.Collect(ch)
	e.utilization.Collect(ch)
	e.queueChange.Collect(ch)
}

// scrapeMetrics fetches metrics from Bamboo and processes them.
func (e *Exporter) scrapeMetrics(ch chan<- prometheus.Metric) float64 {
	if err := e.scrapeAgents(); err != nil {
		e.logger.Error("Failed to scrape agents", "error", err)
		e.failures.Inc()
		return 0
	}

	if err := e.scrapeQueue(); err != nil {
		e.logger.Error("Failed to scrape queue", "error", err)
		e.failures.Inc()
		return 0
	}

	return 1
}

// scrapeAgents fetches and processes agent metrics from Bamboo.
func (e *Exporter) scrapeAgents() error {
	data, err := e.doRequest("/rest/api/latest/agent")
	if err != nil {
		return fmt.Errorf("error fetching agents: %w", err)
	}

	var agents []BambooAgent
	if err := json.Unmarshal(data, &agents); err != nil {
		return fmt.Errorf("error unmarshaling agents: %w", err)
	}

	e.agents.Reset()
	activeCount := 0
	busyCount := 0

	for _, agent := range agents {
		status := "inactive"
		if agent.IsActive {
			status = "active"
			activeCount++
		}
		e.agents.WithLabelValues(agent.Name, status).Set(1)

		if agent.IsBusy {
			e.agents.WithLabelValues(agent.Name, "busy").Set(1)
			busyCount++
		}
	}

	if activeCount > 0 {
		utilization := float64(busyCount) / float64(activeCount)
		e.utilization.Set(utilization)
	}

	return nil
}

// scrapeQueue fetches and processes queue metrics from Bamboo.
func (e *Exporter) scrapeQueue() error {
	data, err := e.doRequest("/rest/api/latest/queue")
	if err != nil {
		return fmt.Errorf("error fetching queue: %w", err)
	}

	var queue BambooQueue
	if err := json.Unmarshal(data, &queue); err != nil {
		return fmt.Errorf("error unmarshaling queue: %w", err)
	}

	currentQueueSize := queue.QueuedBuilds.Size
	e.queue.Set(float64(currentQueueSize))

	// Calculate queue size change
	change := currentQueueSize - e.previousQueueSize
	e.queueChange.Set(float64(change))
	e.previousQueueSize = currentQueueSize

	return nil
}

// doRequest sends a GET request to the Bamboo API and returns the response body.
func (e *Exporter) doRequest(endpoint string) ([]byte, error) {
	configData, err := os.ReadFile("config.json")
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	var config struct {
		BambooUsername string `json:"bamboo_username"`
		BambooPassword string `json:"bamboo_password"`
	}
	err = json.Unmarshal(configData, &config)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling config file: %w", err)
	}

	req, err := http.NewRequest("GET", e.URI+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(config.BambooUsername, config.BambooPassword)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error performing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("unexpected status code: " + resp.Status)
	}

	return io.ReadAll(resp.Body)
}
