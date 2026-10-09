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
	"testing"

	consul_api "github.com/hashicorp/consul/api"
)

func TestApplyQueryConsistency(t *testing.T) {
	cases := []struct {
		name              string
		requireConsistent bool
		allowStale        bool
		wantAllowStale    bool
	}{
		{
			name:              "consistent disables stale",
			requireConsistent: true,
			allowStale:        true,
			wantAllowStale:    false,
		},
		{
			name:              "consistent with stale already false",
			requireConsistent: true,
			allowStale:        false,
			wantAllowStale:    false,
		},
		{
			name:              "inconsistent leaves stale enabled",
			requireConsistent: false,
			allowStale:        true,
			wantAllowStale:    true,
		},
		{
			name:              "inconsistent leaves stale disabled",
			requireConsistent: false,
			allowStale:        false,
			wantAllowStale:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &consul_api.QueryOptions{
				RequireConsistent: tc.requireConsistent,
				AllowStale:        tc.allowStale,
			}
			applyQueryConsistency(q)
			if q.AllowStale != tc.wantAllowStale {
				t.Errorf("AllowStale = %v, want %v", q.AllowStale, tc.wantAllowStale)
			}
			if q.RequireConsistent != tc.requireConsistent {
				t.Errorf("RequireConsistent = %v, want %v", q.RequireConsistent, tc.requireConsistent)
			}
		})
	}
}
