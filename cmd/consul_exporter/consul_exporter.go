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
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/alecthomas/kingpin/v2"
	consul_api "github.com/hashicorp/consul/api"
	"github.com/prometheus/client_golang/prometheus"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/consul_exporter/pkg/exporter"
	"github.com/prometheus/exporter-toolkit/web"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

type promHTTPLogger struct {
	logger *slog.Logger
}

func (l promHTTPLogger) Println(v ...interface{}) {
	l.logger.Error(fmt.Sprint(v...))
}

func init() {
	prometheus.MustRegister(versioncollector.NewCollector("consul_exporter"))
}

func main() {
	var (
		webConfig     = webflag.AddFlags(kingpin.CommandLine, ":9107")
		metricsPath   = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").String()
		healthSummary = kingpin.Flag("consul.health-summary", "Generate a health summary for each service instance. Needs n+1 queries to collect all information.").Default("true").Bool()
		kvPrefix      = kingpin.Flag("kv.prefix", "Prefix from which to expose key/value pairs.").Default("").String()
		kvFilter      = kingpin.Flag("kv.filter", "Regex that determines which keys to expose.").Default(".*").String()
		metaFilter    = kingpin.Flag("meta.filter", "Regex that determines which meta keys to expose.").Default("^$").String()

		opts         = exporter.ConsulOpts{}
		queryOptions = consul_api.QueryOptions{}
	)
	kingpin.Flag("consul.server", "HTTP API address of a Consul server or agent. (prefix with https:// to connect over HTTPS)").Default("http://localhost:8500").StringVar(&opts.URI)
	kingpin.Flag("consul.ca-file", "File path to a PEM-encoded certificate authority used to validate the authenticity of a server certificate.").Default("").StringVar(&opts.CAFile)
	kingpin.Flag("consul.cert-file", "File path to a PEM-encoded certificate used with the private key to verify the exporter's authenticity.").Default("").StringVar(&opts.CertFile)
	kingpin.Flag("consul.key-file", "File path to a PEM-encoded private key used with the certificate to verify the exporter's authenticity.").Default("").StringVar(&opts.KeyFile)
	kingpin.Flag("consul.server-name", "When provided, this overrides the hostname for the TLS certificate. It can be used to ensure that the certificate name matches the hostname we declare.").Default("").StringVar(&opts.ServerName)
	kingpin.Flag("consul.timeout", "Timeout on HTTP requests to the Consul API.").Default("500ms").DurationVar(&opts.Timeout)
	kingpin.Flag("consul.insecure", "Disable TLS host verification.").Default("false").BoolVar(&opts.Insecure)
	kingpin.Flag("consul.request-limit", "Limit the maximum number of concurrent requests to consul, 0 means no limit.").Default("0").IntVar(&opts.RequestLimit)
	kingpin.Flag("consul.agent-only", "Only export metrics about services registered on local agent").Default("false").BoolVar(&opts.AgentOnly)

	// Query options.
	kingpin.Flag("consul.allow_stale", "Allows any Consul server (non-leader) to service a read.").Default("true").BoolVar(&queryOptions.AllowStale)
	kingpin.Flag("consul.require_consistent", "Forces the read to be fully consistent.").Default("false").BoolVar(&queryOptions.RequireConsistent)

	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.Version(version.Print("consul_exporter"))
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promslog.New(promslogConfig)

	logger.Info("Starting consul_exporter", "version", version.Info())
	logger.Info(version.BuildContext())

	exporter, err := exporter.New(opts, queryOptions, *kvPrefix, *kvFilter, *metaFilter, *healthSummary, logger)
	if err != nil {
		logger.Error("Error creating the exporter", "err", err)
		os.Exit(1)
	}
	prometheus.MustRegister(exporter)

	queryOptionsJson, err := json.MarshalIndent(queryOptions, "", "    ")
	if err != nil {
		logger.Error("Error marshaling query options", "err", err)
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

	srv := &http.Server{}
	if err := web.ListenAndServe(srv, webConfig, logger); err != nil {
		logger.Error("Error starting HTTP server", "err", err)
		os.Exit(1)
	}
}
