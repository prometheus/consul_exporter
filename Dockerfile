FROM golang:1.4.2-onbuild
LABEL container.name="wehkamp/prometheus-consul-exporter:1.0.0"

ENTRYPOINT [ "go-wrapper", "run" ]
EXPOSE 9107
