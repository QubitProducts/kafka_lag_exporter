package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace = "burrow"
)

var (
	listenAddress = flag.String("web.listen-address", ":9106", "Address to listen on for web interface and telemetry.")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	cfgFile       = flag.String("config", "kafka_lag_exporter.yml", "Config file location.")
	keepAlive     = time.Minute * 30
)

func main() {
	var err error
	defer func() {
		if err != nil {
			fmt.Fprintf(os.Stderr, "%+v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()

	flag.Parse()

	cfgFile, err := os.Open(*cfgFile)
	if err != nil {
		return
	}

	cfg, err := readConfig(cfgFile)
	if err != nil {
		return
	}

	exp := NewExporter(cfg)
	prometheus.MustRegister(exp)

	http.Handle(*metricsPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Burrow Kafka Offsets Exporter</title></head>
             <body>
             <h1>Burrow Kafka Offsets Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})
	glog.Fatal(http.ListenAndServe(*listenAddress, nil))
}

// Exporter represents Burrow metrics to prometheus.
type Exporter struct {
	*config
	kafkaExps []*KafkaExporter

	sync.Mutex
}

// NewExporter returns an initialized exporter
func NewExporter(cfg *config) *Exporter {
	exp := &Exporter{
		config: cfg,
	}
	for c, kcfg := range cfg.Kafka {
		cfg := kcfg
		kexp, err := NewKafkaExporter(c, &cfg)
		if err != nil {
			glog.Infof("%+v", err)
			continue
		}

		exp.kafkaExps = append(exp.kafkaExps, kexp)
	}
	return exp
}

// Describe describes all the metrics exported by the memcache exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, ke := range e.kafkaExps {
		ke.Describe(ch)
	}
}

// Collect fetches the statistics from the configured burrow server, and
// delivers them as prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	// prevent concurrent metric collections
	e.Lock()
	defer e.Unlock()

	wg := sync.WaitGroup{}
	for _, ke := range e.kafkaExps {
		wg.Add(1)
		go func(ke *KafkaExporter) {
			defer wg.Done()
			ke.Collect(ch)
		}(ke)
	}
	wg.Wait()
}
