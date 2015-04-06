FROM       golang:1.4.2-onbuild
MAINTAINER Prometheus Team <prometheus-developers@googlegroups.com>

ENTRYPOINT [ "go-wrapper", "run" ]
CMD        [ "-logtostderr" ]
EXPOSE     9107
