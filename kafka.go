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
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"

	"github.com/Shopify/sarama"
	"github.com/golang/glog"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
)

type KafkaExporter struct {
	cluster            string
	client             sarama.Client
	masterConsumer     sarama.Consumer
	partitionConsumers []sarama.PartitionConsumer
	requestChannel     chan *BrokerTopicRequest
	messageChannel     chan *sarama.ConsumerMessage
	errorChannel       chan *sarama.ConsumerError
	wgFanIn            sync.WaitGroup
	wgProcessor        sync.WaitGroup

	toffsetDesc *prometheus.Desc
	coffsetDesc *prometheus.Desc

	topicMapLock sync.RWMutex
	topicMap     map[string]int

	zkExp *ZookeeperExporter
}

type BrokerTopicRequest struct {
	Result chan int
	Topic  string
}

func NewKafkaExporter(cluster string, cfg *kafkaConfig) (*KafkaExporter, error) {
	glog.Infof("Creating exporter for %#v %#v", cluster, *cfg)
	clientConfig := sarama.NewConfig()
	clientConfig.ClientID = cfg.ClientID

	//clientConfig.Net.TLS.Enable = profile.TLS
	//clientConfig.Net.TLS.Config = &tls.Config{}
	//clientConfig.Net.TLS.Config.InsecureSkipVerify = profile.TLSNoVerify

	sclient, err := sarama.NewClient(cfg.Brokers, clientConfig)
	if err != nil {
		return nil, err
	}

	// Create sarama master consumer
	master, err := sarama.NewConsumerFromClient(sclient)
	if err != nil {
		sclient.Close()
		return nil, err
	}

	toffsetDesc := prometheus.NewDesc(
		"kafka_topic_offset_messages",
		"The current offset of each topic partition.",
		[]string{"topic", "partition"},
		prometheus.Labels{"cluster": cluster},
	)

	coffsetDesc := prometheus.NewDesc(
		"kafka_consumer_offset_messages",
		"The current offset of consumers for each topic partition.",
		[]string{"consumer", "topic", "partition"},
		prometheus.Labels{"cluster": cluster},
	)

	var zkExp *ZookeeperExporter
	if cfg.Zookeeper.Offsets {
		zkExp, err = NewZookeeperExporter(cluster, coffsetDesc, &cfg.Zookeeper)
		if err != nil {
			return nil, errors.Wrap(err, "could not create zookeeper exporter")
		}
	}

	client := &KafkaExporter{
		cluster:        cluster,
		client:         sclient,
		masterConsumer: master,
		requestChannel: make(chan *BrokerTopicRequest),
		messageChannel: make(chan *sarama.ConsumerMessage),
		errorChannel:   make(chan *sarama.ConsumerError),
		wgFanIn:        sync.WaitGroup{},
		wgProcessor:    sync.WaitGroup{},
		topicMap:       make(map[string]int),
		topicMapLock:   sync.RWMutex{},

		toffsetDesc: toffsetDesc,
		coffsetDesc: coffsetDesc,

		zkExp: zkExp,
	}

	// Start the main processor goroutines for __consumer_offset messages
	client.wgProcessor.Add(2)
	go func() {
		defer client.wgProcessor.Done()
		for msg := range client.messageChannel {
			go client.processConsumerOffsetsMessage(msg)
		}
	}()

	go func() {
		defer client.wgProcessor.Done()
		for err := range client.errorChannel {
			glog.Errorf("Consume error on %s:%v: %v", err.Topic, err.Partition, err.Err)
		}
	}()

	// Start goroutine to handle topic metadata requests. Do this first because the getOffsets call needs this working
	client.RefreshTopicMap()
	go func() {
		for r := range client.requestChannel {
			client.getPartitionCount(r)
		}
	}()

	// Get a partition count for the consumption topic
	partitions, err := client.client.Partitions(cfg.Topic)
	if err != nil {
		return nil, err
	}

	// Start consumers for each partition with fan in
	client.partitionConsumers = make([]sarama.PartitionConsumer, len(partitions))
	glog.Infof("Starting consumers for %v partitions of %s in cluster %s", len(partitions), cfg.Topic, client.cluster)
	for i, partition := range partitions {
		pconsumer, err := client.masterConsumer.ConsumePartition(cfg.Topic, partition, sarama.OffsetNewest)
		if err != nil {
			return nil, err
		}
		client.partitionConsumers[i] = pconsumer
		client.wgFanIn.Add(2)
		go func() {
			defer client.wgFanIn.Done()
			for msg := range pconsumer.Messages() {
				client.messageChannel <- msg
			}
		}()
		go func() {
			defer client.wgFanIn.Done()
			for err := range pconsumer.Errors() {
				client.errorChannel <- err
			}
		}()
	}

	return client, nil
}

func (client *KafkaExporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- client.coffsetDesc
	ch <- client.toffsetDesc
}

func (client *KafkaExporter) Collect(ch chan<- prometheus.Metric) {
	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := client.collectPartitionOffsets(ch)
		if err != nil {
			glog.Errorf("%+v", errors.Wrap(err, "failed collecting partition offsets"))
		}
	}()

	if client.zkExp != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.zkExp.Collect(ch)
		}()
	}
	wg.Wait()
}

// This function performs massively parallel OffsetRequests, which is better than Sarama's internal implementation,
// which does one at a time. Several orders of magnitude faster.
func (client *KafkaExporter) collectPartitionOffsets(ch chan<- prometheus.Metric) error {
	// Start with refreshing the topic list
	client.RefreshTopicMap()

	requests := make(map[int32]*sarama.OffsetRequest)
	brokers := make(map[int32]*sarama.Broker)

	client.topicMapLock.RLock()
	defer client.topicMapLock.RUnlock()

	// Generate an OffsetRequest for each topic:partition and bucket it to the leader broker
	for topic, partitions := range client.topicMap {
		for i := 0; i < partitions; i++ {
			broker, err := client.client.Leader(topic, int32(i))
			if err != nil {
				glog.Errorf("Topic leader error on %s:%v: %v", topic, int32(i), err)
				return err
			}
			if _, ok := requests[broker.ID()]; !ok {
				requests[broker.ID()] = &sarama.OffsetRequest{}
			}
			brokers[broker.ID()] = broker
			requests[broker.ID()].AddBlock(topic, int32(i), sarama.OffsetNewest, 1)
		}
	}

	// Send out the OffsetRequest to each broker for all the partitions it is leader for
	// The results go to the offset storage module
	var wg sync.WaitGroup

	getBrokerOffsets := func(brokerID int32, request *sarama.OffsetRequest) {
		defer wg.Done()
		response, err := brokers[brokerID].GetAvailableOffsets(request)
		if err != nil {
			glog.Errorf("Cannot fetch offsets from broker %v: %v", brokerID, err)
			_ = brokers[brokerID].Close()
			return
		}
		//ts := time.Now().Unix() * 1000
		for topic, partitions := range response.Blocks {
			for partition, offsetResponse := range partitions {
				if offsetResponse.Err != sarama.ErrNoError {
					glog.Infof("Error in OffsetResponse for %s:%v from broker %v: %s", topic, partition, brokerID, offsetResponse.Err.Error())
					continue
				}
				ch <- prometheus.MustNewConstMetric(
					client.toffsetDesc,
					prometheus.GaugeValue,
					float64(offsetResponse.Offsets[0]),
					topic,
					strconv.Itoa(int(partition)),
				)
			}
		}
	}

	for brokerID, request := range requests {
		wg.Add(1)
		go getBrokerOffsets(brokerID, request)
	}

	wg.Wait()
	return nil
}

func (client *KafkaExporter) RefreshTopicMap() {
	client.topicMapLock.Lock()
	topics, _ := client.client.Topics()
	for _, topic := range topics {
		partitions, _ := client.client.Partitions(topic)
		client.topicMap[topic] = len(partitions)
	}
	client.topicMapLock.Unlock()
}

func (client *KafkaExporter) getPartitionCount(r *BrokerTopicRequest) {
	client.topicMapLock.RLock()
	if partitions, ok := client.topicMap[r.Topic]; ok {
		r.Result <- partitions
	} else {
		r.Result <- -1
	}
	client.topicMapLock.RUnlock()
}

func readString(buf *bytes.Buffer) (string, error) {
	var strlen uint16
	err := binary.Read(buf, binary.BigEndian, &strlen)
	if err != nil {
		return "", err
	}
	strbytes := make([]byte, strlen)
	n, err := buf.Read(strbytes)
	if (err != nil) || (n != int(strlen)) {
		return "", errors.New("string underflow")
	}
	return string(strbytes), nil
}

func (client *KafkaExporter) processConsumerOffsetsMessage(msg *sarama.ConsumerMessage) {
	var keyver, valver uint16
	var group, topic string
	var partition uint32
	var offset, timestamp uint64

	buf := bytes.NewBuffer(msg.Key)
	err := binary.Read(buf, binary.BigEndian, &keyver)
	switch keyver {
	case 0, 1:
		group, err = readString(buf)
		if err != nil {
			glog.Infof("Failed to decode %s:%v offset %v: group", msg.Topic, msg.Partition, msg.Offset)
			return
		}
		topic, err = readString(buf)
		if err != nil {
			glog.Infof("Failed to decode %s:%v offset %v: topic", msg.Topic, msg.Partition, msg.Offset)
			return
		}
		err = binary.Read(buf, binary.BigEndian, &partition)
		if err != nil {
			glog.Infof("Failed to decode %s:%v offset %v: partition", msg.Topic, msg.Partition, msg.Offset)
			return
		}
	case 2:
		log.Debugf("Discarding group metadata message with key version 2")
		return
	default:
		glog.Infof("Failed to decode %s:%v offset %v: keyver %v", msg.Topic, msg.Partition, msg.Offset, keyver)
		return
	}

	buf = bytes.NewBuffer(msg.Value)
	err = binary.Read(buf, binary.BigEndian, &valver)
	if (err != nil) || ((valver != 0) && (valver != 1)) {
		glog.Infof("Failed to decode %s:%v offset %v: valver %v", msg.Topic, msg.Partition, msg.Offset, valver)
		return
	}
	err = binary.Read(buf, binary.BigEndian, &offset)
	if err != nil {
		glog.Infof("Failed to decode %s:%v offset %v: offset", msg.Topic, msg.Partition, msg.Offset)
		return
	}
	_, err = readString(buf)
	if err != nil {
		glog.Infof("Failed to decode %s:%v offset %v: metadata", msg.Topic, msg.Partition, msg.Offset)
		return
	}
	err = binary.Read(buf, binary.BigEndian, &timestamp)
	if err != nil {
		glog.Infof("Failed to decode %s:%v offset %v: timestamp", msg.Topic, msg.Partition, msg.Offset)
		return
	}

	fmt.Printf("[%s,%s,%v]::OffsetAndMetadata[%v,%v]\n", group, topic, partition, offset, timestamp)
	// update stats, we'll need to collect them elsewhere
	return
}

func (client *KafkaExporter) collectConsumerOffsetsMessage(ch chan<- prometheus.Metric) {

	// ch <- prometheus.MustNewConstMetric(
	// 	client.coffsetDesc,
	// 	prometheus.GaugeValue,
	// 	float64(offset),
	// 	group,
	// 	topic,
	// 	strconv.Itoa(int(partition)),
	// )
}
