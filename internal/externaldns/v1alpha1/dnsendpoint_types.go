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

// Well-known record types supported by external-dns.
const (
	RecordTypeA    = "A"
	RecordTypeAAAA = "AAAA"
	RecordTypeSRV  = "SRV"
)

// TTL is the TTL for a DNS record in seconds.
type TTL int64

// Targets is the list of targets the DNS record points to.
type Targets []string

// Labels stores labels defined for the Endpoint.
type Labels map[string]string

// ProviderSpecificProperty holds a name/value pair of a configuration which
// is specific to individual DNS providers.
type ProviderSpecificProperty struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// ProviderSpecific stores provider specific config.
type ProviderSpecific []ProviderSpecificProperty

// Endpoint is a high-level way of a connection between a service and an IP.
type Endpoint struct {
	// The hostname of the DNS record
	// +optional
	DNSName string `json:"dnsName,omitempty"`
	// RecordType type of record, e.g. CNAME, A, AAAA, SRV, TXT etc
	// +optional
	RecordType string `json:"recordType,omitempty"`
	// Identifier to distinguish multiple records with the same name and type
	// +optional
	SetIdentifier string `json:"setIdentifier,omitempty"`
	// TTL for the record
	// +optional
	RecordTTL TTL `json:"recordTTL,omitempty"`
	// Labels stores labels defined for the Endpoint
	// +optional
	Labels Labels `json:"labels,omitempty"`
	// The targets the DNS record points to
	// +optional
	Targets Targets `json:"targets,omitempty"`
	// ProviderSpecific stores provider specific config
	// +optional
	ProviderSpecific ProviderSpecific `json:"providerSpecific,omitempty"`
}

// DNSEndpointSpec defines the desired state of DNSEndpoint.
type DNSEndpointSpec struct {
	// +optional
	Endpoints []*Endpoint `json:"endpoints,omitempty"`
}

// DNSEndpointStatus defines the observed state of DNSEndpoint.
type DNSEndpointStatus struct {
	// The generation observed by the external-dns controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,path=dnsendpoints,shortName=dnse

// DNSEndpoint is a contract that a user-specified CRD must implement to be
// used as a source for external-dns.
type DNSEndpoint struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +optional
	Spec DNSEndpointSpec `json:"spec,omitempty"`

	// +optional
	Status DNSEndpointStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DNSEndpointList contains a list of DNSEndpoint.
type DNSEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DNSEndpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &DNSEndpoint{}, &DNSEndpointList{})
		return nil
	})
}
