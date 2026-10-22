// Copyright 2026 The Prometheus Authors
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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	consul_api "github.com/hashicorp/consul/api"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/promslog"
)

const tlsTestServerName = "consul.test"

type testCert struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

func newTestCert(t *testing.T, tmpl *x509.Certificate, parent *testCert) *testCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber, err = rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(time.Hour)

	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return &testCert{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

func writeTestFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()

	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCollectTLS runs the exporter against a TLS server that requires client
// certificates and checks the effect of each of the TLS related options.
func TestCollectTLS(t *testing.T) {
	ca := newTestCert(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: "test CA"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, nil)
	// The server certificate is only valid for tlsTestServerName and not for
	// the IP address the test server listens on.
	serverCert := newTestCert(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: tlsTestServerName},
		DNSNames:    []string{tlsTestServerName},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca)
	clientCert := newTestCert(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "exporter"},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, ca)

	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	tlsCert, err := tls.X509KeyPair(serverCert.certPEM, serverCert.keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	caFile := writeTestFile(t, dir, "ca.pem", ca.certPEM)
	certFile := writeTestFile(t, dir, "client.pem", clientCert.certPEM)
	keyFile := writeTestFile(t, dir, "client-key.pem", clientCert.keyPEM)

	cases := []struct {
		name string
		opts ConsulOpts
		up   float64
	}{
		{
			name: "ca, client certificate and server name",
			opts: ConsulOpts{CAFile: caFile, CertFile: certFile, KeyFile: keyFile, ServerName: tlsTestServerName},
			up:   1,
		},
		{
			name: "server name mismatch",
			opts: ConsulOpts{CAFile: caFile, CertFile: certFile, KeyFile: keyFile},
			up:   0,
		},
		{
			name: "server name mismatch ignored with insecure",
			opts: ConsulOpts{CAFile: caFile, CertFile: certFile, KeyFile: keyFile, Insecure: true},
			up:   1,
		},
		{
			name: "unknown CA",
			opts: ConsulOpts{CertFile: certFile, KeyFile: keyFile, ServerName: tlsTestServerName},
			up:   0,
		},
		{
			name: "unknown CA ignored with insecure",
			opts: ConsulOpts{CertFile: certFile, KeyFile: keyFile, Insecure: true},
			up:   1,
		},
		{
			name: "missing client certificate",
			opts: ConsulOpts{CAFile: caFile, ServerName: tlsTestServerName},
			up:   0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.opts.URI = srv.URL
			c.opts.Timeout = 5 * time.Second
			c.opts.AgentOnly = true

			e, err := New(c.opts, consul_api.QueryOptions{}, "", ".*", "", true, promslog.NewNopLogger())
			if err != nil {
				t.Fatal(err)
			}

			ch := make(chan prometheus.Metric, 100)
			e.Collect(ch)
			close(ch)

			got, found := 0.0, false
			for m := range ch {
				if m.Desc() != up {
					continue
				}
				var pb dto.Metric
				if err := m.Write(&pb); err != nil {
					t.Fatal(err)
				}
				got, found = pb.GetGauge().GetValue(), true
			}
			if !found {
				t.Fatal("consul_up metric not collected")
			}
			if got != c.up {
				t.Errorf("expected consul_up %v, got %v", c.up, got)
			}
		})
	}
}
