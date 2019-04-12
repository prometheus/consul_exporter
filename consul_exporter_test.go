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

import "testing"

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
