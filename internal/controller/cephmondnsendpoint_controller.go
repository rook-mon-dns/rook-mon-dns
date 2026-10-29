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
	"errors"
	"fmt"
	"slices"
	"strings"
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
	ReasonSynced              = "Synced"
	ReasonConfigMapNotFound   = "ConfigMapNotFound"
	ReasonMappingEmpty        = "MappingEmpty"
	ReasonMappingInvalid      = "MappingInvalid"
	ReasonHostNetworkRequired = "HostNetworkRequired"
	ReasonRecordNameConflict  = "RecordNameConflict"
	ReasonDNSEndpointConflict = "DNSEndpointConflict"
	ReasonZoneNotAllowed      = "ZoneNotAllowed"

	// DefaultConfigMapName is the default name of the Rook mon endpoints
	// ConfigMap. Keep in sync with the CRD default of spec.configMapRef.name.
	DefaultConfigMapName = "rook-ceph-mon-endpoints"

	// retryInterval is the backoff for conditions whose fix is not observed
	// by a watch (a missing ConfigMap, a DNSEndpoint owned by someone else).
	retryInterval = time.Minute
)

// errNotOwned is returned when a DNSEndpoint with the CR's name exists but is
// not controlled by the CR.
var errNotOwned = errors.New("DNSEndpoint exists and is not owned by this CephMonDNSEndpoint")

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

	// Let the garbage collector remove the DNSEndpoint; recreating it here
	// would fight foreground cascading deletion.
	if !cr.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Guardrail: zone allowlist. Records in a disallowed zone are removed.
	if !r.zoneAllowed(cr.Spec.Zone) {
		return r.reject(ctx, cr, ReasonZoneNotAllowed,
			fmt.Sprintf("spec.zone %q is not contained in the operator's allowed zones", cr.Spec.Zone))
	}

	// All other failures keep the last published records: a briefly broken
	// ConfigMap (e.g. during a mon failover) must not take down mon DNS.

	// Load the source ConfigMap.
	cmKey := types.NamespacedName{Namespace: cr.Namespace, Name: configMapName(cr)}
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, cmKey, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: retryInterval},
				r.setNotReady(ctx, cr, ReasonConfigMapNotFound, fmt.Sprintf("ConfigMap %s not found", cmKey))
		}
		return ctrl.Result{}, err
	}

	// Parse the mon endpoints (node IPs from the mapping field).
	mons, err := ParseMonEndpoints(cm)
	switch {
	case errors.Is(err, ErrMappingEmpty):
		return ctrl.Result{}, r.setNotReady(ctx, cr, ReasonMappingEmpty,
			fmt.Sprintf("ConfigMap %s has no node mapping; mons must run with hostNetwork on fixed nodes", cmKey))
	case errors.Is(err, ErrHostNetworkRequired):
		return ctrl.Result{}, r.setNotReady(ctx, cr, ReasonHostNetworkRequired, err.Error())
	case err != nil:
		return ctrl.Result{}, r.setNotReady(ctx, cr, ReasonMappingInvalid, err.Error())
	}

	desired, err := BuildDNSEndpoint(cr, mons)
	if err != nil {
		return ctrl.Result{}, r.setNotReady(ctx, cr, ReasonRecordNameConflict, err.Error())
	}

	// Publish the DNSEndpoint, but never take over one we do not own.
	ep := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, ep, func() error {
		if ep.ResourceVersion != "" && !metav1.IsControlledBy(ep, cr) {
			return errNotOwned
		}
		ep.Labels = desired.Labels
		ep.Spec = desired.Spec
		return controllerutil.SetControllerReference(cr, ep, r.Scheme)
	})
	if errors.Is(err, errNotOwned) {
		return ctrl.Result{RequeueAfter: retryInterval}, r.setNotReady(ctx, cr, ReasonDNSEndpointConflict,
			fmt.Sprintf("DNSEndpoint %s/%s already exists and is not owned by this resource; rename one of them",
				ep.Namespace, ep.Name))
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile DNSEndpoint %s/%s: %w", ep.Namespace, ep.Name, err)
	}
	if op != controllerutil.OperationResultNone {
		log.Info("Reconciled DNSEndpoint", "namespace", ep.Namespace, "name", ep.Name, "operation", op,
			"endpoints", len(ep.Spec.Endpoints))
	}

	// Update status.
	cr.Status.ObservedGeneration = cr.Generation
	cr.Status.PublishedEndpoints = publishedEndpoints(ep)
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

// reject sets Ready=False with the given reason and deletes the DNSEndpoint
// owned by the CR, if any. It does not requeue: the fix requires a CR change,
// which triggers a new reconcile.
func (r *CephMonDNSEndpointReconciler) reject(ctx context.Context, cr *cephmonsv1alpha1.CephMonDNSEndpoint,
	reason, message string) (ctrl.Result, error) {
	ep := &externaldnsv1alpha1.DNSEndpoint{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, ep)
	switch {
	case err == nil && metav1.IsControlledBy(ep, cr):
		if err := r.Delete(ctx, ep); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete DNSEndpoint after rejection: %w", err)
		}
		logf.FromContext(ctx).Info("Deleted DNSEndpoint after rejection", "reason", reason,
			"namespace", ep.Namespace, "name", ep.Name)
	case client.IgnoreNotFound(err) != nil:
		return ctrl.Result{}, err
	}

	cr.Status.PublishedEndpoints = nil
	return ctrl.Result{}, r.setNotReady(ctx, cr, reason, message)
}

// setNotReady sets the Ready condition to False and persists the status.
// status.publishedEndpoints is left as is: it still describes the records the
// DNSEndpoint serves.
func (r *CephMonDNSEndpointReconciler) setNotReady(ctx context.Context, cr *cephmonsv1alpha1.CephMonDNSEndpoint,
	reason, message string) error {
	logf.FromContext(ctx).Info("CephMonDNSEndpoint not ready", "reason", reason, "message", message)

	cr.Status.ObservedGeneration = cr.Generation
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
	return slices.ContainsFunc(r.AllowedZones, func(allowed string) bool {
		return strings.EqualFold(strings.TrimSuffix(allowed, "."), zone)
	})
}

// configMapName returns the name of the referenced ConfigMap.
func configMapName(cr *cephmonsv1alpha1.CephMonDNSEndpoint) string {
	if cr.Spec.ConfigMapRef.Name != "" {
		return cr.Spec.ConfigMapRef.Name
	}
	return DefaultConfigMapName
}

// publishedEndpoints converts the records of the DNSEndpoint into status entries.
func publishedEndpoints(ep *externaldnsv1alpha1.DNSEndpoint) []cephmonsv1alpha1.PublishedEndpoint {
	endpoints := make([]cephmonsv1alpha1.PublishedEndpoint, 0, len(ep.Spec.Endpoints))
	for _, e := range ep.Spec.Endpoints {
		endpoints = append(endpoints, cephmonsv1alpha1.PublishedEndpoint{
			DNSName:    e.DNSName,
			RecordType: e.RecordType,
			Targets:    []string(e.Targets),
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
		if configMapName(cr) == obj.GetName() && cr.Namespace == obj.GetNamespace() {
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
