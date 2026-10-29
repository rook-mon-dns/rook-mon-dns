/*
Copyright 2026 The rook-mon-dns Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// realMapping is the mapping of a real rook-ceph-mon-endpoints ConfigMap.
const realMapping = `{"node":{` +
	`"g":{"Name":"talos-node-01","Hostname":"talos-node-01","Address":"192.168.1.103"},` +
	`"j":{"Name":"talos-node-03","Hostname":"talos-node-03","Address":"192.168.1.107"},` +
	`"l":{"Name":"talos-node-02","Hostname":"talos-node-02","Address":"192.168.1.105"}}}`

// realData is the data field for realMapping with hostNetwork mons: the mon
// IPs are the node addresses.
const realData = "g=192.168.1.103:6789,l=192.168.1.105:6789,j=192.168.1.107:6789"

// podNetworkData is the data field of the same cluster without hostNetwork:
// the mons listen on ClusterIPs, not on the node addresses.
const podNetworkData = "g=10.109.56.143:6789,l=10.97.138.166:6789,j=10.97.18.232:6789"

func configMap(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph-mon-endpoints", Namespace: "rook-ceph"},
		Data:       data,
	}
}

func TestParseMonEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		data    map[string]string
		want    []MonEndpoint
		wantErr error // nil = expect success; errors.Is comparison
	}{
		{
			name: "real rook ConfigMap is sorted by mon ID",
			data: map[string]string{
				ConfigMapMappingKey: realMapping,
				ConfigMapDataKey:    realData,
			},
			want: []MonEndpoint{
				{ID: "g", Address: "192.168.1.103", Port: 6789},
				{ID: "j", Address: "192.168.1.107", Port: 6789},
				{ID: "l", Address: "192.168.1.105", Port: 6789},
			},
		},
		{
			name: "mons on the pod network are rejected",
			data: map[string]string{
				ConfigMapMappingKey: realMapping,
				ConfigMapDataKey:    podNetworkData,
			},
			wantErr: ErrHostNetworkRequired,
		},
		{
			name:    "empty mapping string",
			data:    map[string]string{ConfigMapMappingKey: "", ConfigMapDataKey: realData},
			wantErr: ErrMappingEmpty,
		},
		{
			name:    "missing mapping key",
			data:    map[string]string{ConfigMapDataKey: realData},
			wantErr: ErrMappingEmpty,
		},
		{
			name:    "mapping with empty node object",
			data:    map[string]string{ConfigMapMappingKey: `{"node":{}}`},
			wantErr: ErrMappingEmpty,
		},
		{
			name:    "invalid JSON is not ErrMappingEmpty",
			data:    map[string]string{ConfigMapMappingKey: `{"node":{invalid`},
			wantErr: nil, // any error, checked separately below
		},
		{
			name: "missing data key falls back to default port",
			data: map[string]string{ConfigMapMappingKey: realMapping},
			want: []MonEndpoint{
				{ID: "g", Address: "192.168.1.103", Port: DefaultMonPort},
				{ID: "j", Address: "192.168.1.107", Port: DefaultMonPort},
				{ID: "l", Address: "192.168.1.105", Port: DefaultMonPort},
			},
		},
		{
			name: "custom port is parsed from data",
			data: map[string]string{
				ConfigMapMappingKey: `{"node":{"a":{"Name":"n1","Hostname":"n1","Address":"192.168.1.10"}}}`,
				ConfigMapDataKey:    "a=192.168.1.10:3300",
			},
			want: []MonEndpoint{
				{ID: "a", Address: "192.168.1.10", Port: 3300},
			},
		},
		{
			name: "IPv4-mapped IPv6 addresses are unmapped",
			data: map[string]string{
				ConfigMapMappingKey: `{"node":{"a":{"Name":"n1","Hostname":"n1","Address":"::ffff:192.168.1.10"}}}`,
				ConfigMapDataKey:    "a=192.168.1.10:6789",
			},
			want: []MonEndpoint{
				{ID: "a", Address: "192.168.1.10", Port: 6789},
			},
		},
		{
			name: "unsorted IDs come back sorted",
			data: map[string]string{
				ConfigMapMappingKey: `{"node":{` +
					`"c":{"Name":"n3","Hostname":"n3","Address":"192.168.1.12"},` +
					`"a":{"Name":"n1","Hostname":"n1","Address":"192.168.1.10"},` +
					`"b":{"Name":"n2","Hostname":"n2","Address":"192.168.1.11"}}}`,
			},
			want: []MonEndpoint{
				{ID: "a", Address: "192.168.1.10", Port: DefaultMonPort},
				{ID: "b", Address: "192.168.1.11", Port: DefaultMonPort},
				{ID: "c", Address: "192.168.1.12", Port: DefaultMonPort},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMonEndpoints(configMap(tt.data))

			if tt.name == "invalid JSON is not ErrMappingEmpty" {
				if err == nil || errors.Is(err, ErrMappingEmpty) {
					t.Fatalf("expected JSON parse error (not ErrMappingEmpty), got: %v", err)
				}
				return
			}

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d endpoints, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("endpoint %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseMonEndpointsInvalidAddress(t *testing.T) {
	cm := configMap(map[string]string{
		ConfigMapMappingKey: `{"node":{"a":{"Name":"n1","Hostname":"n1","Address":"not-an-ip"}}}`,
	})
	if _, err := ParseMonEndpoints(cm); err == nil {
		t.Fatal("expected error for invalid address, got nil")
	}
}
