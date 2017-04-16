package main

import (
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"

	consul_api "github.com/hashicorp/consul/api"
	consul "github.com/hashicorp/consul/consul/structs"
	cleanhttp "github.com/hashicorp/go-cleanhttp"
)

const (
	namespace = "consul"
)

var (
	up = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Was the last query of Consul successful.",
		nil, nil,
	)
	clusterServers = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "raft_peers"),
		"How many peers (servers) are in the Raft cluster.",
		nil, nil,
	)
	clusterLeader = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "raft_leader"),
		"Does Raft cluster have a leader (according to this node).",
		nil, nil,
	)
	nodeCount = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "serf_lan_members"),
		"How many members are in the cluster.",
		nil, nil,
	)
	serviceCount = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "catalog_services"),
		"How many services are in the cluster.",
		nil, nil,
	)
	serviceNodesHealthy = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "catalog_service_node_healthy"),
		"Is this service healthy on this node?",
		[]string{"service_id", "node", "service_name", "status", "tags"}, nil,
	)
	nodeChecks = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "health_node_status"),
		"Status of health checks associated with a node.",
		[]string{"check", "node", "status"}, nil,
	)
	serviceChecks = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "health_service_status"),
		"Status of health checks associated with a service.",
		[]string{"check", "node", "service_id", "service_name", "status"}, nil,
	)
	keyValues = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "catalog_kv"),
		"The values for selected keys in Consul's key/value catalog. Keys with non-numeric values are omitted.",
		[]string{"key"}, nil,
	)
)

// Exporter collects Consul stats from the given server and exports them using
// the prometheus metrics package.
type Exporter struct {
	client        *consul_api.Client
	kvPrefix      string
	kvFilter      *regexp.Regexp
	healthSummary bool
}

type consulOpts struct {
	uri        string
	caFile     string
	certFile   string
	keyFile    string
	serverName string
	timeout    time.Duration
}

// NewExporter returns an initialized Exporter.
func NewExporter(opts consulOpts, kvPrefix, kvFilter string, healthSummary bool) (*Exporter, error) {
	uri := opts.uri
	if !strings.Contains(uri, "://") {
		uri = "http://" + uri
	}
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid consul URL: %s", err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid consul URL: %s", uri)
	}

	tlsConfig, err := consul_api.SetupTLSConfig(&consul_api.TLSConfig{
		Address:  opts.serverName,
		CAFile:   opts.caFile,
		CertFile: opts.certFile,
		KeyFile:  opts.keyFile,
	})
	if err != nil {
		return nil, err
	}
	transport := cleanhttp.DefaultPooledTransport()
	transport.TLSClientConfig = tlsConfig

	config := consul_api.DefaultConfig()
	config.Address = u.Host
	config.Scheme = u.Scheme
	config.HttpClient.Timeout = opts.timeout
	config.HttpClient.Transport = transport

	client, err := consul_api.NewClient(config)
	if err != nil {
		return nil, err
	}

	// Init our exporter.
	return &Exporter{
		client:        client,
		kvPrefix:      kvPrefix,
		kvFilter:      regexp.MustCompile(kvFilter),
		healthSummary: healthSummary,
	}, nil
}

// Describe describes all the metrics ever exported by the Consul exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- up
	ch <- clusterServers
	ch <- clusterLeader
	ch <- nodeCount
	ch <- serviceCount
	ch <- serviceNodesHealthy
	ch <- nodeChecks
	ch <- serviceChecks
	ch <- keyValues
}

// Collect fetches the stats from configured Consul location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	// How many peers are in the Consul cluster?
	peers, err := e.client.Status().Peers()
	if err != nil {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 0,
		)
		log.Errorf("Can't query consul: %v", err)
		return
	}

	// We'll use peers to decide that we're up.
	ch <- prometheus.MustNewConstMetric(
		up, prometheus.GaugeValue, 1,
	)
	ch <- prometheus.MustNewConstMetric(
		clusterServers, prometheus.GaugeValue, float64(len(peers)),
	)

	leader, err := e.client.Status().Leader()
	if err != nil {
		log.Errorf("Can't query consul: %v", err)
	}
	if len(leader) == 0 {
		ch <- prometheus.MustNewConstMetric(
			clusterLeader, prometheus.GaugeValue, 0,
		)
	} else {
		ch <- prometheus.MustNewConstMetric(
			clusterLeader, prometheus.GaugeValue, 1,
		)
	}

	// How many nodes are registered?
	nodes, _, err := e.client.Catalog().Nodes(&consul_api.QueryOptions{})
	if err != nil {
		// FIXME: How should we handle a partial failure like this?
	} else {
		ch <- prometheus.MustNewConstMetric(
			nodeCount, prometheus.GaugeValue, float64(len(nodes)),
		)
	}

	// Query for the full list of services.
	serviceNames, _, err := e.client.Catalog().Services(&consul_api.QueryOptions{})
	if err != nil {
		// FIXME: How should we handle a partial failure like this?
		return
	}
	ch <- prometheus.MustNewConstMetric(
		serviceCount, prometheus.GaugeValue, float64(len(serviceNames)),
	)

	if e.healthSummary {
		e.collectHealthSummary(ch, serviceNames)
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
			ch <- prometheus.MustNewConstMetric(
				nodeChecks, prometheus.GaugeValue, passing, hc.CheckID, hc.Node, hc.Status,
			)
		} else {
			ch <- prometheus.MustNewConstMetric(
				serviceChecks, prometheus.GaugeValue, passing, hc.CheckID, hc.Node, hc.ServiceID, hc.ServiceName, hc.Status,
			)
		}
	}

	e.collectKeyValues(ch)
}

// collectHealthSummary collects health information about every node+service
// combination. It will cause one lookup query per service.
func (e *Exporter) collectHealthSummary(ch chan<- prometheus.Metric, serviceNames map[string][]string) {
	for s := range serviceNames {
		service, _, err := e.client.Health().Service(s, "", false, &consul_api.QueryOptions{})
		if err != nil {
			log.Errorf("Failed to query service health: %v", err)
			continue
		}

		for _, entry := range service {
			aggregatedStatus := entry.Checks.AggregatedStatus()
			// We have a Node, a Service, and one or more Checks. Our
			// service-node combo is passing if all checks have a `status`
			// of "passing."
			passing := 1.
			for _, hc := range entry.Checks {
				if hc.Status != consul.HealthPassing {
					passing = 0
					break
				}
			}
			ch <- prometheus.MustNewConstMetric(
				serviceNodesHealthy, prometheus.GaugeValue, passing, entry.Service.ID, entry.Node.Node, entry.Service.Service, aggregatedStatus, strings.Join(entry.Service.Tags, ","),
			)
		}
	}
}

func (e *Exporter) collectKeyValues(ch chan<- prometheus.Metric) {
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
				ch <- prometheus.MustNewConstMetric(
					keyValues, prometheus.GaugeValue, val, pair.Key,
				)
			}
		}
	}
}

func init() {
	prometheus.MustRegister(version.NewCollector("consul_exporter"))
}

func main() {
	var (
		showVersion   = flag.Bool("version", false, "Print version information.")
		listenAddress = flag.String("web.listen-address", ":9107", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		healthSummary = flag.Bool("consul.health-summary", true, "Generate a health summary for each service instance. Needs n+1 queries to collect all information.")
		kvPrefix      = flag.String("kv.prefix", "", "Prefix from which to expose key/value pairs.")
		kvFilter      = flag.String("kv.filter", ".*", "Regex that determines which keys to expose.")

		opts = consulOpts{}
	)
	flag.StringVar(&opts.uri, "consul.server", "http://localhost:8500", "HTTP API address of a Consul server or agent. (prefix with https:// to connect over HTTPS)")
	flag.StringVar(&opts.caFile, "consul.ca-file", "", "File path to a PEM-encoded certificate authority used to validate the authenticity of a server certificate.")
	flag.StringVar(&opts.certFile, "consul.cert-file", "", "File path to a PEM-encoded certificate used with the private key to verify the exporter's authenticity.")
	flag.StringVar(&opts.keyFile, "consul.key-file", "", "File path to a PEM-encoded private key used with the certificate to verify the exporter's authenticity.")
	flag.StringVar(&opts.serverName, "consul.server-name", "", "When provided, this overrides the hostname for the TLS certificate. It can be used to ensure that the certificate name matches the hostname we declare.")
	flag.DurationVar(&opts.timeout, "consul.timeout", 200*time.Millisecond, "Timeout on HTTP requests to consul.")

	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Print("consul_exporter"))
		os.Exit(0)
	}

	log.Infoln("Starting consul_exporter", version.Info())
	log.Infoln("Build context", version.BuildContext())

	exporter, err := NewExporter(opts, *kvPrefix, *kvFilter, *healthSummary)
	if err != nil {
		log.Fatalln(err)
	}
	prometheus.MustRegister(exporter)

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

	log.Infoln("Listening on", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
