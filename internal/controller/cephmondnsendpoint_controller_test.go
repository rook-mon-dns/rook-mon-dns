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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cephmonsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/api/v1alpha1"
	externaldnsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/internal/externaldns/v1alpha1"
)

var _ = Describe("CephMonDNSEndpoint Controller", func() {
	const (
		resourceName      = "test-resource"
		resourceNamespace = "default"
		zone              = "ceph.example.com"
	)

	ctx := context.Background()

	namespacedName := types.NamespacedName{Name: resourceName, Namespace: resourceNamespace}
	cmName := types.NamespacedName{Name: DefaultConfigMapName, Namespace: resourceNamespace}

	var reconciler *CephMonDNSEndpointReconciler

	newReconciler := func() *CephMonDNSEndpointReconciler {
		return &CephMonDNSEndpointReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(32),
		}
	}

	reconcileResource := func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())
	}

	createConfigMap := func(mapping string) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName.Name, Namespace: cmName.Namespace},
			Data: map[string]string{
				ConfigMapMappingKey: mapping,
				ConfigMapDataKey:    realData,
			},
		}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
	}

	createResource := func(mutators ...func(*cephmonsv1alpha1.CephMonDNSEndpoint)) {
		cr := &cephmonsv1alpha1.CephMonDNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{Name: namespacedName.Name, Namespace: namespacedName.Namespace},
			Spec:       cephmonsv1alpha1.CephMonDNSEndpointSpec{Zone: zone},
		}
		for _, mutate := range mutators {
			mutate(cr)
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	}

	getResource := func() *cephmonsv1alpha1.CephMonDNSEndpoint {
		cr := &cephmonsv1alpha1.CephMonDNSEndpoint{}
		Expect(k8sClient.Get(ctx, namespacedName, cr)).To(Succeed())
		return cr
	}

	getDNSEndpoint := func() *externaldnsv1alpha1.DNSEndpoint {
		ep := &externaldnsv1alpha1.DNSEndpoint{}
		Expect(k8sClient.Get(ctx, namespacedName, ep)).To(Succeed())
		return ep
	}

	BeforeEach(func() {
		reconciler = newReconciler()
	})

	AfterEach(func() {
		cr := &cephmonsv1alpha1.CephMonDNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{Name: namespacedName.Name, Namespace: namespacedName.Namespace},
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cr))).To(Succeed())

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName.Name, Namespace: cmName.Namespace},
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cm))).To(Succeed())

		// envtest has no garbage collector, so owned DNSEndpoints must be
		// removed explicitly.
		ep := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{Name: namespacedName.Name, Namespace: namespacedName.Namespace},
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, ep))).To(Succeed())
	})

	Context("CRD defaulting and validation", func() {
		It("applies the documented defaults", func() {
			createResource()
			cr := getResource()

			Expect(cr.Spec.ConfigMapRef.Name).To(Equal(DefaultConfigMapName))
			Expect(cr.Spec.RecordTemplate).To(Equal("mon{n}"))
			Expect(cr.Spec.AllRecordName).To(Equal("mon-all"))
			Expect(cr.Spec.TTL).To(Equal(int64(300)))
			Expect(cr.Spec.SRV.Enabled).To(BeFalse())
			Expect(cr.Spec.SRV.Name).To(Equal("_ceph-mon._tcp"))
		})

		It("rejects a resource without zone", func() {
			cr := &cephmonsv1alpha1.CephMonDNSEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: namespacedName.Name, Namespace: namespacedName.Namespace},
			}
			Expect(k8sClient.Create(ctx, cr)).NotTo(Succeed())
		})

		It("rejects record names that are not valid DNS names", func() {
			for _, mutate := range []func(*cephmonsv1alpha1.CephMonDNSEndpointSpec){
				func(s *cephmonsv1alpha1.CephMonDNSEndpointSpec) { s.RecordTemplate = "Mon_{n}" },
				func(s *cephmonsv1alpha1.CephMonDNSEndpointSpec) { s.AllRecordName = "all mons" },
				func(s *cephmonsv1alpha1.CephMonDNSEndpointSpec) { s.SRV.Name = "_ceph mon" },
			} {
				cr := &cephmonsv1alpha1.CephMonDNSEndpoint{
					ObjectMeta: metav1.ObjectMeta{Name: namespacedName.Name, Namespace: namespacedName.Namespace},
					Spec:       cephmonsv1alpha1.CephMonDNSEndpointSpec{Zone: zone},
				}
				mutate(&cr.Spec)
				Expect(k8sClient.Create(ctx, cr)).NotTo(Succeed())
			}
		})

		It("rejects a recordTemplate without placeholders", func() {
			cr := &cephmonsv1alpha1.CephMonDNSEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: namespacedName.Name, Namespace: namespacedName.Namespace},
				Spec: cephmonsv1alpha1.CephMonDNSEndpointSpec{
					Zone:           zone,
					RecordTemplate: "static-name",
				},
			}
			Expect(k8sClient.Create(ctx, cr)).NotTo(Succeed())
		})
	})

	Context("When the source ConfigMap is present", func() {
		It("publishes a DNSEndpoint with all records", func() {
			createConfigMap(realMapping)
			createResource(func(cr *cephmonsv1alpha1.CephMonDNSEndpoint) {
				cr.Spec.SRV.Enabled = true
			})

			reconcileResource()

			ep := getDNSEndpoint()
			Expect(ep.Spec.Endpoints).To(HaveLen(5)) // 3 mons + aggregate + SRV
			Expect(ep.OwnerReferences).To(HaveLen(1))
			Expect(ep.OwnerReferences[0].Kind).To(Equal("CephMonDNSEndpoint"))

			byName := map[string]*externaldnsv1alpha1.Endpoint{}
			for _, e := range ep.Spec.Endpoints {
				byName[e.DNSName] = e
			}
			Expect(byName).To(HaveKey("mon1.ceph.example.com"))
			Expect(byName).To(HaveKey("mon2.ceph.example.com"))
			Expect(byName).To(HaveKey("mon3.ceph.example.com"))
			Expect(byName).To(HaveKey("mon-all.ceph.example.com"))
			Expect(byName).To(HaveKey("_ceph-mon._tcp.ceph.example.com"))
			Expect(byName["mon1.ceph.example.com"].Targets).To(Equal(externaldnsv1alpha1.Targets{"192.168.1.103"}))
			Expect(byName["mon-all.ceph.example.com"].Targets).To(HaveLen(3))

			cond := meta.FindStatusCondition(getResource().Status.Conditions, ConditionTypeReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(ReasonSynced))
		})

		It("updates the DNSEndpoint when a mon moves to another node", func() {
			createConfigMap(realMapping)
			createResource()
			reconcileResource()

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, cmName, cm)).To(Succeed())
			cm.Data[ConfigMapMappingKey] = `{"node":{` +
				`"g":{"Name":"talos-node-04","Hostname":"talos-node-04","Address":"192.168.1.110"},` +
				`"j":{"Name":"talos-node-03","Hostname":"talos-node-03","Address":"192.168.1.107"},` +
				`"l":{"Name":"talos-node-02","Hostname":"talos-node-02","Address":"192.168.1.105"}}}`
			cm.Data[ConfigMapDataKey] = "g=192.168.1.110:6789,l=192.168.1.105:6789,j=192.168.1.107:6789"
			Expect(k8sClient.Update(ctx, cm)).To(Succeed())

			reconcileResource()

			for _, e := range getDNSEndpoint().Spec.Endpoints {
				if e.DNSName == "mon1.ceph.example.com" {
					Expect(e.Targets).To(Equal(externaldnsv1alpha1.Targets{"192.168.1.110"}))
					return
				}
			}
			Fail("mon1.ceph.example.com not found")
		})

		It("keeps the DNSEndpoint and sets MappingEmpty when the mapping is emptied", func() {
			createConfigMap(realMapping)
			createResource()
			reconcileResource()
			getDNSEndpoint() // must exist

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, cmName, cm)).To(Succeed())
			cm.Data[ConfigMapMappingKey] = ""
			Expect(k8sClient.Update(ctx, cm)).To(Succeed())

			reconcileResource()

			Expect(getDNSEndpoint().Spec.Endpoints).To(HaveLen(4))

			cond := meta.FindStatusCondition(getResource().Status.Conditions, ConditionTypeReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(ReasonMappingEmpty))
		})
	})

	Context("Guardrails", func() {
		It("does not take over a DNSEndpoint it does not own", func() {
			createConfigMap(realMapping)
			foreign := &externaldnsv1alpha1.DNSEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: namespacedName.Name, Namespace: namespacedName.Namespace},
				Spec: externaldnsv1alpha1.DNSEndpointSpec{Endpoints: []*externaldnsv1alpha1.Endpoint{
					{DNSName: "other.example.com", RecordType: "A", Targets: externaldnsv1alpha1.Targets{"192.0.2.1"}},
				}},
			}
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			createResource()

			reconcileResource()

			cond := meta.FindStatusCondition(getResource().Status.Conditions, ConditionTypeReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(ReasonDNSEndpointConflict))
			ep := getDNSEndpoint()
			Expect(ep.OwnerReferences).To(BeEmpty())
			Expect(ep.Spec.Endpoints).To(HaveLen(1))
		})

		It("sets ConfigMapNotFound when the source ConfigMap is missing", func() {
			createResource()

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(retryInterval))

			cond := meta.FindStatusCondition(getResource().Status.Conditions, ConditionTypeReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(ReasonConfigMapNotFound))
		})
	})
})
