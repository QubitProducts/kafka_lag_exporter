/* Copyright 2015 LinkedIn Corp. Licensed under the Apache License, Version
 * 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 */

package main

import (
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/samuel/go-zookeeper/zk"
)

type ZookeeperExporter struct {
	cfg     *zkConfig
	cluster string
	conn    *zk.Conn

	zkGroupLock sync.RWMutex
	zkGroupList map[string]bool

	coffsetDesc *prometheus.Desc
}

func NewZookeeperExporter(cluster string, desc *prometheus.Desc, cfg *zkConfig) (*ZookeeperExporter, error) {
	dialer := func(network, address string, timeout time.Duration) (net.Conn, error) {
		conn, err := net.DialTimeout(network, address, timeout)
		if tconn, ok := conn.(*net.TCPConn); conn != nil && ok {
			tconn.SetKeepAlive(true)
			tconn.SetKeepAlivePeriod(keepAlive)
		}
		return conn, err
	}
	zkconn, _, err := zk.ConnectWithDialer(cfg.Quorum, cfg.Timeout, dialer)
	if err != nil {
		return nil, err
	}

	client := &ZookeeperExporter{
		cfg:         cfg,
		cluster:     cluster,
		conn:        zkconn,
		zkGroupLock: sync.RWMutex{},
		zkGroupList: make(map[string]bool),
		coffsetDesc: desc,
	}

	return client, nil
}

func (zkClient *ZookeeperExporter) Collect(ch chan<- prometheus.Metric) {
	zkClient.refreshConsumerGroups()

	// Make sure this group still exists
	zkClient.zkGroupLock.RLock()
	defer zkClient.zkGroupLock.RUnlock()

	wg := sync.WaitGroup{}
	for g := range zkClient.zkGroupList {
		wg.Add(1)
		go func(g string) {
			defer wg.Done()
			zkClient.collectOffsetsForConsumerGroup(ch, g)
		}(g)
	}
	wg.Wait()
}

func (zkClient *ZookeeperExporter) refreshConsumerGroups() {
	zkClient.zkGroupLock.Lock()
	defer zkClient.zkGroupLock.Unlock()

	consumerGroups, _, err := zkClient.conn.Children(zkClient.cfg.Path + "/consumers")
	if err != nil {
		// Can't read the consumers path. Bail for now
		glog.Errorf("Cannot get consumer group list for cluster %s: %s %s", zkClient.cluster, zkClient.cfg.Path+"/consumers", err)
		return
	}

	// Mark all existing groups false
	for consumerGroup := range zkClient.zkGroupList {
		zkClient.zkGroupList[consumerGroup] = false
	}

	// Check for new groups, mark existing groups true
	for _, consumerGroup := range consumerGroups {
		// Don't bother adding groups in the blacklist
		// if !zkClient.app.Storage.AcceptConsumerGroup(consumerGroup) {
		//		continue
		//	}

		zkClient.zkGroupList[consumerGroup] = true
	}

	// Delete groups that are still false
	for consumerGroup := range zkClient.zkGroupList {
		if !zkClient.zkGroupList[consumerGroup] {
			glog.Infof("Remove ZK consumer group %s from cluster %s", consumerGroup, zkClient.cluster)
			delete(zkClient.zkGroupList, consumerGroup)
		}
	}
}

func (zkClient *ZookeeperExporter) collectOffsetsForConsumerGroup(ch chan<- prometheus.Metric, consumerGroup string) {
	topics, _, err := zkClient.conn.Children(zkClient.cfg.Path + "/consumers/" + consumerGroup + "/offsets")
	switch {
	case err == nil:
		wg := sync.WaitGroup{}
		for _, topic := range topics {
			wg.Add(1)
			go func(t string) {
				defer wg.Done()
				zkClient.getOffsetsForTopic(ch, consumerGroup, t)
			}(topic)
		}
		wg.Wait()
	case err == zk.ErrNoNode:
		// If the node doesn't exist, it may be because the group is using Kafka-committed offsets. Skip it
		glog.Infof("Skip checking ZK offsets for group %s in cluster %s as the offsets path doesn't exist", consumerGroup, zkClient.cluster)
	default:
		glog.Infof("Cannot read topics for group %s in cluster %s: %s", consumerGroup, zkClient.cluster, err)
	}
}

func (zkClient *ZookeeperExporter) getOffsetsForTopic(ch chan<- prometheus.Metric, consumerGroup string, topic string) {
	partitions, _, err := zkClient.conn.Children(zkClient.cfg.Path + "/consumers/" + consumerGroup + "/offsets/" + topic)
	if err != nil {
		glog.Infof("Cannot read partitions for topic %s for group %s in cluster %s: %s", topic, consumerGroup, zkClient.cluster, err)
		return
	}

	wg := sync.WaitGroup{}
	for _, partition := range partitions {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			zkClient.getOffsetForPartition(ch, consumerGroup, topic, p)
		}(partition)
	}
	wg.Wait()
}

func (zkClient *ZookeeperExporter) getOffsetForPartition(ch chan<- prometheus.Metric, consumerGroup string, topic string, partition string) {
	offsetStr, zkNodeStat, err := zkClient.conn.Get(zkClient.cfg.Path + "/consumers/" + consumerGroup + "/offsets/" + topic + "/" + partition)
	if err != nil {
		glog.Infof("Failed to read partition %s:%v for group %s in cluster %s: %s", topic, partition, consumerGroup, zkClient.cluster, err)
		return
	}

	if false {
		glog.Infof("%+v", zkNodeStat)
	}

	offset, err := strconv.ParseInt(string(offsetStr), 10, 64)
	if err != nil {
		glog.Errorf("Offset value (%s) for partition %s:%v for group %s in cluster %s is not an integer", string(offsetStr), topic, partition, consumerGroup, zkClient.cluster)
		return
	}

	ch <- prometheus.MustNewConstMetric(
		zkClient.coffsetDesc,
		prometheus.GaugeValue,
		float64(offset),
		consumerGroup,
		topic,
		partition,
	)
}
