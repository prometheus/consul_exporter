FROM        quay.io/prometheus/busybox:latest
LABEL maintainer="The Prometheus Authors <prometheus-developers@googlegroups.com>"

COPY consul_exporter /bin/consul_exporter

EXPOSE     9107
ENTRYPOINT [ "/bin/consul_exporter" ]
