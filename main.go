package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/golang/glog"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace = "burrow"
)

var (
	listenAddress = flag.String("web.listen-address", ":9106", "Address to listen on for web interface and telemetry.")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	cfgFile       = flag.String("config", "kafka_lag_exporter.yml", "Config file location.")
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

	exporter := NewExporter(cfg)
	prometheus.MustRegister(exporter)

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
	url string

	sync.Mutex
	toffset *prometheus.GaugeVec
	coffset *prometheus.GaugeVec
	//lag     *prometheus.GaugeVec
	//ok      *prometheus.GaugeVec
}

// NewExporter returns an initialized exporter
func NewExporter(cfg *config) *Exporter {
	url := "http://localhost:8000"
	return &Exporter{
		config: cfg,
		url:    url,
		toffset: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name:      "topic_offset_messages",
				Namespace: namespace,
				Help:      "The current offset of each topic partition.",
			},
			[]string{"cluster", "topic", "partition"},
		),
		coffset: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name:      "consumer_offset_messages",
				Namespace: namespace,
				Help:      "The current offset of each consumer in each partition for a topic.",
			},
			[]string{"cluster", "consumer_group", "topic", "partition"},
		),
	}
}

// Describe describes all the metrics exported by the memcache exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	e.toffset.Describe(ch)
	e.coffset.Describe(ch)
	//e.lag.Describe(ch)
	//e.ok.Describe(ch)
}

// Collect fetches the statistics from the configured burrow server, and
// delivers them as prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	// prevent concurrent metric collections
	e.Lock()
	defer e.Unlock()

	e.toffset.Reset()
	e.coffset.Reset()
	//e.lag.Reset()
	//e.ok.Reset()

	cls, err := e.listClusters()
	if err != nil {
		glog.Errorf("%+v", err)
		return
	}

	for _, c := range cls {
		cts, err := e.listClusterTopics(c)
		if err != nil {
			glog.Errorf("%+v", err)
		}
		for _, ct := range cts {
			e.collectTopicOffsets(c, ct)
		}

		cgs, err := e.listConsumerGroups(c)
		if err != nil {
			glog.Errorf("%+v", err)
		}
		for _, cg := range cgs {
			cgts, err := e.listConsumerGroupTopics(c, cg)
			if err != nil {
				glog.Errorf("%+v", err)
			}
			for _, t := range cgts {
				e.collectConsumerGroupTopicOffsets(c, cg, t)
			}
		}
	}

	e.toffset.Collect(ch)
	e.coffset.Collect(ch)
}

func (e *Exporter) listClusters() ([]string, error) {
	resp := struct {
		Err      bool
		Message  string
		Clusters []string
	}{}

	bresp, err := http.Get(fmt.Sprintf("%s/v2/kafka", e.url))
	if err != nil {
		errors.Wrap(err, "request failed")
	}

	buf := bytes.Buffer{}
	io.Copy(&buf, bresp.Body)

	err = json.Unmarshal(buf.Bytes(), &resp)
	if err != nil {
		errors.Wrap(err, "unmarshal failed")
	}

	if resp.Err {
		return nil, errors.Errorf("list cnusters failed, %+v", resp.Message)
	}

	return resp.Clusters, nil
}

func (e *Exporter) listClusterTopics(c string) ([]string, error) {
	resp := struct {
		Err     bool
		Message string
		Topics  []string
	}{}

	bresp, err := http.Get(fmt.Sprintf("%s/v2/kafka/%s/topic", e.url, c))
	if err != nil {
		errors.Wrap(err, "request failed")
	}

	buf := bytes.Buffer{}
	io.Copy(&buf, bresp.Body)

	err = json.Unmarshal(buf.Bytes(), &resp)
	if err != nil {
		errors.Wrap(err, "unmarshal failed")
	}

	if resp.Err {
		return nil, errors.Errorf("list cnusters failed, %+v", resp.Message)
	}

	return resp.Topics, nil
}

func (e *Exporter) listConsumerGroups(c string) ([]string, error) {
	resp := struct {
		Err       bool
		Message   string
		Consumers []string
	}{}

	bresp, err := http.Get(fmt.Sprintf("%s/v2/kafka/%s/consumer", e.url, c))
	if err != nil {
		errors.Wrap(err, "request failed")
	}

	buf := bytes.Buffer{}
	io.Copy(&buf, bresp.Body)

	err = json.Unmarshal(buf.Bytes(), &resp)
	if err != nil {
		errors.Wrap(err, "unmarshal failed")
	}

	if resp.Err {
		return nil, errors.Errorf("list cnusters failed, %+v", resp.Message)
	}

	return resp.Consumers, nil
}

func (e *Exporter) listConsumerGroupTopics(c, cg string) ([]string, error) {
	resp := struct {
		Err     bool
		Message string
		Topics  []string
	}{}

	bresp, err := http.Get(fmt.Sprintf("%s/v2/kafka/%s/consumer/%s/topic", e.url, c, cg))
	if err != nil {
		errors.Wrap(err, "request failed")
	}

	buf := bytes.Buffer{}
	io.Copy(&buf, bresp.Body)

	err = json.Unmarshal(buf.Bytes(), &resp)
	if err != nil {
		errors.Wrap(err, "unmarshal failed")
	}

	if resp.Err {
		return nil, errors.Errorf("list cnusters failed, %+v", resp.Message)
	}

	return resp.Topics, nil
}

func (e *Exporter) collectTopicOffsets(c, t string) error {
	resp := struct {
		Err     bool
		Message string
		Offsets []int
	}{}

	bresp, err := http.Get(fmt.Sprintf("%s/v2/kafka/%s/topic/%s", e.url, c, t))
	if err != nil {
		errors.Wrap(err, "request failed")
	}

	buf := bytes.Buffer{}
	io.Copy(&buf, bresp.Body)

	err = json.Unmarshal(buf.Bytes(), &resp)
	if err != nil {
		errors.Wrap(err, "unmarshal failed")
	}

	if resp.Err {
		return errors.Errorf("get topic offsets failed, %+v", resp.Message)
	}

	for i, v := range resp.Offsets {
		e.toffset.WithLabelValues(c, t, strconv.Itoa(i)).Set(float64(v))
	}

	return nil
}

func (e *Exporter) collectConsumerGroupTopicOffsets(c, cg, t string) error {
	resp := struct {
		Err     bool
		Message string
		Offsets []int
	}{}

	bresp, err := http.Get(fmt.Sprintf("%s/v2/kafka/%s/consumer/%s/topic/%s", e.url, c, cg, t))
	if err != nil {
		errors.Wrap(err, "request failed")
	}

	buf := bytes.Buffer{}
	io.Copy(&buf, bresp.Body)

	err = json.Unmarshal(buf.Bytes(), &resp)
	if err != nil {
		errors.Wrap(err, "unmarshal failed")
	}

	if resp.Err {
		return errors.Errorf("list cnusters failed, %+v", resp.Message)
	}

	for i, v := range resp.Offsets {
		e.coffset.WithLabelValues(c, cg, t, strconv.Itoa(i)).Set(float64(v))
	}

	return nil
}
