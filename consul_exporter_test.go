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
	"fmt"
	"io"
	"os"
	"testing"
	"time"

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
		_, err := NewExporter(consulOpts{uri: test.uri}, "", ".*", true)
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

	exporter, err := NewExporter(consulOpts{uri: addr, timeout: time.Duration(time.Second)}, "", "", true)
	if err != nil {
		t.Errorf("expected no error but got %q", err)
	}

	metrics := `# HELP consul_catalog_service_node_healthy Is this service healthy on this node?
# TYPE consul_catalog_service_node_healthy gauge
consul_catalog_service_node_healthy{node="%s",service_id="consul",service_name="consul"} 1
# HELP consul_catalog_services How many services are in the cluster.
# TYPE consul_catalog_services gauge
consul_catalog_services 1
# HELP consul_health_node_status Status of health checks associated with a node.
# TYPE consul_health_node_status gauge
consul_health_node_status{check="serfHealth",node="%s",status="critical"} 0
consul_health_node_status{check="serfHealth",node="%s",status="maintenance"} 0
consul_health_node_status{check="serfHealth",node="%s",status="passing"} 1
consul_health_node_status{check="serfHealth",node="%s",status="warning"} 0
# HELP consul_raft_leader Does Raft cluster have a leader (according to this node).
# TYPE consul_raft_leader gauge
consul_raft_leader 1
# HELP consul_raft_peers How many peers (servers) are in the Raft cluster.
# TYPE consul_raft_peers gauge
consul_raft_peers 1
# HELP consul_serf_lan_members How many members are in the cluster.
# TYPE consul_serf_lan_members gauge
consul_serf_lan_members 1
# HELP consul_up Was the last query of Consul successful.
# TYPE consul_up gauge
consul_up 1
`
	expected := bytes.NewReader([]byte(fmt.Sprintf(metrics, node, node, node, node, node)))

	// Only check metrics that are explicitly listed above.
	var (
		tp          expfmt.TextParser
		metricNames []string
	)
	mfs, err := tp.TextToMetricFamilies(expected)
	if err != nil {
		t.Errorf("expected no error but got %q", err)
	}
	for _, mf := range mfs {
		metricNames = append(metricNames, mf.GetName())
	}

	expected.Seek(0, io.SeekStart)
	err = testutil.CollectAndCompare(exporter, expected, metricNames...)
	if err != nil {
		t.Errorf("expected no error but got %s", err)
	}
}
