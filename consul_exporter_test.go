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
