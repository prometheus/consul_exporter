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

package exporter

import (
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

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
	memberInfo = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "serf_lan_member_info"),
		"Information of member in the cluster.",
		[]string{"member", "role", "version"}, nil,
	)
	memberStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "serf_lan_member_status"),
		"Status of member in the cluster. 1=Alive, 2=Leaving, 3=Left, 4=Failed.",
		[]string{"member"}, nil,
	)
	memberWanInfo = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "serf_wan_member_info"),
		"Information of member in the wan cluster.",
		[]string{"member", "dc", "role", "version"}, nil,
	)
	memberWanStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "serf_wan_member_status"),
		"Status of member in the wan cluster. 1=Alive, 2=Leaving, 3=Left, 4=Failed.",
		[]string{"member", "dc"}, nil,
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
	serviceMeta = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "service_meta_info"),
		"Meta of a service.",
		[]string{"service_id", "node", "key", "value"}, nil,
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
		[]string{"service_id", "service_name", "check_id", "check_name", "node"}, nil,
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
	client           *consul_api.Client
	queryOptions     consul_api.QueryOptions
	kvPrefix         string
	kvFilter         *regexp.Regexp
	metaFilter       *regexp.Regexp
	healthSummary    bool
	agentOnly        bool
	logger           *slog.Logger
	requestLimitChan chan struct{}
}

// ConsulOpts configures options for connecting to Consul.
type ConsulOpts struct {
	URI          string
	CAFile       string
	CertFile     string
	KeyFile      string
	ServerName   string
	Timeout      time.Duration
	Insecure     bool
	RequestLimit int
	AgentOnly    bool
}

// New returns an initialized Exporter.
func New(opts ConsulOpts, queryOptions consul_api.QueryOptions, kvPrefix, kvFilter string, metaFilter string, healthSummary bool, logger *slog.Logger) (*Exporter, error) {
	uri := opts.URI
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
		Address:            opts.ServerName,
		CAFile:             opts.CAFile,
		CertFile:           opts.CertFile,
		KeyFile:            opts.KeyFile,
		InsecureSkipVerify: opts.Insecure,
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
	config.HttpClient.Timeout = opts.Timeout
	config.HttpClient.Transport = transport

	client, err := consul_api.NewClient(config)
	if err != nil {
		return nil, err
	}

	var requestLimitChan chan struct{}
	if opts.RequestLimit > 0 {
		requestLimitChan = make(chan struct{}, opts.RequestLimit)
	}

	// Init our exporter.
	return &Exporter{
		client:           client,
		queryOptions:     queryOptions,
		kvPrefix:         kvPrefix,
		kvFilter:         regexp.MustCompile(kvFilter),
		metaFilter:       regexp.MustCompile(metaFilter),
		healthSummary:    healthSummary,
		logger:           logger,
		requestLimitChan: requestLimitChan,
		agentOnly:        opts.AgentOnly,
	}, nil
}

// Describe describes all the metrics ever exported by the Consul exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- up
	ch <- clusterServers
	ch <- clusterLeader
	ch <- nodeCount
	ch <- memberInfo
	ch <- memberStatus
	ch <- memberWanInfo
	ch <- memberWanStatus
	ch <- serviceCount
	ch <- serviceNodesHealthy
	ch <- nodeChecks
	ch <- serviceChecks
	ch <- keyValues
	ch <- serviceTag
	ch <- serviceMeta
	ch <- serviceCheckNames
}

// Collect fetches the stats from configured Consul location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	ok := e.collectServicesMetric(ch)
	if !e.agentOnly {
		ok = e.collectPeersMetric(ch) && ok
		ok = e.collectLeaderMetric(ch) && ok
		ok = e.collectNodesMetric(ch) && ok
		ok = e.collectMembersInfoMetric(ch) && ok
		ok = e.collectMembersMetric(ch) && ok
		ok = e.collectMembersWanInfoMetric(ch) && ok
		ok = e.collectMembersWanMetric(ch) && ok
		ok = e.collectHealthStateMetric(ch) && ok
		ok = e.collectKeyValues(ch) && ok
	}

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
		e.logger.Error("Can't query consul", "err", err)
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
		e.logger.Error("Can't query consul", "err", err)
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
	nodes, _, err := e.client.Catalog().Nodes(&e.queryOptions)
	if err != nil {
		e.logger.Error("Failed to query catalog for nodes", "err", err)
		return false
	}
	ch <- prometheus.MustNewConstMetric(
		nodeCount, prometheus.GaugeValue, float64(len(nodes)),
	)
	return true
}

func (e *Exporter) collectMembersInfoMetric(ch chan<- prometheus.Metric) bool {
	members, err := e.client.Agent().Members(false)
	if err != nil {
		e.logger.Error("Failed to query member info", "err", err)
		return false
	}
	for _, entry := range members {
		version := strings.Split(entry.Tags["build"], ":")[0]
		ch <- prometheus.MustNewConstMetric(
			memberInfo, prometheus.GaugeValue, float64(entry.Status), entry.Name, entry.Tags["role"], version,
		)
	}
	return true
}

func (e *Exporter) collectMembersMetric(ch chan<- prometheus.Metric) bool {
	members, err := e.client.Agent().Members(false)
	if err != nil {
		e.logger.Error("Failed to query member status", "err", err)
		return false
	}
	for _, entry := range members {
		ch <- prometheus.MustNewConstMetric(
			memberStatus, prometheus.GaugeValue, float64(entry.Status), entry.Name,
		)
	}
	return true
}

func (e *Exporter) collectMembersWanInfoMetric(ch chan<- prometheus.Metric) bool {
	members, err := e.client.Agent().Members(true)
	if err != nil {
		e.logger.Error("Failed to query wan member info", "err", err)
		return false
	}
	for _, entry := range members {
		version := strings.Split(entry.Tags["build"], ":")[0]
		ch <- prometheus.MustNewConstMetric(
			memberWanInfo, prometheus.GaugeValue, float64(entry.Status), entry.Name, entry.Tags["dc"], entry.Tags["role"], version,
		)
	}
	return true
}

func (e *Exporter) collectMembersWanMetric(ch chan<- prometheus.Metric) bool {
	members, err := e.client.Agent().Members(true)
	if err != nil {
		e.logger.Error("Failed to query wan member status", "err", err)
		return false
	}
	for _, entry := range members {
		ch <- prometheus.MustNewConstMetric(
			memberWanStatus, prometheus.GaugeValue, float64(entry.Status), entry.Name, entry.Tags["dc"],
		)
	}
	return true
}

func (e *Exporter) collectServicesMetric(ch chan<- prometheus.Metric) bool {
	serviceNames := make(map[string][]string)
	if e.agentOnly {
		services, err := e.client.Agent().Services()
		if err != nil {
			e.logger.Error("Failed to query for agent services", "err", err)
			return false
		}
		for _, srv := range services {
			serviceNames[srv.Service] = srv.Tags
		}
	} else {
		services, _, err := e.client.Catalog().Services(&e.queryOptions)
		if err != nil {
			e.logger.Error("Failed to query for services", "err", err)
			return false
		}
		serviceNames = services
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
	checks, _, err := e.client.Health().State("any", &e.queryOptions)
	if err != nil {
		e.logger.Error("Failed to query service health", "err", err)
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
				serviceCheckNames, prometheus.GaugeValue, 1, hc.ServiceID, hc.ServiceName, hc.CheckID, hc.Name, hc.Node,
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
		go func(s string) {
			if e.requestLimitChan != nil {
				e.requestLimitChan <- struct{}{}
				defer func() {
					<-e.requestLimitChan
				}()
			}
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
		e.logger.Warn("Skipping service because it starts with a slash", "service_name", serviceName)
		return true
	}
	e.logger.Debug("Fetching health summary", "serviceName", serviceName)

	var serviceEntries []*consul_api.ServiceEntry

	if e.agentOnly {
		nodeName, err := e.client.Agent().NodeName()
		if err != nil {
			e.logger.Error("Failed to query agent node name", "err", err)
			return false
		}

		_, agentServices, err := e.client.Agent().AgentHealthServiceByName(serviceName)
		if err != nil {
			e.logger.Error("Failed to query agent service health", "err", err)
			return false
		}
		for _, agentService := range agentServices {
			serviceEntries = append(serviceEntries, &consul_api.ServiceEntry{Checks: agentService.Checks, Service: agentService.Service, Node: &consul_api.Node{Node: nodeName}})
		}
	} else {
		service, _, err := e.client.Health().Service(serviceName, "", false, &e.queryOptions)
		if err != nil {
			e.logger.Error("Failed to query service health", "err", err)
			return false
		}
		serviceEntries = service
	}

	for _, entry := range serviceEntries {
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
		if entry.Service.Service != serviceName {
			e.logger.Debug("Skipping service instance because its registered to %s but belongs to %s service registration", entry.Service.Service, serviceName)
			break
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

		for key, val := range entry.Service.Meta {
			if e.metaFilter.MatchString(key) {
				e.logger.Debug("outputting meta", "key", key)
				ch <- prometheus.MustNewConstMetric(serviceMeta, prometheus.GaugeValue, 1, entry.Service.ID, entry.Node.Node, key, val)
			}
		}
	}
	return true
}

func (e *Exporter) collectKeyValues(ch chan<- prometheus.Metric) bool {
	if e.kvPrefix == "" {
		return true
	}

	kv := e.client.KV()
	pairs, _, err := kv.List(e.kvPrefix, &e.queryOptions)
	if err != nil {
		e.logger.Error("Error fetching key/values", "err", err)
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
