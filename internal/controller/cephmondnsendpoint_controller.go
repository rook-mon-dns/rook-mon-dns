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
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cephmonsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/api/v1alpha1"
	externaldnsv1alpha1 "github.com/rook-mon-dns/rook-mon-dns/internal/externaldns/v1alpha1"
)

const (
	// ConditionTypeReady indicates that the published DNS records are in sync
	// with the Rook Ceph mon endpoints ConfigMap.
	ConditionTypeReady = "Ready"

	// Condition reasons.
	ReasonSynced                  = "Synced"
	ReasonConfigMapNotFound       = "ConfigMapNotFound"
	ReasonMappingEmpty            = "MappingEmpty"
	ReasonMappingInvalid          = "MappingInvalid"
	ReasonCrossNamespaceRefDenied = "CrossNamespaceRefDenied"
	ReasonZoneNotAllowed          = "ZoneNotAllowed"

	// DefaultConfigMapName is the default name of the Rook mon endpoints
	// ConfigMap. Keep in sync with the CRD default of spec.configMapRef.name.
	DefaultConfigMapName = "rook-ceph-mon-endpoints"

	// configMapNotFoundRequeue is the backoff for a missing ConfigMap. The
	// ConfigMap watch is the primary trigger; this is a safety net.
	configMapNotFoundRequeue = time.Minute
)

// CephMonDNSEndpointReconciler reconciles a CephMonDNSEndpoint object
type CephMonDNSEndpointReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// AllowedZones restricts which spec.zone values are accepted.
	// Empty means no restriction.
	AllowedZones []string
}

// +kubebuilder:rbac:groups=rook-mon-dns.github.io,resources=cephmondnsendpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=rook-mon-dns.github.io,resources=cephmondnsendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rook-mon-dns.github.io,resources=cephmondnsendpoints/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=externaldns.k8s.io,resources=dnsendpoints,verbs=get;list;watch;create;update;patch;delete

// Reconcile publishes the Rook Ceph mon node endpoints of the referenced
// ConfigMap as DNS records via a DNSEndpoint object.
func (r *CephMonDNSEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	cr := &cephmonsv1alpha1.CephMonDNSEndpoint{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Guardrail: references must not escape the CR namespace (v1).
	if ns := cr.Spec.ConfigMapRef.Namespace; ns != "" && ns != cr.Namespace {
		return r.reject(ctx, cr, ReasonCrossNamespaceRefDenied,
			fmt.Sprintf("spec.configMapRef.namespace %q must be empty or equal to the CR namespace %q", ns, cr.Namespace))
	}
	if ns := cr.Spec.TargetNamespace; ns != "" && ns != cr.Namespace {
		return r.reject(ctx, cr, ReasonCrossNamespaceRefDenied,
			fmt.Sprintf("spec.targetNamespace %q must be empty or equal to the CR namespace %q", ns, cr.Namespace))
	}

	// Guardrail: zone allowlist.
	if !r.zoneAllowed(cr.Spec.Zone) {
		return r.reject(ctx, cr, ReasonZoneNotAllowed,
			fmt.Sprintf("spec.zone %q is not contained in the operator's allowed zones", cr.Spec.Zone))
	}

	// Load the source ConfigMap.
	cmKey := types.NamespacedName{Namespace: configMapNamespace(cr), Name: configMapName(cr)}
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, cmKey, cm); err != nil {
		if apierrors.IsNotFound(err) {
			if uerr := r.setNotReady(ctx, cr, ReasonConfigMapNotFound,
				fmt.Sprintf("ConfigMap %s not found", cmKey)); uerr != nil {
				return ctrl.Result{}, uerr
			}
			return ctrl.Result{RequeueAfter: configMapNotFoundRequeue}, nil
		}
		return ctrl.Result{}, err
	}

	// Parse the mon endpoints (node IPs from the mapping field).
	mons, err := ParseMonEndpoints(cm)
	switch {
	case errors.Is(err, ErrMappingEmpty):
		return r.reject(ctx, cr, ReasonMappingEmpty,
			fmt.Sprintf("ConfigMap %s has no node mapping; publishing ClusterIPs is not supported (hostNetwork cluster required)", cmKey))
	case err != nil:
		return r.reject(ctx, cr, ReasonMappingInvalid, err.Error())
	}

	// Publish the DNSEndpoint.
	desired := BuildDNSEndpoint(cr, mons)
	ep := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, ep, func() error {
		ep.Labels = desired.Labels
		ep.Spec = desired.Spec
		return controllerutil.SetControllerReference(cr, ep, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile DNSEndpoint %s/%s: %w", ep.Namespace, ep.Name, err)
	}
	if op != controllerutil.OperationResultNone {
		log.Info("Reconciled DNSEndpoint", "namespace", ep.Namespace, "name", ep.Name, "operation", op,
			"endpoints", len(ep.Spec.Endpoints))
	}

	// Update status.
	cr.Status.ObservedGeneration = cr.Generation
	cr.Status.PublishedEndpoints = publishedEndpoints(cr, mons)
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             ReasonSynced,
		ObservedGeneration: cr.Generation,
		Message:            fmt.Sprintf("Published %d DNS records from ConfigMap %s", len(ep.Spec.Endpoints), cmKey),
	})
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		r.Recorder.Eventf(cr, nil, corev1.EventTypeNormal, ReasonSynced, "Reconcile",
			"Published %d DNS records in zone %s", len(ep.Spec.Endpoints), cr.Spec.Zone)
	}
	return ctrl.Result{}, nil
}

// reject sets Ready=False with the given reason, deletes a possibly existing
// DNSEndpoint (e.g. when a previously valid CR was edited into an invalid
// one), records a warning event and does not requeue (the fix requires a CR
// or ConfigMap change, which triggers a new reconcile via the watches).
func (r *CephMonDNSEndpointReconciler) reject(ctx context.Context, cr *cephmonsv1alpha1.CephMonDNSEndpoint,
	reason, message string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ep := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: targetNamespace(cr)},
	}
	if err := r.Delete(ctx, ep); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed to delete DNSEndpoint after rejection: %w", err)
	} else if err == nil {
		log.Info("Deleted DNSEndpoint after rejection", "reason", reason,
			"namespace", ep.Namespace, "name", ep.Name)
	}

	if err := r.setNotReady(ctx, cr, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// setNotReady sets the Ready condition to False and persists the status.
func (r *CephMonDNSEndpointReconciler) setNotReady(ctx context.Context, cr *cephmonsv1alpha1.CephMonDNSEndpoint,
	reason, message string) error {
	logf.FromContext(ctx).Info("CephMonDNSEndpoint not ready", "reason", reason, "message", message)

	cr.Status.ObservedGeneration = cr.Generation
	cr.Status.PublishedEndpoints = nil
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		ObservedGeneration: cr.Generation,
		Message:            message,
	})
	if err := r.Status().Update(ctx, cr); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reason, "Reconcile", "%s", message)
	return nil
}

// zoneAllowed reports whether the given zone passes the operator's allowlist.
func (r *CephMonDNSEndpointReconciler) zoneAllowed(zone string) bool {
	if len(r.AllowedZones) == 0 {
		return true
	}
	return slices.Contains(r.AllowedZones, zone)
}

// configMapName returns the name of the referenced ConfigMap.
func configMapName(cr *cephmonsv1alpha1.CephMonDNSEndpoint) string {
	if cr.Spec.ConfigMapRef.Name != "" {
		return cr.Spec.ConfigMapRef.Name
	}
	return DefaultConfigMapName
}

// configMapNamespace returns the namespace of the referenced ConfigMap.
func configMapNamespace(cr *cephmonsv1alpha1.CephMonDNSEndpoint) string {
	if cr.Spec.ConfigMapRef.Namespace != "" {
		return cr.Spec.ConfigMapRef.Namespace
	}
	return cr.Namespace
}

// publishedEndpoints computes the status entries for the per-monitor records.
func publishedEndpoints(cr *cephmonsv1alpha1.CephMonDNSEndpoint, mons []MonEndpoint) []cephmonsv1alpha1.PublishedEndpoint {
	endpoints := make([]cephmonsv1alpha1.PublishedEndpoint, 0, len(mons))
	for i, mon := range mons {
		endpoints = append(endpoints, cephmonsv1alpha1.PublishedEndpoint{
			DNSName: fqdn(renderRecordTemplate(cr.Spec.RecordTemplate, i+1, mon.ID), cr.Spec.Zone),
			MonID:   mon.ID,
			Target:  mon.Address,
		})
	}
	return endpoints
}

// configMapToRequests maps a ConfigMap to all CephMonDNSEndpoint resources
// referencing it.
func (r *CephMonDNSEndpointReconciler) configMapToRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &cephmonsv1alpha1.CephMonDNSEndpointList{}
	if err := r.List(ctx, list); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list CephMonDNSEndpoints for ConfigMap mapping")
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		cr := &list.Items[i]
		if configMapName(cr) == obj.GetName() && configMapNamespace(cr) == obj.GetNamespace() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *CephMonDNSEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cephmonsv1alpha1.CephMonDNSEndpoint{}).
		Owns(&externaldnsv1alpha1.DNSEndpoint{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.configMapToRequests)).
		Named("cephmondnsendpoint").
		Complete(r)
}
