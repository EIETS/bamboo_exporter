package collector

import (
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
	URI      string
	client   *http.Client
	mutex    sync.Mutex
	up       *prometheus.Desc
	failures prometheus.Counter
	agents   *prometheus.GaugeVec
	queue    prometheus.Gauge
	logger   *slog.Logger
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
		logger: logger,
	}
}

// Describe describes the Prometheus metrics for the exporter.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.up
	e.failures.Describe(ch)
	e.agents.Describe(ch)
	e.queue.Describe(ch)
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
	for _, agent := range agents {
		status := "inactive"
		if agent.IsActive {
			status = "active"
		}
		e.agents.WithLabelValues(agent.Name, status).Set(1)

		if agent.IsBusy {
			e.agents.WithLabelValues(agent.Name, "busy").Set(1)
		}
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

	e.queue.Set(float64(queue.QueuedBuilds.Size))
	return nil
}

// doRequest sends a GET request to the Bamboo API and returns the response body.
func (e *Exporter) doRequest(endpoint string) ([]byte, error) {
	req, err := http.NewRequest("GET", e.URI+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
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