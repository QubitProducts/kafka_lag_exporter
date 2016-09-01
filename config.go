package main

import (
	"bytes"
	"io"

	"github.com/pkg/errors"
	yaml "gopkg.in/yaml.v2"
)

type zkConfig struct {
	Quorum  []string
	Path    string
	Offsets bool
	Topic   string
}

type config struct {
	Blacklist []string
	Kafka     map[string]struct {
		Broker    []string
		Zookeeper zkConfig
	}
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
