# Consul Exporter [![Build Status](https://travis-ci.org/prometheus/consul_exporter.svg)][travis]

[![CircleCI](https://circleci.com/gh/prometheus/consul_exporter/tree/master.svg?style=shield)][circleci]
[![Docker Repository on Quay](https://quay.io/repository/prometheus/consul-exporter/status)][quay]
[![Docker Pulls](https://img.shields.io/docker/pulls/prom/consul-exporter.svg?maxAge=604800)][hub]

Export Consul service health to Prometheus.

To run it:

```bash
make
./consul_exporter [flags]
```

## Exported Metrics

| Metric | Meaning | Labels |
| ------ | ------- | ------ |
| consul_up | Was the last query of Consul successful | |
| consul_raft_peers | How many peers (servers) are in the Raft cluster | |
| consul_serf_lan_members | How many members are in the cluster | |
| consul_catalog_services | How many services are in the cluster | |
| consul_catalog_service_node_healthy | Is this service healthy on this node | service, node |
| consul_health_node_status | Status of health checks associated with a node | check, node |
| consul_health_service_status | Status of health checks associated with a service | check, node, service |
| consul_catalog_kv | The values for selected keys in Consul's key/value catalog. Keys with non-numeric values are omitted | key |

### Flags

```bash
./consul_exporter --help
```

* __`consul.server`:__ Address (host and port) of the Consul instance we should
    connect to. This could be a local agent (`localhost:8500`, for instance), or
    the address of a Consul server.
* __`consul.health-summary`:__ Collects information about each registered
  service and exports `consul_catalog_service_node_healthy`. This requires n+1
  Consul API queries to gather all information about each service. Health check
  information are available via `consul_health_service_status` as well, but
  only for services which have a health check configured. Defaults to true.
* __`web.listen-address`:__ Address to listen on for web interface and telemetry.
* __`web.telemetry-path`:__ Path under which to expose metrics.
* __`log.level`:__ Logging level. `info` by default.

#### Key/Value Checks

This exporter supports grabbing key/value pairs from Consul's KV store and
exposing them to Prometheus. This can be useful, for instance, if you use
Consul KV to store your intended cluster size, and want to graph that value
against the actual value found via monitoring.

* __`kv.prefix`:__ Prefix under which to look for KV pairs.
* __`kv.filter`:__ Only store keys that match this regex pattern.

A prefix must be supplied to activate this feature. Pass `/` if you want to
search the entire keyspace.

## Environment variables

The consul\_exporter supports all environment variables provided by the official
[consul/api package](https://github.com/hashicorp/consul/blob/c744792fc4d665363dba0ecfc7d05fdedc9cab32/api/api.go#L23-L43),
including `CONSUL_HTTP_TOKEN` to set the [ACL](https://www.consul.io/docs/internals/acl.html) token.

## Useful Queries

__Are my services healthy?__

    min(consul_catalog_service_node_healthy) by (service)

Values of 1 mean that all nodes for the service are passing. Values of 0 mean at least one node for the service is not passing.

__What service nodes are failing?__

    sum by (node, service)(consul_catalog_service_node_healthy == 0)

## Using Docker

You can deploy this exporter using the [prom/consul-exporter](https://registry.hub.docker.com/u/prom/consul-exporter/) Docker image.

For example:

```bash
docker pull prom/consul-exporter

docker run -d -p 9107:9107 prom/consul-exporter -consul.server=172.17.0.1:8500
```

Keep in mind that your container needs to be able to communicate with the Consul server or agent. Use an IP accessible from the container or set the `--dns` and `--dns-search` options of the `docker run` command:

```bash
docker run -d -p 9107:9107 --dns=172.17.0.1 --dns-search=service.consul \
        prom/consul-exporter -consul.server=consul:8500
```


[circleci]: https://circleci.com/gh/prometheus/consul_exporter
[hub]: https://hub.docker.com/r/prom/consul-exporter/
[travis]: https://travis-ci.org/prometheus/consul_exporter
[quay]: https://quay.io/repository/prometheus/consul-exporter
