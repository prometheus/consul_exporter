# Consul Exporter

Export Consul service health to Prometheus.

To run it:

```bash
go run haproxy_exporter [flags]
```

Help on flags:
```bash
go run haproxy_exporter --help
```

## Useful Queries

__Are my services healthy?__

    min(consul_catalog_service_node_healthy) by (service)

Values of 1 mean that all nodes for the service are passing. Values of 0 mean at least one node for the service is not passing.

__What service nodes are failing?__

    consul_catalog_service_node_healthy == 0

Or, to isolate just the service and node labels: `drop_common_labels(consul_catalog_service_node_healthy == 0)`


