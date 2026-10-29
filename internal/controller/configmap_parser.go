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
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// ConfigMapMappingKey is the ConfigMap key holding the mon-to-node mapping
	// as JSON, e.g. {"node":{"a":{"Name":"node-1","Hostname":"node-1","Address":"192.168.1.10"}}}.
	ConfigMapMappingKey = "mapping"

	// ConfigMapDataKey is the ConfigMap key holding the mon endpoints as
	// "id=ip:port" pairs, e.g. "a=10.0.0.1:6789,b=10.0.0.2:6789". With
	// hostNetwork the IPs equal the node addresses from the mapping; otherwise
	// they are ClusterIPs that are not reachable from outside the cluster.
	ConfigMapDataKey = "data"

	// DefaultMonPort is the default Ceph mon port (msgr1/v1; msgr2 uses 3300),
	// used when the data field does not carry a parseable port for a mon.
	DefaultMonPort = 6789
)

// ErrMappingEmpty is returned when the ConfigMap has no node mapping, e.g.
// because the mons are not pinned to nodes. The operator deliberately never
// falls back to the IPs in the data field.
var ErrMappingEmpty = errors.New("configmap contains no node mapping")

// ErrHostNetworkRequired is returned when the mon IPs in the data field differ
// from the node addresses in the mapping, i.e. the mons run on the pod network
// and are not reachable on the node IPs.
// Limitation: also triggers for hostNetwork mons bound to a non-default node IP
// (multi-NIC, network.addressRanges); publish the data IPs if that is needed.
var ErrHostNetworkRequired = errors.New("mons are not on the host network (hostNetwork cluster required)")

// MonEndpoint is a single Ceph monitor as parsed from the Rook
// rook-ceph-mon-endpoints ConfigMap.
type MonEndpoint struct {
	// ID is the Rook mon ID (e.g. "a", "g"). Not stable across failovers.
	ID string
	// Address is the node IP the mon is reachable on.
	Address string
	// Port is the mon port (from the data field, default 6789).
	Port int
}

// monMappingJSON mirrors the structure of the ConfigMap's mapping key.
type monMappingJSON struct {
	Node map[string]monMappingNode `json:"node"`
}

type monMappingNode struct {
	Address string `json:"Address"`
}

// ParseMonEndpoints extracts the mon node endpoints from the Rook
// rook-ceph-mon-endpoints ConfigMap. The returned list is sorted by mon ID so
// that record names derived from the position in the list do not change as
// long as the set of mons stays the same.
func ParseMonEndpoints(cm *corev1.ConfigMap) ([]MonEndpoint, error) {
	raw, ok := cm.Data[ConfigMapMappingKey]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, ErrMappingEmpty
	}

	var mapping monMappingJSON
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return nil, fmt.Errorf("failed to parse %q key of ConfigMap %s/%s: %w",
			ConfigMapMappingKey, cm.Namespace, cm.Name, err)
	}
	if len(mapping.Node) == 0 {
		return nil, ErrMappingEmpty
	}

	monData := parseMonData(cm.Data[ConfigMapDataKey])

	endpoints := make([]MonEndpoint, 0, len(mapping.Node))
	for id, node := range mapping.Node {
		addr, err := netip.ParseAddr(strings.TrimSpace(node.Address))
		if err != nil {
			return nil, fmt.Errorf("mon %q has invalid address %q: %w", id, node.Address, err)
		}
		addr = addr.Unmap()

		port := DefaultMonPort
		if data, ok := monData[id]; ok {
			if data.Addr().Unmap() != addr {
				return nil, fmt.Errorf("%w: mon %q listens on %s, but its node address is %s",
					ErrHostNetworkRequired, id, data.Addr(), addr)
			}
			port = int(data.Port())
		}

		endpoints = append(endpoints, MonEndpoint{ID: id, Address: addr.String(), Port: port})
	}

	slices.SortFunc(endpoints, func(a, b MonEndpoint) int { return strings.Compare(a.ID, b.ID) })
	return endpoints, nil
}

// parseMonData parses the data field ("a=10.0.0.1:6789,b=10.0.0.2:3300") into
// a mon-ID-to-address map. Entries that cannot be parsed are skipped.
func parseMonData(data string) map[string]netip.AddrPort {
	endpoints := make(map[string]netip.AddrPort)
	for entry := range strings.SplitSeq(data, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, hostPort, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		addrPort, err := netip.ParseAddrPort(strings.TrimSpace(hostPort))
		if err != nil {
			continue
		}
		endpoints[strings.TrimSpace(id)] = addrPort
	}
	return endpoints
}
