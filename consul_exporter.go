package main

import (
	"flag"
	"net/http"
	_ "net/http/pprof"
	"regexp"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/log"

	consul_api "github.com/hashicorp/consul/api"
	consul "github.com/hashicorp/consul/consul/structs"
)

const (
	namespace = "consul"
)

// Exporter collects Consul stats from the given server and exports them using
// the prometheus metrics package.
type Exporter struct {
	URI string
	sync.RWMutex

	up, clusterServers                                        prometheus.Gauge
	nodeCount, serviceCount                                   prometheus.Counter
	serviceNodesHealthy, nodeChecks, serviceChecks, keyValues *prometheus.GaugeVec
	client                                                    *consul_api.Client
	kvPrefix                                                  string
	kvFilter                                                  *regexp.Regexp
	healthSummary                                             bool
}

// NewExporter returns an initialized Exporter.
func NewExporter(uri, kvPrefix, kvFilter string, healthSummary bool) *Exporter {
	// Set up our Consul client connection.
	client, _ := consul_api.NewClient(&consul_api.Config{
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
				Name:      "health_node_status",
				Help:      "Status of health checks associated with a node.",
			},
			[]string{"check", "node"},
		),
		serviceChecks: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "health_service_status",
				Help:      "Status of health checks associated with a service.",
			},
			[]string{"check", "node", "service"},
		),
		keyValues: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "catalog_kv",
				Help:      "The values for selected keys in Consul's key/value catalog. Keys with non-numeric values are omitted.",
			},
			[]string{"key"},
		),
		client:        client,
		kvPrefix:      kvPrefix,
		kvFilter:      regexp.MustCompile(kvFilter),
		healthSummary: healthSummary,
	}
}

// Describe describes all the metrics ever exported by the Consul exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.up.Desc()
	ch <- e.nodeCount.Desc()
	ch <- e.serviceCount.Desc()
	ch <- e.clusterServers.Desc()

	e.serviceNodesHealthy.Describe(ch)
	e.keyValues.Describe(ch)
}

// Collect fetches the stats from configured Consul location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.Lock() // To protect metrics from concurrent collects.
	defer e.Unlock()

	// Reset metrics.
	e.serviceNodesHealthy.Reset()
	e.nodeChecks.Reset()
	e.serviceChecks.Reset()

	e.collect()

	ch <- e.up
	ch <- e.clusterServers
	ch <- e.nodeCount
	ch <- e.serviceCount

	e.serviceNodesHealthy.Collect(ch)
	e.nodeChecks.Collect(ch)
	e.serviceChecks.Collect(ch)

	e.keyValues.Reset()
	e.setKeyValues()
	e.keyValues.Collect(ch)
}

func (e *Exporter) collect() {
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
	if err != nil {
		// FIXME: How should we handle a partial failure like this?
		return
	}
	e.serviceCount.Set(float64(len(serviceNames)))

	if e.healthSummary {
		e.collectHealthSummary(serviceNames)
	}

	checks, _, err := e.client.Health().State("any", &consul_api.QueryOptions{})
	if err != nil {
		log.Errorf("Failed to query service health: %v", err)
		return
	}

	for _, hc := range checks {
		var passing float64
		if hc.Status == consul.HealthPassing {
			passing = 1
		}
		if hc.ServiceID == "" {
			e.nodeChecks.WithLabelValues(hc.CheckID, hc.Node).Set(passing)
		} else {
			e.serviceChecks.WithLabelValues(hc.CheckID, hc.Node, hc.ServiceID).Set(passing)
		}
	}
}

// collectHealthSummary collects health information about every node+service
// combination. It will cause one lookup query per service.
func (e *Exporter) collectHealthSummary(serviceNames map[string][]string) {
	for s := range serviceNames {
		service, _, err := e.client.Health().Service(s, "", false, &consul_api.QueryOptions{})
		if err != nil {
			log.Errorf("Failed to query service health: %v", err)
			continue
		}

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
			e.serviceNodesHealthy.WithLabelValues(entry.Service.ID, entry.Node.Node).Set(float64(passing))
		}
	}
}

func (e *Exporter) setKeyValues() {
	if e.kvPrefix == "" {
		return
	}

	kv := e.client.KV()
	pairs, _, err := kv.List(e.kvPrefix, &consul_api.QueryOptions{})
	if err != nil {
		log.Errorf("Error fetching key/values: %s", err)
		return
	}

	for _, pair := range pairs {
		if e.kvFilter.MatchString(pair.Key) {
			val, err := strconv.ParseFloat(string(pair.Value), 64)
			if err == nil {
				e.keyValues.WithLabelValues(pair.Key).Set(val)
			}
		}
	}
}

func main() {
	var (
		listenAddress = flag.String("web.listen-address", ":9107", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		consulServer  = flag.String("consul.server", "localhost:8500", "HTTP API address of a Consul server or agent.")
		healthSummary = flag.Bool("consul.health-summary", true, "Generate a health summary for each service instance. Needs n+1 queries to collect all information.")
		kvPrefix      = flag.String("kv.prefix", "", "Prefix from which to expose key/value pairs.")
		kvFilter      = flag.String("kv.filter", ".*", "Regex that determines which keys to expose.")
	)
	flag.Parse()

	exporter := NewExporter(*consulServer, *kvPrefix, *kvFilter, *healthSummary)
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
