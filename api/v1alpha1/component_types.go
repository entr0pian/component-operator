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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ComponentSpec defines the desired state of Component
type ComponentSpec struct {
	// owner is the team responsible for this component. Label-only today —
	// not forwarded to the GitHubRepository XR, which has no matching field.
	// +kubebuilder:validation:Required
	Owner string `json:"owner"`

	// repository describes the GitHub repository this component owns.
	// Component's own controller creates and owns the GitHubRepository XR
	// derived from this field — see PLATFORM_API_ARCHITECTURE.md's
	// OWNERSHIP EXCEPTION section. Deleting this Component cascade-deletes
	// that XR, and therefore the real GitHub repository.
	// +required
	Repository RepositorySpec `json:"repository"`
}

// RepositorySpec is the desired state of the component's owned GitHub repository.
type RepositorySpec struct {
	// name overrides the repository name; defaults to metadata.name when omitted.
	// +optional
	Name string `json:"name,omitempty"`

	// +kubebuilder:validation:Enum=public;private;internal
	// +kubebuilder:default=private
	// +optional
	Visibility string `json:"visibility,omitempty"`
}

// ComponentStatus defines the observed state of Component.
type ComponentStatus struct {
	// repository reflects the observed state of the owned GitHubRepository XR.
	// +optional
	Repository *RepositoryStatus `json:"repository,omitempty"`

	// conditions represent the current state of the Component resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types:
	// - "Ready": true once every owned related resource (currently just the
	//   GitHubRepository) is ready.
	// - "RepositoryReady": true once the owned GitHubRepository XR reports Ready.
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RepositoryStatus is the observed state of the component's owned GitHub repository.
type RepositoryStatus struct {
	// name is the actual name of the created repository.
	// +optional
	Name string `json:"name,omitempty"`

	// url is the HTML URL of the created repository, promoted from the XR.
	// +optional
	URL string `json:"url,omitempty"`

	// ready mirrors the owned GitHubRepository XR's Ready condition.
	Ready bool `json:"ready"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Component is the Schema for the components API
type Component struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Component
	// +required
	Spec ComponentSpec `json:"spec"`

	// status defines the observed state of Component
	// +optional
	Status ComponentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ComponentList contains a list of Component
type ComponentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Component `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Component{}, &ComponentList{})
}
