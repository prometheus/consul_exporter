package main

import (
	"flag"
	"net/http"
	_ "net/http/pprof"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/log"

	consul_api "github.com/hashicorp/consul/api"
	consul "github.com/hashicorp/consul/consul/structs"
)

const (
	namespace = "consul"
)

var (
	serviceLabelNames = []string{"service", "node"}
	memberLabelNames  = []string{"member"}
)

// Exporter collects Consul stats from the given server and exports them using
// the prometheus metrics package.
type Exporter struct {
	URI   string
	mutex sync.RWMutex

	up, clusterServers                                 prometheus.Gauge
	nodeCount, serviceCount                            prometheus.Counter
	serviceNodesTotal, serviceNodesHealthy, nodeChecks *prometheus.GaugeVec
	client                                             *consul_api.Client
}

// NewExporter returns an initialized Exporter.
func NewExporter(uri string) *Exporter {
	// Set up our Consul client connection.
	consul_client, _ := consul_api.NewClient(&consul_api.Config{
		Address: uri,
	})

	// Init our exporter.
	return &Exporter{
		URI: uri,
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Was the last query of Consul successful.",
		}),

		clusterServers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "raft_peers",
			Help:      "How many peers (servers) are in the Raft cluster.",
		}),

		nodeCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "serf_lan_members",
			Help:      "How many members are in the cluster.",
		}),

		serviceCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "catalog_services",
			Help:      "How many services are in the cluster.",
		}),

		serviceNodesTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "catalog_service_nodes",
				Help:      "Number of nodes currently registered for this service",
			},
			[]string{"service"},
		),

		serviceNodesHealthy: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "catalog_service_node_healthy",
				Help:      "Is this service healthy on this node?",
			},
			[]string{"service", "node"},
		),

		nodeChecks: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "agent_check",
				Help:      "Is this check passing on this node?",
			},
			[]string{"check", "node"},
		),

		client: consul_client,
	}
}

// Describe describes all the metrics ever exported by the Consul exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.up.Desc()
	ch <- e.nodeCount.Desc()
	ch <- e.serviceCount.Desc()
	ch <- e.clusterServers.Desc()

	e.serviceNodesTotal.Describe(ch)
	e.serviceNodesHealthy.Describe(ch)
}

// Collect fetches the stats from configured Consul location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	services := make(chan []*consul_api.ServiceEntry)
	checks := make(chan []*consul_api.HealthCheck)

	go e.queryClient(services, checks)

	e.mutex.Lock() // To protect metrics from concurrent collects.
	defer e.mutex.Unlock()

	// Reset metrics.
	e.serviceNodesTotal.Reset()
	e.serviceNodesHealthy.Reset()
	e.nodeChecks.Reset()

	e.setMetrics(services, checks)

	ch <- e.up
	ch <- e.clusterServers
	ch <- e.nodeCount
	ch <- e.serviceCount

	e.serviceNodesTotal.Collect(ch)
	e.serviceNodesHealthy.Collect(ch)
	e.nodeChecks.Collect(ch)
}

func (e *Exporter) queryClient(services chan<- []*consul_api.ServiceEntry, checks chan<- []*consul_api.HealthCheck) {

	defer close(services)
	defer close(checks)

	// How many peers are in the Consul cluster?
	peers, err := e.client.Status().Peers()

	if err != nil {
		e.up.Set(0)
		log.Errorf("Query error is %v", err)
		return
	}

	// We'll use peers to decide that we're up.
	e.up.Set(1)
	e.clusterServers.Set(float64(len(peers)))

	// How many nodes are registered?
	nodes, _, err := e.client.Catalog().Nodes(&consul_api.QueryOptions{})

	if err != nil {
		// FIXME: How should we handle a partial failure like this?
	} else {
		e.nodeCount.Set(float64(len(nodes)))
	}

	// Query for the full list of services.
	serviceNames, _, err := e.client.Catalog().Services(&consul_api.QueryOptions{})
	e.serviceCount.Set(float64(len(serviceNames)))

	if err != nil {
		// FIXME: How should we handle a partial failure like this?
		return
	}

	e.serviceCount.Set(float64(len(serviceNames)))

	for s := range serviceNames {
		s_entries, _, err := e.client.Health().Service(s, "", false, &consul_api.QueryOptions{})

		if err != nil {
			log.Errorf("Failed to query service health: %v", err)
			continue
		}

		services <- s_entries
	}

	c_entries, _, err := e.client.Health().State("any", &consul_api.QueryOptions{})
	if err != nil {
		log.Errorf("Failed to query service health: %v", err)

	} else {
		checks <- c_entries
	}

}

func (e *Exporter) setMetrics(services <-chan []*consul_api.ServiceEntry, checks <-chan []*consul_api.HealthCheck) {

	// Each service will be an array of ServiceEntry structs.
	running := true
	for running {
		select {
		case service, b := <-services:
			running = b
			if len(service) == 0 {
				// Not sure this should ever happen, but catch it just in case...
				continue
			}

			// We should have one ServiceEntry per node, so use that for total nodes.
			e.serviceNodesTotal.WithLabelValues(service[0].Service.Service).Set(float64(len(service)))

			for _, entry := range service {
				// We have a Node, a Service, and one or more Checks. Our
				// service-node combo is passing if all checks have a `status`
				// of "passing."

				passing := 1

				for _, hc := range entry.Checks {
					if hc.Status != consul.HealthPassing {
						passing = 0
						break
					}
				}

				log.Infof("%v/%v status is %v", entry.Service.Service, entry.Node.Node, passing)

				e.serviceNodesHealthy.WithLabelValues(entry.Service.Service, entry.Node.Node).Set(float64(passing))
			}
		case entry, b := <-checks:
			running = b
			for _, hc := range entry {
				passing := 1
				if hc.ServiceID == "" {
					if hc.Status != consul.HealthPassing {
						passing = 0
					}
					e.nodeChecks.WithLabelValues(hc.CheckID, hc.Node).Set(float64(passing))
					log.Infof("CHECKS: %v/%v status is %d", hc.CheckID, hc.Node, passing)
				}
			}
		}
	}

}

func main() {
	var (
		listenAddress = flag.String("web.listen-address", ":9107", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		consulServer  = flag.String("consul.server", "localhost:8500", "HTTP API address of a Consul server or agent.")
	)
	flag.Parse()

	exporter := NewExporter(*consulServer)
	prometheus.MustRegister(exporter)

	log.Infof("Starting Server: %s", *listenAddress)
	http.Handle(*metricsPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Consul Exporter</title></head>
             <body>
             <h1>Consul Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
