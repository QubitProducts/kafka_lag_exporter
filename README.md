# Kafka Lag Exporter

This is a prometheus exporter to expose kafka consumer group lag, and partiion
offset via the prometheus metrics interface.

It builds on code from Burrow (https://github.com/linkedin/Burrow).

The following features currently work:

- Collect zookeeper based kafka consumer group offsets per topic and partition
- Collect kafka topic offsets per partition

Not working yet:

- Consumer groups from kafka
- Consumer groups from storm
- black/white list of topics and gorups
- TLS support
- Defaults for config values read form yaml
