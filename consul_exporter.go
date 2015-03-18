package main

import (
	"flag"
	_ "fmt"
	_ "io"
	_ "io/ioutil"
	"log"
	_ "net"
	"net/http"
	_ "strconv"
	_ "strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
    "github.com/hashicorp/consul/api"
)

const (
	namespace = "consul"
)

var (
	serviceLabelNames = []string{"service","node"}
	memberLabelNames  = []string{"member"}
)

// Exporter collects HAProxy stats from the given URI and exports them using
// the prometheus metrics package.
type Exporter struct {
	URI   string
	mutex sync.RWMutex

	up, clusterServers                  prometheus.Gauge
	totalQueries, jsonParseFailures     prometheus.Counter
	serviceMetrics, lockMetrics         map[string]*prometheus.GaugeVec
	client                              *api.Client
}

func newServiceMetric(metricName string, docString string, constLabels prometheus.Labels) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace:   namespace,
			Name:        "service_" + metricName,
			Help:        docString,
			ConstLabels: constLabels,
		},
		serviceLabelNames,
	)
}

// NewExporter returns an initialized Exporter.
func NewExporter(uri string, consulLocks string, timeout time.Duration) *Exporter {
    // connect to Consul

    consul_client, _ := api.NewClient(&api.Config{
        Address: uri,
    })

    // init our exporter

	return &Exporter{
		URI: uri,
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Was the last query of Consul successful.",
		}),
		totalQueries: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "exporter_total_queries",
			Help:      "Current total Consul queries.",
		}),

		clusterServers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "cluster_servers",
			Help:      "How many peers are in the cluster.",
		}),

        serviceMetrics: map[string]*prometheus.GaugeVec{
            "nodes_healthy":    newServiceMetric("nodes","Number of nodes", prometheus.Labels{"healthy": "healthy"}),
            "nodes_unhealthy":  newServiceMetric("nodes","Number of nodes", prometheus.Labels{"healthy": "unhealthy"}),
        },

		client: consul_client,
    }
}

// Describe describes all the metrics ever exported by the HAProxy exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.up.Desc()
	ch <- e.totalQueries.Desc()
    ch <- e.clusterServers.Desc()
}

// Collect fetches the stats from configured HAProxy location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
    services := make(chan *api.ServiceEntry)

    go e.queryClient(services)

	e.mutex.Lock() // To protect metrics from concurrent collects.
	defer e.mutex.Unlock()

    // reset metrics
	for _, m := range e.serviceMetrics {
		m.Reset()
	}

    e.setMetrics(services)

	ch <- e.up
	ch <- e.totalQueries
    ch <- e.clusterServers
	e.collectMetrics(ch)
}

func (e *Exporter) queryClient(services chan<- *api.ServiceEntry) {
    defer close(services)

    e.totalQueries.Inc()

    // query and set new metrics
    peers, err := e.client.Status().Peers()

    if err != nil {
        e.up.Set(0)
        log.Printf("Query error is %v",err)
        return
    }

    // we'll use peers to decide that we're up
    e.up.Set(1)

    // how many servers?
    e.clusterServers.Set(float64(len(peers)))

    // query for services
    serviceNames, _, err := e.client.Catalog().Services(&api.QueryOptions{})

    for s := range serviceNames {
        s_entries, _, err := e.client.Health().Service(s,"",false,&api.QueryOptions{})

        if err != nil {
            log.Printf("Failed to query service health: %v", err)
            continue
        }

        for _, se := range s_entries {
            services <- se
        }
    }
}

func (e *Exporter) setMetrics(services <-chan *api.ServiceEntry) {
    for entry := range services {
        // we have a Node, a Service, and one or more Checks. Our
        // service-node combo is passing if all checks have a `status`
        // of "passing"

        passing := true

        for _, hc := range entry.Checks {
            if hc.Status != "passing" {
                passing = false
            }
        }

        log.Printf("%v/%v status is %v", entry.Service.Service, entry.Node.Node, passing)

        labels := []string{entry.Service.Service, entry.Node.Node}

        if passing {
            e.serviceMetrics["nodes_healthy"].WithLabelValues(labels...).Set(float64(1))
        } else {
            e.serviceMetrics["nodes_unhealthy"].WithLabelValues(labels...).Set(float64(1))
        }
    }
}

func (e *Exporter) collectMetrics(metrics chan<- prometheus.Metric) {
	for _, m := range e.serviceMetrics {
		m.Collect(metrics)
	}
}

func main() {
	var (
		listenAddress   = flag.String("web.listen-address", ":9105", "Address to listen on for web interface and telemetry.")
		metricsPath     = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		consulServer    = flag.String("consul.server", "localhost:8500", "URI for Consul server.")
		consulLocks     = flag.String("consul.locks", "", "If specified, keys to check for session locks. Comma-seperated list of keys.")
        consulTimeout   = flag.Duration("consul.timeout", 5*time.Second, "Timeout for trying to get stats from Consul.")
	)
	flag.Parse()

    exporter := NewExporter(*consulServer,*consulLocks, *consulTimeout)
    prometheus.MustRegister(exporter)

	log.Printf("Starting Server: %s", *listenAddress)
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