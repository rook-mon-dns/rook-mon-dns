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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cephmonsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/api/v1alpha1"
	externaldnsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/internal/externaldns/v1alpha1"
)

// testMons mirrors the real Rook example: mons sorted by ID (g < j < l).
var testMons = []MonEndpoint{
	{ID: "g", NodeName: "talos-node-01", Address: "192.168.1.103", Port: 6789},
	{ID: "j", NodeName: "talos-node-03", Address: "192.168.1.107", Port: 6789},
	{ID: "l", NodeName: "talos-node-02", Address: "192.168.1.105", Port: 6789},
}

func newCR(mutators ...func(*cephmonsv1alpha1.CephMonDNSEndpoint)) *cephmonsv1alpha1.CephMonDNSEndpoint {
	cr := &cephmonsv1alpha1.CephMonDNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph", Namespace: "rook-ceph"},
		Spec: cephmonsv1alpha1.CephMonDNSEndpointSpec{
			Zone:           "ceph.example.com",
			RecordTemplate: "mon{n}",
			AllRecordName:  "mon-all",
			TTL:            300,
			SRV:            cephmonsv1alpha1.SRVSpec{Enabled: false, Name: "_ceph-mon._tcp"},
		},
	}
	for _, mutate := range mutators {
		mutate(cr)
	}
	return cr
}

// endpointsByName indexes the endpoints of a DNSEndpoint by DNS name.
func endpointsByName(ep *externaldnsv1alpha1.DNSEndpoint) map[string]*externaldnsv1alpha1.Endpoint {
	out := make(map[string]*externaldnsv1alpha1.Endpoint, len(ep.Spec.Endpoints))
	for _, e := range ep.Spec.Endpoints {
		out[e.DNSName] = e
	}
	return out
}

func TestBuildDNSEndpointDefaults(t *testing.T) {
	ep := BuildDNSEndpoint(newCR(), testMons)

	if ep.Name != "rook-ceph" || ep.Namespace != "rook-ceph" {
		t.Errorf("got %s/%s, want rook-ceph/rook-ceph", ep.Namespace, ep.Name)
	}
	if len(ep.Spec.Endpoints) != 4 {
		t.Fatalf("got %d endpoints, want 4 (3 mons + aggregate)", len(ep.Spec.Endpoints))
	}

	byName := endpointsByName(ep)

	wantA := map[string]string{
		"mon1.ceph.example.com": "192.168.1.103", // g
		"mon2.ceph.example.com": "192.168.1.107", // j
		"mon3.ceph.example.com": "192.168.1.105", // l
	}
	for name, target := range wantA {
		e, ok := byName[name]
		if !ok {
			t.Errorf("missing endpoint %s", name)
			continue
		}
		if e.RecordType != externaldnsv1alpha1.RecordTypeA {
			t.Errorf("%s: record type %q, want A", name, e.RecordType)
		}
		if e.RecordTTL != 300 {
			t.Errorf("%s: TTL %d, want 300", name, e.RecordTTL)
		}
		if len(e.Targets) != 1 || e.Targets[0] != target {
			t.Errorf("%s: targets %v, want [%s]", name, e.Targets, target)
		}
	}

	all, ok := byName["mon-all.ceph.example.com"]
	if !ok {
		t.Fatal("missing aggregate record mon-all.ceph.example.com")
	}
	wantAll := []string{"192.168.1.103", "192.168.1.107", "192.168.1.105"}
	if len(all.Targets) != len(wantAll) {
		t.Fatalf("aggregate targets %v, want %v", all.Targets, wantAll)
	}
	for i := range wantAll {
		if all.Targets[i] != wantAll[i] {
			t.Errorf("aggregate target %d: got %s, want %s", i, all.Targets[i], wantAll[i])
		}
	}
}

func TestBuildDNSEndpointSRV(t *testing.T) {
	ep := BuildDNSEndpoint(newCR(func(cr *cephmonsv1alpha1.CephMonDNSEndpoint) {
		cr.Spec.SRV.Enabled = true
	}), testMons)

	if len(ep.Spec.Endpoints) != 5 {
		t.Fatalf("got %d endpoints, want 5 (3 mons + aggregate + SRV)", len(ep.Spec.Endpoints))
	}

	srv, ok := endpointsByName(ep)["_ceph-mon._tcp.ceph.example.com"]
	if !ok {
		t.Fatal("missing SRV record")
	}
	if srv.RecordType != externaldnsv1alpha1.RecordTypeSRV {
		t.Errorf("record type %q, want SRV", srv.RecordType)
	}
	want := []string{
		"0 10 6789 mon1.ceph.example.com",
		"0 10 6789 mon2.ceph.example.com",
		"0 10 6789 mon3.ceph.example.com",
	}
	if len(srv.Targets) != len(want) {
		t.Fatalf("SRV targets %v, want %v", srv.Targets, want)
	}
	for i := range want {
		if srv.Targets[i] != want[i] {
			t.Errorf("SRV target %d: got %q, want %q", i, srv.Targets[i], want[i])
		}
	}
}

func TestBuildDNSEndpointAggregateDisabled(t *testing.T) {
	ep := BuildDNSEndpoint(newCR(func(cr *cephmonsv1alpha1.CephMonDNSEndpoint) {
		cr.Spec.AllRecordName = ""
	}), testMons)

	if len(ep.Spec.Endpoints) != 3 {
		t.Fatalf("got %d endpoints, want 3 (no aggregate)", len(ep.Spec.Endpoints))
	}
}

func TestBuildDNSEndpointIDTemplate(t *testing.T) {
	ep := BuildDNSEndpoint(newCR(func(cr *cephmonsv1alpha1.CephMonDNSEndpoint) {
		cr.Spec.RecordTemplate = "ceph-mon-{id}"
	}), testMons)

	byName := endpointsByName(ep)
	for _, name := range []string{"ceph-mon-g.ceph.example.com", "ceph-mon-j.ceph.example.com", "ceph-mon-l.ceph.example.com"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing endpoint %s", name)
		}
	}
}

func TestBuildDNSEndpointDedupesAggregate(t *testing.T) {
	mons := []MonEndpoint{
		{ID: "a", NodeName: "n1", Address: "192.168.1.10", Port: 6789},
		{ID: "b", NodeName: "n1", Address: "192.168.1.10", Port: 6789}, // same IP as a
		{ID: "c", NodeName: "n2", Address: "192.168.1.11", Port: 6789},
	}
	ep := BuildDNSEndpoint(newCR(), mons)

	all := endpointsByName(ep)["mon-all.ceph.example.com"]
	if all == nil {
		t.Fatal("missing aggregate record")
	}
	if len(all.Targets) != 2 {
		t.Errorf("aggregate targets %v, want 2 deduped entries", all.Targets)
	}
	// Per-mon records must still exist for both mons.
	if len(ep.Spec.Endpoints) != 4 {
		t.Errorf("got %d endpoints, want 4 (3 mons + aggregate)", len(ep.Spec.Endpoints))
	}
}

func TestBuildDNSEndpointCustomTTLAndNamespace(t *testing.T) {
	ep := BuildDNSEndpoint(newCR(func(cr *cephmonsv1alpha1.CephMonDNSEndpoint) {
		cr.Spec.TTL = 60
		cr.Spec.TargetNamespace = "dns"
	}), testMons)

	if ep.Namespace != "dns" {
		t.Errorf("namespace %q, want dns", ep.Namespace)
	}
	for _, e := range ep.Spec.Endpoints {
		if e.RecordTTL != 60 {
			t.Errorf("%s: TTL %d, want 60", e.DNSName, e.RecordTTL)
		}
	}
}
