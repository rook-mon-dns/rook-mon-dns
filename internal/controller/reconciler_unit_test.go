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
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cephmonsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/api/v1alpha1"
	externaldnsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/internal/externaldns/v1alpha1"
)

var reconcileCtx = context.Background()

// newTestCR returns a CR with all defaulted fields set explicitly (the fake
// client does not apply CRD defaults).
func newTestCR(mutators ...func(*cephmonsv1alpha1.CephMonDNSEndpoint)) *cephmonsv1alpha1.CephMonDNSEndpoint {
	cr := &cephmonsv1alpha1.CephMonDNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph", Namespace: "rook-ceph", Generation: 1},
		Spec: cephmonsv1alpha1.CephMonDNSEndpointSpec{
			ConfigMapRef:   cephmonsv1alpha1.ConfigMapReference{Name: DefaultConfigMapName},
			Zone:           "ceph.example.com",
			RecordTemplate: "mon{n}",
			AllRecordName:  "mon-all",
			TTL:            300,
			SRV:            cephmonsv1alpha1.SRVSpec{Enabled: true, Name: "_ceph-mon._tcp"},
		},
	}
	for _, mutate := range mutators {
		mutate(cr)
	}
	return cr
}

func realConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigMapName, Namespace: "rook-ceph"},
		Data: map[string]string{
			ConfigMapMappingKey: realMapping,
			ConfigMapDataKey:    realData,
		},
	}
}

func newFakeReconciler(t *testing.T, allowedZones []string, objs ...client.Object) *CephMonDNSEndpointReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := cephmonsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := externaldnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&cephmonsv1alpha1.CephMonDNSEndpoint{}).
		Build()
	return &CephMonDNSEndpointReconciler{
		Client:       cl,
		Scheme:       scheme,
		Recorder:     record.NewFakeRecorder(16),
		AllowedZones: allowedZones,
	}
}

func reconcileOnce(t *testing.T, r *CephMonDNSEndpointReconciler) reconcile.Result {
	t.Helper()
	result, err := r.Reconcile(reconcileCtx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "rook-ceph", Namespace: "rook-ceph"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	return result
}

func getCR(t *testing.T, r *CephMonDNSEndpointReconciler) *cephmonsv1alpha1.CephMonDNSEndpoint {
	t.Helper()
	cr := &cephmonsv1alpha1.CephMonDNSEndpoint{}
	if err := r.Get(reconcileCtx, types.NamespacedName{Name: "rook-ceph", Namespace: "rook-ceph"}, cr); err != nil {
		t.Fatalf("failed to get CR: %v", err)
	}
	return cr
}

func getDNSEndpoint(t *testing.T, r *CephMonDNSEndpointReconciler) *externaldnsv1alpha1.DNSEndpoint {
	t.Helper()
	ep := &externaldnsv1alpha1.DNSEndpoint{}
	if err := r.Get(reconcileCtx, types.NamespacedName{Name: "rook-ceph", Namespace: "rook-ceph"}, ep); err != nil {
		t.Fatalf("failed to get DNSEndpoint: %v", err)
	}
	return ep
}

func readyCondition(cr *cephmonsv1alpha1.CephMonDNSEndpoint) *metav1.Condition {
	return meta.FindStatusCondition(cr.Status.Conditions, ConditionTypeReady)
}

func TestReconcileHappyPath(t *testing.T) {
	r := newFakeReconciler(t, nil, newTestCR(), realConfigMap())

	reconcileOnce(t, r)

	ep := getDNSEndpoint(t, r)
	if len(ep.Spec.Endpoints) != 5 { // 3 A + aggregate + SRV
		t.Errorf("got %d endpoints, want 5", len(ep.Spec.Endpoints))
	}
	if !controllerutil.HasControllerReference(ep) {
		t.Error("DNSEndpoint has no controller reference")
	}

	cr := getCR(t, r)
	cond := readyCondition(cr)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonSynced {
		t.Errorf("Ready condition: got %+v, want True/%s", cond, ReasonSynced)
	}
	if len(cr.Status.PublishedEndpoints) != 3 {
		t.Errorf("got %d published endpoints, want 3", len(cr.Status.PublishedEndpoints))
	}
	if cr.Status.ObservedGeneration != 1 {
		t.Errorf("observedGeneration %d, want 1", cr.Status.ObservedGeneration)
	}
}

func TestReconcileConfigMapNotFound(t *testing.T) {
	r := newFakeReconciler(t, nil, newTestCR())

	result := reconcileOnce(t, r)

	if result.RequeueAfter != configMapNotFoundRequeue {
		t.Errorf("requeueAfter %v, want %v", result.RequeueAfter, configMapNotFoundRequeue)
	}
	cond := readyCondition(getCR(t, r))
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonConfigMapNotFound {
		t.Errorf("Ready condition: got %+v, want False/%s", cond, ReasonConfigMapNotFound)
	}
}

func TestReconcileMappingEmpty(t *testing.T) {
	cm := realConfigMap()
	cm.Data[ConfigMapMappingKey] = ""
	r := newFakeReconciler(t, nil, newTestCR(), cm)

	reconcileOnce(t, r)

	cond := readyCondition(getCR(t, r))
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonMappingEmpty {
		t.Errorf("Ready condition: got %+v, want False/%s", cond, ReasonMappingEmpty)
	}
	// No DNSEndpoint may be created.
	ep := &externaldnsv1alpha1.DNSEndpoint{}
	err := r.Get(reconcileCtx, types.NamespacedName{Name: "rook-ceph", Namespace: "rook-ceph"}, ep)
	if err == nil {
		t.Error("DNSEndpoint was created despite empty mapping")
	}
}

func TestReconcileCrossNamespaceDenied(t *testing.T) {
	cr := newTestCR(func(cr *cephmonsv1alpha1.CephMonDNSEndpoint) {
		cr.Spec.ConfigMapRef.Namespace = "other-namespace"
	})
	r := newFakeReconciler(t, nil, cr, realConfigMap())

	reconcileOnce(t, r)

	cond := readyCondition(getCR(t, r))
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonCrossNamespaceRefDenied {
		t.Errorf("Ready condition: got %+v, want False/%s", cond, ReasonCrossNamespaceRefDenied)
	}
}

func TestReconcileZoneNotAllowed(t *testing.T) {
	r := newFakeReconciler(t, []string{"other.example.com"}, newTestCR(), realConfigMap())

	reconcileOnce(t, r)

	cond := readyCondition(getCR(t, r))
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonZoneNotAllowed {
		t.Errorf("Ready condition: got %+v, want False/%s", cond, ReasonZoneNotAllowed)
	}
}

func TestReconcileUpdatesOnConfigMapChange(t *testing.T) {
	cm := realConfigMap()
	r := newFakeReconciler(t, nil, newTestCR(), cm)

	reconcileOnce(t, r)
	ep := getDNSEndpoint(t, r)
	byName := endpointsByName(ep)
	if byName["mon1.ceph.example.com"].Targets[0] != "192.168.1.103" {
		t.Fatalf("unexpected initial target: %v", byName["mon1.ceph.example.com"].Targets)
	}

	// Simulate a mon failover: mon g moves to a new node IP.
	cm = realConfigMap()
	cm.Data[ConfigMapMappingKey] = `{"node":{` +
		`"g":{"Name":"talos-node-04","Hostname":"talos-node-04","Address":"192.168.1.110"},` +
		`"j":{"Name":"talos-node-03","Hostname":"talos-node-03","Address":"192.168.1.107"},` +
		`"l":{"Name":"talos-node-02","Hostname":"talos-node-02","Address":"192.168.1.105"}}}`
	if err := r.Update(reconcileCtx, cm); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(t, r)
	ep = getDNSEndpoint(t, r)
	if got := endpointsByName(ep)["mon1.ceph.example.com"].Targets[0]; got != "192.168.1.110" {
		t.Errorf("mon1 target after failover: got %s, want 192.168.1.110", got)
	}
}

func TestReconcileRejectionDeletesDNSEndpoint(t *testing.T) {
	cr := newTestCR()
	r := newFakeReconciler(t, nil, cr, realConfigMap())

	reconcileOnce(t, r)
	getDNSEndpoint(t, r) // must exist now

	// Edit the CR into an invalid one: zone no longer allowed afterwards.
	r.AllowedZones = []string{"other.example.com"}
	if err := r.Get(reconcileCtx, types.NamespacedName{Name: "rook-ceph", Namespace: "rook-ceph"}, cr); err != nil {
		t.Fatal(err)
	}
	cr.Generation = 2
	if err := r.Update(reconcileCtx, cr); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(t, r)

	ep := &externaldnsv1alpha1.DNSEndpoint{}
	if err := r.Get(reconcileCtx, types.NamespacedName{Name: "rook-ceph", Namespace: "rook-ceph"}, ep); err == nil {
		t.Error("DNSEndpoint still exists after rejection")
	}
}

func TestZoneAllowed(t *testing.T) {
	r := &CephMonDNSEndpointReconciler{}
	if !r.zoneAllowed("anything.example.com") {
		t.Error("empty allowlist must allow every zone")
	}
	r.AllowedZones = []string{"ceph.example.com", "storage.example.com"}
	if !r.zoneAllowed("ceph.example.com") {
		t.Error("listed zone must be allowed")
	}
	if r.zoneAllowed("other.example.com") {
		t.Error("unlisted zone must be denied")
	}
}
