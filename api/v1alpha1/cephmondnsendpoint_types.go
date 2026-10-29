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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ConfigMapReference points to the Rook Ceph mon endpoints ConfigMap in the
// namespace of the CephMonDNSEndpoint.
type ConfigMapReference struct {
	// name of the ConfigMap that contains the Rook Ceph mon endpoints.
	// +kubebuilder:default="rook-ceph-mon-endpoints"
	// +kubebuilder:validation:MinLength=1
	// +optional
	Name string `json:"name,omitempty"`
}

// SRVSpec configures the optional SRV record for Ceph mon discovery
// ("mon dns srv name" in ceph.conf).
type SRVSpec struct {
	// enabled controls whether a SRV record is published in addition to the
	// A records.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// name of the SRV record, relative to the zone (e.g. "_ceph-mon._tcp"
	// results in "_ceph-mon._tcp.<zone>").
	// +kubebuilder:default="_ceph-mon._tcp"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(_?[a-z0-9]([-a-z0-9]*[a-z0-9])?[.])*_?[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Name string `json:"name,omitempty"`
}

// CephMonDNSEndpointSpec defines the desired state of CephMonDNSEndpoint
type CephMonDNSEndpointSpec struct {
	// configMapRef references the Rook Ceph mon endpoints ConfigMap.
	// +kubebuilder:default={}
	// +optional
	ConfigMapRef ConfigMapReference `json:"configMapRef,omitzero"`

	// zone is the base DNS zone all records are published in
	// (e.g. "ceph.example.com"). If the operator is started with
	// --allowed-zones, the zone must be contained in that list.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)*[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Zone string `json:"zone"`

	// recordTemplate is the per-monitor record name, relative to the zone.
	// Supported placeholders: {n} = sequence number (monitors sorted by mon
	// ID, starting at 1; can shift when a mon is replaced), {id} = Rook mon
	// ID. Example: "mon{n}" results in "mon1.<zone>", "mon2.<zone>".
	// +kubebuilder:default="mon{n}"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self.contains('{n}') || self.contains('{id}')",message="recordTemplate must contain at least one of the placeholders {n} or {id}"
	// +kubebuilder:validation:XValidation:rule="self.replace('{n}', '1').replace('{id}', 'a').matches('^([a-z0-9]([-a-z0-9]*[a-z0-9])?[.])*[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="recordTemplate must render to a lowercase DNS name"
	// +optional
	RecordTemplate string `json:"recordTemplate,omitempty"`

	// allRecordName is an additional A record (relative to the zone) that
	// contains all monitor node IPs at once. Set to an empty string to
	// disable the aggregate record. Must not collide with a per-monitor name.
	// +kubebuilder:default="mon-all"
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^$|^([a-z0-9]([-a-z0-9]*[a-z0-9])?[.])*[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	AllRecordName string `json:"allRecordName,omitempty"`

	// ttl of the published DNS records in seconds.
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=86400
	// +optional
	TTL int64 `json:"ttl,omitempty"`

	// srv configures the optional SRV record used for Ceph mon discovery.
	// +kubebuilder:default={}
	// +optional
	SRV SRVSpec `json:"srv,omitzero"`
}

// PublishedEndpoint describes a single published DNS record.
type PublishedEndpoint struct {
	// dnsName is the fully qualified DNS name of the record.
	// +required
	DNSName string `json:"dnsName"`

	// recordType is the DNS record type (A, AAAA or SRV).
	// +required
	RecordType string `json:"recordType"`

	// targets are the record values (node IPs, or SRV targets).
	// +listType=atomic
	// +required
	Targets []string `json:"targets"`
}

// CephMonDNSEndpointStatus defines the observed state of CephMonDNSEndpoint.
type CephMonDNSEndpointStatus struct {
	// observedGeneration is the most recent generation observed by the
	// controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// publishedEndpoints lists the DNS records currently published by the
	// operator.
	// +listType=atomic
	// +optional
	PublishedEndpoints []PublishedEndpoint `json:"publishedEndpoints,omitempty"`

	// conditions represent the current state of the CephMonDNSEndpoint resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Ready": DNS records are in sync with the Rook Ceph mon endpoints
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cmde
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=".spec.zone"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// CephMonDNSEndpoint is the Schema for the cephmondnsendpoints API
type CephMonDNSEndpoint struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CephMonDNSEndpoint
	// +required
	Spec CephMonDNSEndpointSpec `json:"spec"`

	// status defines the observed state of CephMonDNSEndpoint
	// +optional
	Status CephMonDNSEndpointStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CephMonDNSEndpointList contains a list of CephMonDNSEndpoint
type CephMonDNSEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CephMonDNSEndpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &CephMonDNSEndpoint{}, &CephMonDNSEndpointList{})
		return nil
	})
}
