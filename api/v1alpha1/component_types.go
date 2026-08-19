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

	// scaffold requests a one-time initial commit into the owned repository
	// once it's ready, rendered from a platform-scaffolds template.
	// Component's own controller creates (but does not own — see
	// PLATFORM_API_ARCHITECTURE.md's CREATION EXCEPTION section) a
	// ScaffoldRequest from this field. Presence/absence of this pointer is
	// what gates scaffolding entirely — an existing Component with no
	// spec.scaffold behaves exactly as it does today. Once the resulting
	// ScaffoldRequest completes, this field is never re-acted on, even if
	// changed — no auto-migration of an already-scaffolded repo.
	// +optional
	Scaffold *ScaffoldSpec `json:"scaffold,omitempty"`
}

// ScaffoldSpec is the desired scaffold template to render into the
// component's owned repository on first reconcile after it's ready.
type ScaffoldSpec struct {
	// template is the platform-scaffolds template name (e.g. golang-service).
	// +kubebuilder:validation:Required
	Template string `json:"template"`

	// version is the platform-scaffolds template's SemVer tag (e.g. 0.1.0),
	// without the template name prefix or a leading "v".
	// +kubebuilder:validation:Required
	Version string `json:"version"`
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

	// scaffold reflects the observed state of the created ScaffoldRequest.
	// Only set once spec.scaffold is set and a ScaffoldRequest has been
	// created for this Component.
	// +optional
	Scaffold *ScaffoldStatus `json:"scaffold,omitempty"`

	// conditions represent the current state of the Component resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types:
	// - "Ready": true once every owned related resource (currently just the
	//   GitHubRepository) is ready.
	// - "RepositoryReady": true once the owned GitHubRepository XR reports Ready.
	// - "Scaffolded": true once the created ScaffoldRequest reports its
	//   Completed condition True. Never reverts to False once True.
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ScaffoldStatus is the observed state of the ScaffoldRequest created for
// this component, mirrored from ScaffoldRequest.status so it's visible
// from Component without needing to separately inspect ScaffoldRequest.
type ScaffoldStatus struct {
	// template is the scaffold template that was requested.
	// +optional
	Template string `json:"template,omitempty"`

	// version is the scaffold template version that was requested.
	// +optional
	Version string `json:"version,omitempty"`

	// templateRevision is the immutable platform-scaffolds commit SHA the
	// template was actually rendered from, mirrored from
	// ScaffoldRequest.status.templateRevision.
	// +optional
	TemplateRevision string `json:"templateRevision,omitempty"`

	// commitSHA is the commit created in the component's own repository,
	// mirrored from ScaffoldRequest.status.commitSHA.
	// +optional
	CommitSHA string `json:"commitSHA,omitempty"`

	// completed mirrors the ScaffoldRequest's Completed condition.
	Completed bool `json:"completed"`
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
