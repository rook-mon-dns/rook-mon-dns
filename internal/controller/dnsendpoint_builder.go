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
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cephmonsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/api/v1alpha1"
	externaldnsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/internal/externaldns/v1alpha1"
)

// ErrRecordNameConflict is returned when two records of the same type would
// get the same DNS name (e.g. allRecordName "mon1" with template "mon{n}").
var ErrRecordNameConflict = errors.New("record name conflict")

// BuildDNSEndpoint computes the desired DNSEndpoint object for the given
// CephMonDNSEndpoint and the parsed mon endpoints. mons must be sorted by mon
// ID (ParseMonEndpoints already guarantees this). The result is deterministic.
func BuildDNSEndpoint(cr *cephmonsv1alpha1.CephMonDNSEndpoint, mons []MonEndpoint) (*externaldnsv1alpha1.DNSEndpoint, error) {
	spec := cr.Spec
	ttl := externaldnsv1alpha1.TTL(spec.TTL)

	endpoints := make([]*externaldnsv1alpha1.Endpoint, 0, len(mons)+3)

	// One A/AAAA record per monitor: mon1.<zone>, mon2.<zone>, ...
	monDNSNames := make([]string, 0, len(mons))
	for i, mon := range mons {
		dnsName := fqdn(renderRecordTemplate(spec.RecordTemplate, i+1, mon.ID), spec.Zone)
		monDNSNames = append(monDNSNames, dnsName)
		endpoints = append(endpoints, &externaldnsv1alpha1.Endpoint{
			DNSName:    dnsName,
			RecordType: recordType(mon.Address),
			RecordTTL:  ttl,
			Targets:    externaldnsv1alpha1.Targets{mon.Address},
		})
	}

	// Aggregate record with all monitor node IPs, one per address family.
	if spec.AllRecordName != "" {
		for _, rt := range []string{externaldnsv1alpha1.RecordTypeA, externaldnsv1alpha1.RecordTypeAAAA} {
			if targets := dedupeAddresses(mons, rt); len(targets) > 0 {
				endpoints = append(endpoints, &externaldnsv1alpha1.Endpoint{
					DNSName:    fqdn(spec.AllRecordName, spec.Zone),
					RecordType: rt,
					RecordTTL:  ttl,
					Targets:    targets,
				})
			}
		}
	}

	// Optional SRV record for Ceph mon discovery ("mon dns srv name").
	if spec.SRV.Enabled {
		srvTargets := make(externaldnsv1alpha1.Targets, 0, len(mons))
		for i, mon := range mons {
			// SRV target format: "priority weight port hostname".
			srvTargets = append(srvTargets, fmt.Sprintf("0 10 %d %s", mon.Port, monDNSNames[i]))
		}
		endpoints = append(endpoints, &externaldnsv1alpha1.Endpoint{
			DNSName:    fqdn(spec.SRV.Name, spec.Zone),
			RecordType: externaldnsv1alpha1.RecordTypeSRV,
			RecordTTL:  ttl,
			Targets:    srvTargets,
		})
	}

	seen := make(map[string]bool, len(endpoints))
	for _, ep := range endpoints {
		key := ep.RecordType + " " + ep.DNSName
		if seen[key] {
			return nil, fmt.Errorf("%w: %s record %s is generated twice", ErrRecordNameConflict, ep.RecordType, ep.DNSName)
		}
		seen[key] = true
	}

	return &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: cr.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "rook-mon-dns",
				"app.kubernetes.io/managed-by": "rook-mon-dns",
			},
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{Endpoints: endpoints},
	}, nil
}

// recordType returns AAAA for IPv6 addresses and A otherwise.
func recordType(address string) string {
	if addr, err := netip.ParseAddr(address); err == nil && addr.Is6() {
		return externaldnsv1alpha1.RecordTypeAAAA
	}
	return externaldnsv1alpha1.RecordTypeA
}

// renderRecordTemplate expands the {n} (sequence number) and {id} (mon ID)
// placeholders of the record template.
func renderRecordTemplate(template string, n int, id string) string {
	return strings.NewReplacer("{n}", strconv.Itoa(n), "{id}", id).Replace(template)
}

// fqdn joins a relative record name with the zone.
func fqdn(name, zone string) string {
	return strings.TrimSuffix(name, ".") + "." + zone
}

// dedupeAddresses returns the mon addresses of the given record type without
// duplicates, preserving the order of the sorted mon list.
func dedupeAddresses(mons []MonEndpoint, rt string) externaldnsv1alpha1.Targets {
	seen := make(map[string]struct{}, len(mons))
	targets := make(externaldnsv1alpha1.Targets, 0, len(mons))
	for _, mon := range mons {
		if _, ok := seen[mon.Address]; ok || recordType(mon.Address) != rt {
			continue
		}
		seen[mon.Address] = struct{}{}
		targets = append(targets, mon.Address)
	}
	return targets
}
