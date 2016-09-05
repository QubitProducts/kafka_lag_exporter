package main

import (
	"bytes"
	"io"
	"regexp"
	"time"

	"github.com/pkg/errors"
	yaml "gopkg.in/yaml.v2"
)

type zkConfig struct {
	Quorum  []string
	Path    string
	Offsets bool
	Timeout *time.Duration
}

type kafkaConfig struct {
	ClientID  string `yaml:"client-id"`
	Brokers   []string
	Topic     string
	Zookeeper zkConfig
}

type config struct {
	ClientID string `yaml:"client-id"`

	GroupWhitelist []string
	gwhitelistRe   []*regexp.Regexp
	GroupBlacklist []string
	gblacklistRe   []*regexp.Regexp
	TopicWhitelist []string
	twhitelistRe   []*regexp.Regexp
	TopicBlacklist []string
	tblacklistRe   []*regexp.Regexp
	Kafka          map[string]kafkaConfig
}

func readConfig(r io.Reader) (*config, error) {
	var cfg config
	var buf bytes.Buffer
	var err error

	io.Copy(&buf, r)

	if err := yaml.Unmarshal(buf.Bytes(), &cfg); err != nil {
		return nil, errors.Wrap(err, "unable to read config")
	}

	cfg.gwhitelistRe, err = compileReList(cfg.GroupWhitelist)
	if err != nil {
		return nil, errors.Wrap(err, "bad group white list")
	}

	cfg.twhitelistRe, err = compileReList(cfg.TopicWhitelist)
	if err != nil {
		return nil, errors.Wrap(err, "bad topic white list")
	}

	cfg.gblacklistRe, err = compileReList(cfg.GroupBlacklist)
	if err != nil {
		return nil, errors.Wrap(err, "bad group black list")
	}

	cfg.tblacklistRe, err = compileReList(cfg.TopicBlacklist)
	if err != nil {
		return nil, errors.Wrap(err, "bad group white list")
	}

	return &cfg, nil
}

func compileReList(strs []string) ([]*regexp.Regexp, error) {
	var res []*regexp.Regexp
	for _, str := range strs {
		re, err := regexp.Compile(str)
		if err != nil {
			return nil, errors.Wrapf(err, "could not compile regex %v", str)
		}
		res = append(res, re)
	}

	return res, nil
}
