package main

import (
	"bytes"
	"io"
	"time"

	"github.com/pkg/errors"
	yaml "gopkg.in/yaml.v2"
)

type zkConfig struct {
	Quorum  []string
	Path    string
	Offsets bool
	Timeout time.Duration
}

type kafkaConfig struct {
	ClientID  string
	Brokers   []string
	Topic     string
	Zookeeper zkConfig
}

type config struct {
	Blacklist []string
	Kafka     map[string]kafkaConfig
}

func readConfig(r io.Reader) (*config, error) {
	var cfg config
	var buf bytes.Buffer

	io.Copy(&buf, r)

	if err := yaml.Unmarshal(buf.Bytes(), &cfg); err != nil {
		return nil, errors.Wrap(err, "unable to read config")
	}

	return &cfg, nil
}
