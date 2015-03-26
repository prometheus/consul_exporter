# Consul Exporter

Export Consul service health to Prometheus.

To run it:

```bash
make
./consul_exporter [flags]
```

### Flags

```bash
./consul_exporter --help
```

* __`consul.server`:__ Address (host and port) of the Consul instance we should
    connect to. This could be a local agent (`localhost:8500`, for instance), or
    the address of a Consul server.
* __`web.listen-address`:__ Address to listen on for web interface and telemetry.
* __`web.telemetry-path`:__ Path under which to expose metrics.

## Useful Queries

__Are my services healthy?__

    min(consul_catalog_service_node_healthy) by (service)

Values of 1 mean that all nodes for the service are passing. Values of 0 mean at least one node for the service is not passing.

__What service nodes are failing?__

    sum by (node, service)(consul_catalog_service_node_healthy == 0)
