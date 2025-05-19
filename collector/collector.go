package collector

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	buildSuccess      *prometheus.CounterVec
	buildFailure      *prometheus.CounterVec
	buildCount        *prometheus.GaugeVec
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
	Type     string `json:"type"`
	IsActive bool   `json:"active"`
	IsBusy   bool   `json:"busy"`
	Enabled  bool   `json:"enabled"`
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
			Help:      "Status of Bamboo agents (enabled/active/busy).",
		}, []string{"id", "name", "type", "enabled", "active", "busy"}),
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
		buildSuccess: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "build_success_total",
			Help:      "Successful builds per project version",
		}, []string{"project", "name"}),
		buildFailure: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "build_failure_total",
			Help:      "Failed builds per project version",
		}, []string{"project", "name"}),
		buildCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "build_total",
			Help:      "Total builds executed per project version",
		}, []string{"project", "name"}),
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
	e.buildSuccess.Describe(ch)
	e.buildFailure.Describe(ch)
	e.buildCount.Describe(ch)
}

// Collect collects metrics from Bamboo and sends them to Prometheus.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	success := e.scrapeMetrics()
	ch <- prometheus.MustNewConstMetric(e.up, prometheus.GaugeValue, success)
	e.failures.Collect(ch)
	e.agents.Collect(ch)
	e.queue.Collect(ch)
	e.utilization.Collect(ch)
	e.queueChange.Collect(ch)
	e.buildSuccess.Collect(ch)
	e.buildFailure.Collect(ch)
	e.buildCount.Collect(ch)
}

// scrapeMetrics fetches metrics from Bamboo and processes them.
func (e *Exporter) scrapeMetrics() float64 {
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

	if err := e.scrapeBuildResults(); err != nil {
		e.logger.Error("Failed to scrape build results", "error", err)
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
		if agent.IsActive {
			activeCount++
		}

		if agent.IsBusy {
			busyCount++
		}
		e.agents.WithLabelValues(strconv.FormatInt(agent.ID, 10), agent.Name, agent.Type,
			fmt.Sprintf("%t", agent.Enabled), fmt.Sprintf("%t", agent.IsActive),
			fmt.Sprintf("%t", agent.IsBusy)).Set(1)
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

// scrapeBuildResults fetches and processes build results from Bamboo.
func (e *Exporter) scrapeBuildResults() error {
	currentIndex := 0
	totalSize := 0
	maxResult := 100

	for {
		params := url.Values{
			"start-index": []string{strconv.Itoa(currentIndex)},
			"max-result":  []string{strconv.Itoa(maxResult)},
			"expand":      []string{"results.result"},
		}

		data, err := e.doRequest("/rest/api/latest/result?" + params.Encode())
		if err != nil {
			return fmt.Errorf("error fetching result: %w", err)
		}

		var response struct {
			Results struct {
				Size   int `json:"size"`
				Result []struct {
					Plan struct {
						Name string `json:"name"`
					} `json:"plan"`
					BuildNumber int    `json:"buildNumber"`
					State       string `json:"state"`
				} `json:"result"`
			} `json:"results"`
		}

		if err := json.Unmarshal(data, &response); err != nil {
			return fmt.Errorf("error unmarshaling results: %w", err)
		}

		if totalSize == 0 {
			totalSize = response.Results.Size
		}

		// process data of current page
		for _, r := range response.Results.Result {
			project, name := parseProjectAndName(r.Plan.Name)
			labels := []string{project, name}

			switch r.State {
			case "Successful":
				e.buildSuccess.WithLabelValues(labels...).Inc()
			default:
				e.buildFailure.WithLabelValues(labels...).Inc()
			}

			// set build count
			e.buildCount.WithLabelValues(labels...).Set(float64(r.BuildNumber))
		}

		// end of page
		fetchedCount := currentIndex + len(response.Results.Result)
		if fetchedCount >= totalSize || len(response.Results.Result) == 0 {
			break
		}
		currentIndex = fetchedCount
	}
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

func parseProjectAndName(planName string) (project, name string) {
	parts := strings.SplitN(planName, " - ", 2)
	if len(parts) >= 2 {
		project = strings.TrimSpace(parts[0])
		name = strings.TrimSpace(parts[1])
	} else {
		project = "Unknown"
		name = strings.TrimSpace(planName)
	}
	return
}
