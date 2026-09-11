/*
Copyright 2026.

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
	"fmt"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cephmonsv1alpha1 "cephmons.github.io/rook-mon-dns/api/v1alpha1"
	externaldnsv1alpha1 "cephmons.github.io/rook-mon-dns/internal/externaldns/v1alpha1"
)

// BuildDNSEndpoint computes the desired DNSEndpoint object for the given
// CephMonDNSEndpoint and the parsed mon endpoints. mons must be sorted by mon
// ID (ParseMonEndpoints already guarantees this) so that the generated record
// names stay stable. The result is deterministic.
func BuildDNSEndpoint(cr *cephmonsv1alpha1.CephMonDNSEndpoint, mons []MonEndpoint) *externaldnsv1alpha1.DNSEndpoint {
	spec := cr.Spec
	ttl := externaldnsv1alpha1.TTL(spec.TTL)

	endpoints := make([]*externaldnsv1alpha1.Endpoint, 0, len(mons)+2)

	// One A record per monitor: mon1.<zone>, mon2.<zone>, ...
	monDNSNames := make([]string, 0, len(mons))
	for i, mon := range mons {
		dnsName := fqdn(renderRecordTemplate(spec.RecordTemplate, i+1, mon.ID), spec.Zone)
		monDNSNames = append(monDNSNames, dnsName)
		endpoints = append(endpoints, &externaldnsv1alpha1.Endpoint{
			DNSName:    dnsName,
			RecordType: externaldnsv1alpha1.RecordTypeA,
			RecordTTL:  ttl,
			Targets:    externaldnsv1alpha1.Targets{mon.Address},
		})
	}

	// Aggregate A record with all monitor node IPs.
	if spec.AllRecordName != "" {
		endpoints = append(endpoints, &externaldnsv1alpha1.Endpoint{
			DNSName:    fqdn(spec.AllRecordName, spec.Zone),
			RecordType: externaldnsv1alpha1.RecordTypeA,
			RecordTTL:  ttl,
			Targets:    dedupeAddresses(mons),
		})
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

	return &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: targetNamespace(cr),
			Labels: map[string]string{
				"app.kubernetes.io/name":       "rook-mon-dns",
				"app.kubernetes.io/managed-by": "rook-mon-dns",
			},
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{Endpoints: endpoints},
	}
}

// targetNamespace returns the namespace the DNSEndpoint is created in.
func targetNamespace(cr *cephmonsv1alpha1.CephMonDNSEndpoint) string {
	if cr.Spec.TargetNamespace != "" {
		return cr.Spec.TargetNamespace
	}
	return cr.Namespace
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

// dedupeAddresses returns the mon addresses without duplicates, preserving
// the order of the sorted mon list.
func dedupeAddresses(mons []MonEndpoint) externaldnsv1alpha1.Targets {
	seen := make(map[string]struct{}, len(mons))
	targets := make(externaldnsv1alpha1.Targets, 0, len(mons))
	for _, mon := range mons {
		if _, ok := seen[mon.Address]; ok {
			continue
		}
		seen[mon.Address] = struct{}{}
		targets = append(targets, mon.Address)
	}
	return targets
}
