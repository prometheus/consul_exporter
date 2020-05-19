// Copyright 2019 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"gopkg.in/alecthomas/kingpin.v2"

	consul_api "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-cleanhttp"
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
	memberStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "serf_lan_member_status"),
		"Status of member in the cluster. 1=Alive, 2=Leaving, 3=Left, 4=Failed.",
		[]string{"member"}, nil,
	)
	serviceCount = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "catalog_services"),
		"How many services are in the cluster.",
		nil, nil,
	)
	serviceTag = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "service_tag"),
		"Tags of a service.",
		[]string{"service_id", "node", "tag"}, nil,
	)
	serviceNodesHealthy = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "catalog_service_node_healthy"),
		"Is this service healthy on this node?",
		[]string{"service_id", "node", "service_name"}, nil,
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
	serviceCheckNames = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "service_checks"),
		"Link the service id and check name if available.",
		[]string{"service_id", "service_name", "check_id", "check_name"}, nil,
	)
	keyValues = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "catalog_kv"),
		"The values for selected keys in Consul's key/value catalog. Keys with non-numeric values are omitted.",
		[]string{"key"}, nil,
	)
	queryOptions = consul_api.QueryOptions{}
)

type promHTTPLogger struct {
	logger log.Logger
}

func (l promHTTPLogger) Println(v ...interface{}) {
	level.Error(l.logger).Log("msg", fmt.Sprint(v...))
}

// Exporter collects Consul stats from the given server and exports them using
// the prometheus metrics package.
type Exporter struct {
	client           *consul_api.Client
	kvPrefix         string
	kvFilter         *regexp.Regexp
	healthSummary    bool
	logger           log.Logger
	requestLimitChan chan struct{}
}

type consulOpts struct {
	uri          string
	caFile       string
	certFile     string
	keyFile      string
	serverName   string
	timeout      time.Duration
	insecure     bool
	requestLimit int
}

// NewExporter returns an initialized Exporter.
func NewExporter(opts consulOpts, kvPrefix, kvFilter string, healthSummary bool, logger log.Logger) (*Exporter, error) {
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
		Address:            opts.serverName,
		CAFile:             opts.caFile,
		CertFile:           opts.certFile,
		KeyFile:            opts.keyFile,
		InsecureSkipVerify: opts.insecure,
	})
	if err != nil {
		return nil, err
	}
	transport := cleanhttp.DefaultPooledTransport()
	transport.TLSClientConfig = tlsConfig

	config := consul_api.DefaultConfig()
	config.Address = u.Host
	config.Scheme = u.Scheme
	if config.HttpClient == nil {
		config.HttpClient = &http.Client{}
	}
	config.HttpClient.Timeout = opts.timeout
	config.HttpClient.Transport = transport

	client, err := consul_api.NewClient(config)
	if err != nil {
		return nil, err
	}

	var requestLimitChan chan struct{}
	if opts.requestLimit > 0 {
		requestLimitChan = make(chan struct{}, opts.requestLimit)
	}

	// Init our exporter.
	return &Exporter{
		client:           client,
		kvPrefix:         kvPrefix,
		kvFilter:         regexp.MustCompile(kvFilter),
		healthSummary:    healthSummary,
		logger:           logger,
		requestLimitChan: requestLimitChan,
	}, nil
}

// Describe describes all the metrics ever exported by the Consul exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- up
	ch <- clusterServers
	ch <- clusterLeader
	ch <- nodeCount
	ch <- memberStatus
	ch <- serviceCount
	ch <- serviceNodesHealthy
	ch <- nodeChecks
	ch <- serviceChecks
	ch <- keyValues
	ch <- serviceTag
	ch <- serviceCheckNames
}

// Collect fetches the stats from configured Consul location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	ok := e.collectPeersMetric(ch)
	ok = e.collectLeaderMetric(ch) && ok
	ok = e.collectNodesMetric(ch) && ok
	ok = e.collectMembersMetric(ch) && ok
	ok = e.collectServicesMetric(ch) && ok
	ok = e.collectHealthStateMetric(ch) && ok
	ok = e.collectKeyValues(ch) && ok

	if ok {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 1.0,
		)
	} else {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 0.0,
		)
	}
}

func (e *Exporter) collectPeersMetric(ch chan<- prometheus.Metric) bool {
	peers, err := e.client.Status().Peers()
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't query consul", "err", err)
		return false
	}
	ch <- prometheus.MustNewConstMetric(
		clusterServers, prometheus.GaugeValue, float64(len(peers)),
	)
	return true
}

func (e *Exporter) collectLeaderMetric(ch chan<- prometheus.Metric) bool {
	leader, err := e.client.Status().Leader()
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't query consul", "err", err)
		return false
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
	return true
}

func (e *Exporter) collectNodesMetric(ch chan<- prometheus.Metric) bool {
	nodes, _, err := e.client.Catalog().Nodes(&queryOptions)
	if err != nil {
		level.Error(e.logger).Log("msg", "Failed to query catalog for nodes", "err", err)
		return false
	}
	ch <- prometheus.MustNewConstMetric(
		nodeCount, prometheus.GaugeValue, float64(len(nodes)),
	)
	return true
}

func (e *Exporter) collectMembersMetric(ch chan<- prometheus.Metric) bool {
	members, err := e.client.Agent().Members(false)
	if err != nil {
		level.Error(e.logger).Log("msg", "Failed to query member status", "err", err)
		return false
	}
	for _, entry := range members {
		ch <- prometheus.MustNewConstMetric(
			memberStatus, prometheus.GaugeValue, float64(entry.Status), entry.Name,
		)
	}
	return true
}

func (e *Exporter) collectServicesMetric(ch chan<- prometheus.Metric) bool {
	serviceNames, _, err := e.client.Catalog().Services(&queryOptions)
	if err != nil {
		level.Error(e.logger).Log("msg", "Failed to query for services", "err", err)
		return false
	}
	ch <- prometheus.MustNewConstMetric(
		serviceCount, prometheus.GaugeValue, float64(len(serviceNames)),
	)
	if e.healthSummary {
		if ok := e.collectHealthSummary(ch, serviceNames); !ok {
			return false
		}
	}
	return true
}

func (e *Exporter) collectHealthStateMetric(ch chan<- prometheus.Metric) bool {
	checks, _, err := e.client.Health().State("any", &queryOptions)
	if err != nil {
		level.Error(e.logger).Log("msg", "Failed to query service health", "err", err)
		return false
	}
	for _, hc := range checks {
		var passing, warning, critical, maintenance float64

		switch hc.Status {
		case consul_api.HealthPassing:
			passing = 1
		case consul_api.HealthWarning:
			warning = 1
		case consul_api.HealthCritical:
			critical = 1
		case consul_api.HealthMaint:
			maintenance = 1
		}

		if hc.ServiceID == "" {
			ch <- prometheus.MustNewConstMetric(
				nodeChecks, prometheus.GaugeValue, passing, hc.CheckID, hc.Node, consul_api.HealthPassing,
			)
			ch <- prometheus.MustNewConstMetric(
				nodeChecks, prometheus.GaugeValue, warning, hc.CheckID, hc.Node, consul_api.HealthWarning,
			)
			ch <- prometheus.MustNewConstMetric(
				nodeChecks, prometheus.GaugeValue, critical, hc.CheckID, hc.Node, consul_api.HealthCritical,
			)
			ch <- prometheus.MustNewConstMetric(
				nodeChecks, prometheus.GaugeValue, maintenance, hc.CheckID, hc.Node, consul_api.HealthMaint,
			)
		} else {
			ch <- prometheus.MustNewConstMetric(
				serviceChecks, prometheus.GaugeValue, passing, hc.CheckID, hc.Node, hc.ServiceID, hc.ServiceName, consul_api.HealthPassing,
			)
			ch <- prometheus.MustNewConstMetric(
				serviceChecks, prometheus.GaugeValue, warning, hc.CheckID, hc.Node, hc.ServiceID, hc.ServiceName, consul_api.HealthWarning,
			)
			ch <- prometheus.MustNewConstMetric(
				serviceChecks, prometheus.GaugeValue, critical, hc.CheckID, hc.Node, hc.ServiceID, hc.ServiceName, consul_api.HealthCritical,
			)
			ch <- prometheus.MustNewConstMetric(
				serviceChecks, prometheus.GaugeValue, maintenance, hc.CheckID, hc.Node, hc.ServiceID, hc.ServiceName, consul_api.HealthMaint,
			)
			ch <- prometheus.MustNewConstMetric(
				serviceCheckNames, prometheus.GaugeValue, 1, hc.ServiceID, hc.ServiceName, hc.CheckID, hc.Name,
			)
		}
	}
	return true
}

// collectHealthSummary collects health information about every node+service
// combination. It will cause one lookup query per service.
func (e *Exporter) collectHealthSummary(ch chan<- prometheus.Metric, serviceNames map[string][]string) bool {
	ok := make(chan bool)

	for s := range serviceNames {
		if e.requestLimitChan != nil {
			e.requestLimitChan <- struct{}{}
		}
		go func(s string) {
			defer func() {
				if e.requestLimitChan != nil {
					<-e.requestLimitChan
				}
			}()
			ok <- e.collectOneHealthSummary(ch, s)
		}(s)
	}

	allOK := true
	for range serviceNames {
		allOK = <-ok && allOK
	}
	close(ok)

	return allOK
}

func (e *Exporter) collectOneHealthSummary(ch chan<- prometheus.Metric, serviceName string) bool {
	// See https://github.com/hashicorp/consul/issues/1096.
	if strings.HasPrefix(serviceName, "/") {
		level.Warn(e.logger).Log("msg", "Skipping service because it starts with a slash", "service_name", serviceName)
		return true
	}
	level.Debug(e.logger).Log("msg", "Fetching health summary", "serviceName", serviceName)

	service, _, err := e.client.Health().Service(serviceName, "", false, &queryOptions)
	if err != nil {
		level.Error(e.logger).Log("msg", "Failed to query service health", "err", err)
		return false
	}

	for _, entry := range service {
		// We have a Node, a Service, and one or more Checks. Our
		// service-node combo is passing if all checks have a `status`
		// of "passing."
		passing := 1.
		for _, hc := range entry.Checks {
			if hc.Status != consul_api.HealthPassing {
				passing = 0
				break
			}
		}
		ch <- prometheus.MustNewConstMetric(
			serviceNodesHealthy, prometheus.GaugeValue, passing, entry.Service.ID, entry.Node.Node, entry.Service.Service,
		)
		tags := make(map[string]struct{})
		for _, tag := range entry.Service.Tags {
			if _, ok := tags[tag]; ok {
				continue
			}
			ch <- prometheus.MustNewConstMetric(serviceTag, prometheus.GaugeValue, 1, entry.Service.ID, entry.Node.Node, tag)
			tags[tag] = struct{}{}
		}
	}
	return true
}

func (e *Exporter) collectKeyValues(ch chan<- prometheus.Metric) bool {
	if e.kvPrefix == "" {
		return true
	}

	kv := e.client.KV()
	pairs, _, err := kv.List(e.kvPrefix, &queryOptions)
	if err != nil {
		level.Error(e.logger).Log("msg", "Error fetching key/values", "err", err)
		return false
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
	return true
}

func init() {
	prometheus.MustRegister(version.NewCollector("consul_exporter"))
}

func main() {
	var (
		listenAddress = kingpin.Flag("web.listen-address", "Address to listen on for web interface and telemetry.").Default(":9107").String()
		metricsPath   = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").String()
		healthSummary = kingpin.Flag("consul.health-summary", "Generate a health summary for each service instance. Needs n+1 queries to collect all information.").Default("true").Bool()
		kvPrefix      = kingpin.Flag("kv.prefix", "Prefix from which to expose key/value pairs.").Default("").String()
		kvFilter      = kingpin.Flag("kv.filter", "Regex that determines which keys to expose.").Default(".*").String()

		opts = consulOpts{}
	)
	kingpin.Flag("consul.server", "HTTP API address of a Consul server or agent. (prefix with https:// to connect over HTTPS)").Default("http://localhost:8500").StringVar(&opts.uri)
	kingpin.Flag("consul.ca-file", "File path to a PEM-encoded certificate authority used to validate the authenticity of a server certificate.").Default("").StringVar(&opts.caFile)
	kingpin.Flag("consul.cert-file", "File path to a PEM-encoded certificate used with the private key to verify the exporter's authenticity.").Default("").StringVar(&opts.certFile)
	kingpin.Flag("consul.key-file", "File path to a PEM-encoded private key used with the certificate to verify the exporter's authenticity.").Default("").StringVar(&opts.keyFile)
	kingpin.Flag("consul.server-name", "When provided, this overrides the hostname for the TLS certificate. It can be used to ensure that the certificate name matches the hostname we declare.").Default("").StringVar(&opts.serverName)
	kingpin.Flag("consul.timeout", "Timeout on HTTP requests to the Consul API.").Default("500ms").DurationVar(&opts.timeout)
	kingpin.Flag("consul.insecure", "Disable TLS host verification.").Default("false").BoolVar(&opts.insecure)
	kingpin.Flag("consul.request-limit", "Limit the maximum number of concurrent requests to consul, 0 means no limit.").Default("0").IntVar(&opts.requestLimit)

	// Query options.
	kingpin.Flag("consul.allow_stale", "Allows any Consul server (non-leader) to service a read.").Default("true").BoolVar(&queryOptions.AllowStale)
	kingpin.Flag("consul.require_consistent", "Forces the read to be fully consistent.").Default("false").BoolVar(&queryOptions.RequireConsistent)

	promlogConfig := &promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, promlogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(promlogConfig)

	level.Info(logger).Log("msg", "Starting consul_exporter", "version", version.Info())
	level.Info(logger).Log("build_context", version.BuildContext())

	exporter, err := NewExporter(opts, *kvPrefix, *kvFilter, *healthSummary, logger)
	if err != nil {
		level.Error(logger).Log("msg", "Error creating the exporter", "err", err)
		os.Exit(1)
	}
	prometheus.MustRegister(exporter)

	queryOptionsJson, err := json.MarshalIndent(queryOptions, "", "    ")
	if err != nil {
		level.Error(logger).Log("msg", "Error marshaling query options", "err", err)
		os.Exit(1)
	}

	http.Handle(*metricsPath,
		promhttp.InstrumentMetricHandler(
			prometheus.DefaultRegisterer,
			promhttp.HandlerFor(
				prometheus.DefaultGatherer,
				promhttp.HandlerOpts{
					ErrorLog: &promHTTPLogger{
						logger: logger,
					},
				},
			),
		),
	)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Consul Exporter</title></head>
             <body>
             <h1>Consul Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             <h2>Options</h2>
             <pre>` + string(queryOptionsJson) + `</pre>
             </dl>
             <h2>Build</h2>
             <pre>` + version.Info() + ` ` + version.BuildContext() + `</pre>
             </body>
             </html>`))
	})
	http.HandleFunc("/-/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})
	http.HandleFunc("/-/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	level.Info(logger).Log("msg", "Listening on address", "address", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, nil); err != nil {
		level.Error(logger).Log("msg", "Error starting HTTP server", "err", err)
		os.Exit(1)
	}
}
