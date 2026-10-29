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

// Package v1alpha1 contains a minimal, self-contained copy of the external-dns
// DNSEndpoint API (externaldns.k8s.io/v1alpha1).
//
// The external-dns project does not publish its CRD types as a Go module, so
// the types are mirrored here. They follow the upstream CRD schema at
// config/crd/standard/dnsendpoints.externaldns.k8s.io.yaml.
//
// +kubebuilder:object:generate=true
// +groupName=externaldns.k8s.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is group version used to register these objects.
var GroupVersion = schema.GroupVersion{Group: "externaldns.k8s.io", Version: "v1alpha1"}

// SchemeBuilder is used to add go types to the GroupVersionResource scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
})

// AddToScheme adds the types in this group-version to the given scheme.
var AddToScheme = SchemeBuilder.AddToScheme
