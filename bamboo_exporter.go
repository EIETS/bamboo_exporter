package main

import (
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EIETS/bamboo_exporter/collector"
	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

var (
	metricsEndpoint = kingpin.Flag("telemetry.endpoint", "Path under which to expose metrics.").Default("/metrics").String()
	scrapeURI       = kingpin.Flag("bamboo.uri", "Full Bamboo URI to scrape metrics from.").Default("http://localhost:8085").String()
	insecure        = kingpin.Flag("insecure", "Ignore server certificate if using https.").Bool()
	// toolkitFlags: Add default web server configuration flags.
	toolkitFlags = kingpinflag.AddFlags(kingpin.CommandLine, ":9117")
	// gracefulStop: Channel to receive OS signals for graceful shutdown.
	gracefulStop = make(chan os.Signal, 1)
)

func main() {
	promslogConfig := &promslog.Config{}

	// Parse flags
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Version(version.Print("bamboo_exporter"))
	kingpin.Parse()

	logger := promslog.New(promslogConfig)
	// listen to termination signals from the OS
	signal.Notify(gracefulStop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)

	config := &collector.Config{
		ScrapeURI: *scrapeURI,
		Insecure:  *insecure,
	}

	exporter := collector.NewExporter(config, logger)
	prometheus.MustRegister(exporter)
	prometheus.MustRegister(versioncollector.NewCollector("bamboo_exporter"))

	// log startup information
	logger.Info("Starting bamboo_exporter", "version", version.Info())
	logger.Info("Build context", "build", version.BuildContext())
	logger.Info("Collecting metrics from", "scrape_host", *scrapeURI)

	// listener for the termination signals from the OS
	go func() {
		logger.Debug("Listening and waiting for graceful stop")
		sig := <-gracefulStop
		logger.Info("Caught signal. Wait 2 seconds...", "sig", sig)
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	// expose metrics endpoint
	http.Handle(*metricsEndpoint, promhttp.Handler())

	// configure the landing page
	landingConfig := web.LandingConfig{
		Name:        "Bamboo Exporter",
		Description: "Prometheus exporter for Bamboo metrics",
		Version:     version.Info(),
		Links: []web.LandingLinks{
			{
				Address: *metricsEndpoint,
				Text:    "Metrics",
			},
		},
	}

	landingPage, err := web.NewLandingPage(landingConfig)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	http.Handle("/", landingPage)

	// start the http server
	server := &http.Server{}
	if err := web.ListenAndServe(server, toolkitFlags, logger); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}
