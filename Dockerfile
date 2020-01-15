FROM golang:alpine AS build

RUN apk add --update bash curl git && rm -rf /var/cache/apk/*

ADD . /src
WORKDIR /src
RUN go build .

FROM alpine:latest
COPY --from=build /src/kafka_lag_exporter /bin/kafka_lag_exporter
ADD kafka_lag_exporter.yml /etc/kafka_lag_exporter.yml

WORKDIR /var/tmp

ENTRYPOINT ["/bin/kafka_lag_exporter", "-config", "/etc/kafka_lag_exporter.yml", "-logtostderr"]
