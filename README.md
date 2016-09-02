# Kafka Lag Exporter

This is a prometheus exporter to expose kafka consumer group lag, and partiion
offset via the prometheus metrics interface.

It builds on code from Burrow (https://github.com/linkedin/Burrow).

The following features currently work:

- Collect kafka topic offsets per partition
- Collect zookeeper based kafka consumer group offsets per topic and partition

Not working yet:

- black/white list of topics and gorups
- TLS support
- Consumer groups from kafka
- Consumer gorups from storm
- Defaults for config values read form yaml
