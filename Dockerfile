FROM golang:alpine

RUN apk add --update bash curl git && rm -rf /var/cache/apk/*

ADD . $GOPATH/src/github.com/QubitProducts/kafka_lag_exporter
RUN cd $GOPATH/src/github.com/QubitProducts/kafka_lag_exporter && go install

ADD kafka_lag_exporter.yml /etc/kafka_lag_exporter.yml

WORKDIR /var/tmp

CMD ["/go/bin/kafka_lag_exporter", "-config", "/etc/kafka_lag_exporter.yml", "-logtostderr"]
