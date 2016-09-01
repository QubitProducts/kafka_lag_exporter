# Kafka Lag Exporter

This is a prometheus exporter to expose kafka consumer group lag, and partiion
offset via the prometheus metrics interface.

At the present time this is a simple re-export of the metrics provided by the
Burrow (https://github.com/linkedin/Burrow).
