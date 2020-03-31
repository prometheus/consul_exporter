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
	"bytes"
	"io"
	"os"
	"testing"
	"text/template"
	"time"

	"github.com/go-kit/kit/log"
	consul_api "github.com/hashicorp/consul/api"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"
)

func TestNewExporter(t *testing.T) {
	cases := []struct {
		uri string
		ok  bool
	}{
		{uri: "", ok: false},
		{uri: "localhost:8500", ok: true},
		{uri: "https://localhost:8500", ok: true},
		{uri: "http://some.where:8500", ok: true},
		{uri: "fuuuu://localhost:8500", ok: false},
	}

	for _, test := range cases {
		_, err := NewExporter(consulOpts{uri: test.uri}, "", ".*", true, log.NewNopLogger())
		if test.ok && err != nil {
			t.Errorf("expected no error w/ %q, but got %q", test.uri, err)
		}
		if !test.ok && err == nil {
			t.Errorf("expected error w/ %q, but got %q", test.uri, err)
		}
	}
}

func TestCollect(t *testing.T) {
	addr := os.Getenv("CONSUL_SERVER")
	if len(addr) == 0 {
		t.Skipf("CONSUL_SERVER environment variable not set")
	}
	node := os.Getenv("CONSUL_NODE_NAME")
	if len(node) == 0 {
		t.Skipf("CONSUL_NODE_NAME environment variable not set")
	}

	for _, tc := range []struct {
		name     string
		metrics  string
		services []*consul_api.AgentServiceRegistration
	}{
		{
			name: "simple collect",
			metrics: `# HELP consul_catalog_service_node_healthy Is this service healthy on this node?
# TYPE consul_catalog_service_node_healthy gauge
consul_catalog_service_node_healthy{node="{{ .Node }}",service_id="consul",service_name="consul"} 1
# HELP consul_catalog_services How many services are in the cluster.
# TYPE consul_catalog_services gauge
consul_catalog_services 1
# HELP consul_health_node_status Status of health checks associated with a node.
# TYPE consul_health_node_status gauge
consul_health_node_status{check="serfHealth",node="{{ .Node }}",status="critical"} 0
consul_health_node_status{check="serfHealth",node="{{ .Node }}",status="maintenance"} 0
consul_health_node_status{check="serfHealth",node="{{ .Node }}",status="passing"} 1
consul_health_node_status{check="serfHealth",node="{{ .Node }}",status="warning"} 0
# HELP consul_raft_leader Does Raft cluster have a leader (according to this node).
# TYPE consul_raft_leader gauge
consul_raft_leader 1
# HELP consul_raft_peers How many peers (servers) are in the Raft cluster.
# TYPE consul_raft_peers gauge
consul_raft_peers 1
# HELP consul_serf_lan_member_status Status of member in the cluster. 1=Alive, 2=Leaving, 3=Left, 4=Failed.
# TYPE consul_serf_lan_member_status gauge
consul_serf_lan_member_status{member="{{ .Node }}"} 1
# HELP consul_serf_lan_members How many members are in the cluster.
# TYPE consul_serf_lan_members gauge
consul_serf_lan_members 1
# HELP consul_up Was the last query of Consul successful.
# TYPE consul_up gauge
consul_up 1
`,
		},
		{
			name: "collect with duplicate tag values",
			metrics: `# HELP consul_catalog_service_node_healthy Is this service healthy on this node?
# TYPE consul_catalog_service_node_healthy gauge
consul_catalog_service_node_healthy{node="{{ .Node }}",service_id="consul",service_name="consul"} 1
consul_catalog_service_node_healthy{node="{{ .Node }}",service_id="foo",service_name="foo"} 1
# HELP consul_catalog_services How many services are in the cluster.
# TYPE consul_catalog_services gauge
consul_catalog_services 2
# HELP consul_service_tag Tags of a service.
# TYPE consul_service_tag gauge
consul_service_tag{node="{{ .Node }}",service_id="foo",tag="tag1"} 1
consul_service_tag{node="{{ .Node }}",service_id="foo",tag="tag2"} 1
`,
			services: []*consul_api.AgentServiceRegistration{
				&consul_api.AgentServiceRegistration{
					ID:   "foo",
					Name: "foo",
					Tags: []string{"tag1", "tag2", "tag1"},
				},
			},
		},
		{
			name: "collect with forward slash service name",
			metrics: `# HELP consul_catalog_service_node_healthy Is this service healthy on this node?
# TYPE consul_catalog_service_node_healthy gauge
consul_catalog_service_node_healthy{node="{{ .Node }}",service_id="bar",service_name="bar"} 1
consul_catalog_service_node_healthy{node="{{ .Node }}",service_id="consul",service_name="consul"} 1
# HELP consul_catalog_services How many services are in the cluster.
# TYPE consul_catalog_services gauge
consul_catalog_services 3
`,
			services: []*consul_api.AgentServiceRegistration{
				&consul_api.AgentServiceRegistration{
					ID:   "slashbar",
					Name: "/bar",
				},
				&consul_api.AgentServiceRegistration{
					ID:   "bar",
					Name: "bar",
				},
			},
		},
		{
			name: "collect with service check name",
			metrics: `# HELP consul_service_checks Link the service id and check name if available.
# TYPE consul_service_checks gauge
consul_service_checks{check_id="_nomad-check-special",check_name="friendly-name",service_id="special",service_name="special"} 1
`,
			services: []*consul_api.AgentServiceRegistration{
				&consul_api.AgentServiceRegistration{
					ID:   "special",
					Name: "special",
					Checks: []*consul_api.AgentServiceCheck{
						&consul_api.AgentServiceCheck{
							CheckID:  "_nomad-check-special",
							Name:     "friendly-name",
							TCP:      "localhost:8080",
							Timeout:  "30s",
							Interval: "10s",
						},
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter, err := NewExporter(consulOpts{uri: addr, timeout: time.Duration(time.Second)}, "", "", true, log.NewNopLogger())
			if err != nil {
				t.Errorf("expected no error but got %q", err)
			}

			defer func() {
				for _, svc := range tc.services {
					err := exporter.client.Agent().ServiceDeregister(svc.ID)
					if err != nil {
						t.Logf("deregistering service %q: %v", svc.Name, err)
					}
				}
			}()
			for _, svc := range tc.services {
				err := exporter.client.Agent().ServiceRegister(svc)
				if err != nil {
					t.Errorf("expected no error but got %s", err)
				}
			}

			tmpl, err := template.New(tc.name).Parse(tc.metrics)
			if err != nil {
				t.Errorf("expected no error but got %s", err)
			}

			var w bytes.Buffer
			err = tmpl.Execute(&w, map[string]string{"Node": node})
			if err != nil {
				t.Errorf("expected no error but got %s", err)
			}

			// Only check metrics that are explicitly listed above.
			var (
				tp          expfmt.TextParser
				metricNames []string
				buf         = bytes.NewReader(w.Bytes())
			)
			mfs, err := tp.TextToMetricFamilies(buf)
			if err != nil {
				t.Errorf("expected no error but got %q", err)
			}
			for _, mf := range mfs {
				metricNames = append(metricNames, mf.GetName())
			}

			buf.Seek(0, io.SeekStart)
			err = testutil.CollectAndCompare(exporter, buf, metricNames...)
			if err != nil {
				t.Errorf("expected no error but got %s", err)
			}
		})
	}
}
