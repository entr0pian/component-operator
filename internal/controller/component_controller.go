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

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/entr0pian/component-operator/api/v1alpha1"
)

// githubRepositoryGVK is crossplane-compositions' apis/githubrepository XR
// (repo.taskapp.io/v1alpha1 GitHubRepository). Component is the one related
// resource type this controller creates and owns directly — see
// PLATFORM_API_ARCHITECTURE.md's OWNERSHIP EXCEPTION section. Every future
// capability (Database, Queue, ...) stays on the default model instead: an
// independent CR a developer creates directly, correlated only via
// componentRef + label, never created by this controller.
var githubRepositoryGVK = schema.GroupVersionKind{
	Group:   "repo.taskapp.io",
	Version: "v1alpha1",
	Kind:    "GitHubRepository",
}

// ComponentReconciler reconciles a Component object
type ComponentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.taskapp.io,resources=components,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.taskapp.io,resources=components/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.taskapp.io,resources=components/finalizers,verbs=update
// +kubebuilder:rbac:groups=repo.taskapp.io,resources=githubrepositories,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=repo.taskapp.io,resources=githubrepositories/status,verbs=get

// Reconcile creates and keeps in sync the GitHubRepository XR owned by this
// Component, then reflects its status back onto Component.status.
func (r *ComponentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	component := &platformv1alpha1.Component{}
	if err := r.Get(ctx, req.NamespacedName, component); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	statusBase := component.DeepCopy()

	repo, err := r.reconcileGitHubRepository(ctx, component)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.updateRepositoryStatus(component, repo)

	if err := r.patchStatusIfChanged(ctx, statusBase, component); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconciled Component", "component", component.Name)
	return ctrl.Result{}, nil
}

// reconcileGitHubRepository creates the GitHubRepository XR on first
// reconcile (with a controller ownerReference back to component — deleting
// the Component cascade-deletes this XR, and therefore the real GitHub
// repository) and afterwards keeps only the fields Component owns
// (spec.repoName, spec.visibility) in sync, leaving spec.description /
// spec.autoInit and anything else on the XR untouched.
func (r *ComponentReconciler) reconcileGitHubRepository(ctx context.Context, component *platformv1alpha1.Component) (*unstructured.Unstructured, error) {
	log := logf.FromContext(ctx)

	desired, err := buildGitHubRepository(component)
	if err != nil {
		return nil, err
	}
	if err := controllerutil.SetControllerReference(component, desired, r.Scheme); err != nil {
		return nil, err
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(githubRepositoryGVK)
	err = r.Get(ctx, types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}, existing)
	if errors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		log.Info("created GitHubRepository", "githubrepository", desired.GetName())
		return desired, nil
	}
	if apimeta.IsNoMatchError(err) {
		log.Info("GitHubRepository CRD not yet installed, skipping")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	desiredRepoName, _, _ := unstructured.NestedString(desired.Object, "spec", "repoName")
	desiredVisibility, _, _ := unstructured.NestedString(desired.Object, "spec", "visibility")
	existingRepoName, _, _ := unstructured.NestedString(existing.Object, "spec", "repoName")
	existingVisibility, _, _ := unstructured.NestedString(existing.Object, "spec", "visibility")
	ownerRefMissing := !controllerutil.HasControllerReference(existing)

	if existingRepoName != desiredRepoName || existingVisibility != desiredVisibility || ownerRefMissing {
		patch := client.MergeFrom(existing.DeepCopy())
		if err := unstructured.SetNestedField(existing.Object, desiredRepoName, "spec", "repoName"); err != nil {
			return nil, err
		}
		if err := unstructured.SetNestedField(existing.Object, desiredVisibility, "spec", "visibility"); err != nil {
			return nil, err
		}
		existing.SetLabels(desired.GetLabels())
		if err := controllerutil.SetControllerReference(component, existing, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Patch(ctx, existing, patch); err != nil {
			return nil, err
		}
		log.Info("patched GitHubRepository", "githubrepository", existing.GetName())
	}

	return existing, nil
}

// buildGitHubRepository derives the desired GitHubRepository XR from a
// Component. spec.componentRef.name is always component.Name — the
// Component correlation link (PLATFORM_API_ARCHITECTURE.md rule 5) — never
// component.Spec.Repository.Name, which only controls what the repo itself
// is called.
func buildGitHubRepository(component *platformv1alpha1.Component) (*unstructured.Unstructured, error) {
	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(githubRepositoryGVK)
	desired.SetName(repositoryName(component))
	desired.SetNamespace(component.Namespace)
	desired.SetLabels(map[string]string{
		"platform.taskapp.io/component":  component.Name,
		"platform.taskapp.io/owner":      component.Spec.Owner,
		"platform.taskapp.io/managed-by": "component-controller",
	})

	if err := unstructured.SetNestedField(desired.Object, map[string]any{
		"name": component.Name,
	}, "spec", "componentRef"); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedField(desired.Object, repositoryName(component), "spec", "repoName"); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedField(desired.Object, visibilityOrDefault(component), "spec", "visibility"); err != nil {
		return nil, err
	}

	return desired, nil
}

func repositoryName(component *platformv1alpha1.Component) string {
	if component.Spec.Repository.Name != "" {
		return component.Spec.Repository.Name
	}
	return component.Name
}

func visibilityOrDefault(component *platformv1alpha1.Component) string {
	if component.Spec.Repository.Visibility != "" {
		return component.Spec.Repository.Visibility
	}
	return "private"
}

// updateRepositoryStatus reflects the owned GitHubRepository XR's status
// onto component.Status. repo is nil when the GitHubRepository CRD isn't
// installed yet.
func (r *ComponentReconciler) updateRepositoryStatus(component *platformv1alpha1.Component, repo *unstructured.Unstructured) {
	if repo == nil {
		component.Status.Repository = nil
		r.setCondition(component, metav1.Condition{
			Type:               "RepositoryReady",
			Status:             metav1.ConditionFalse,
			Reason:             "GitHubRepositoryCRDNotInstalled",
			Message:            "waiting for the GitHubRepository CRD to be installed",
			ObservedGeneration: component.Generation,
			LastTransitionTime: metav1.Now(),
		})
		r.setReadyCondition(component)
		return
	}

	repoURL, _, _ := unstructured.NestedString(repo.Object, "status", "repoURL")
	status, reason, message := readyCondition(repo)

	component.Status.Repository = &platformv1alpha1.RepositoryStatus{
		Name:  repo.GetName(),
		URL:   repoURL,
		Ready: status == metav1.ConditionTrue,
	}

	r.setCondition(component, metav1.Condition{
		Type:               "RepositoryReady",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: component.Generation,
		LastTransitionTime: metav1.Now(),
	})
	r.setReadyCondition(component)
}

func (r *ComponentReconciler) setReadyCondition(component *platformv1alpha1.Component) {
	status := metav1.ConditionFalse
	reason := "RepositoryNotReady"
	message := "waiting for the owned GitHubRepository to become ready"
	if repoReady := findCondition(component.Status.Conditions, "RepositoryReady"); repoReady != nil && repoReady.Status == metav1.ConditionTrue {
		status = metav1.ConditionTrue
		reason = "RepositoryReady"
		message = "owned GitHubRepository is ready"
	}
	r.setCondition(component, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: component.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

// readyCondition reads the XR's own "Ready" condition (standard Crossplane
// condition type) so a failure reason/message on the child surfaces onto
// Component's own conditions, rather than a generic string.
func readyCondition(obj *unstructured.Unstructured) (metav1.ConditionStatus, string, string) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok || cond["type"] != "Ready" {
			continue
		}
		status := metav1.ConditionFalse
		if cond["status"] == "True" {
			status = metav1.ConditionTrue
		}
		reason, _ := cond["reason"].(string)
		if reason == "" {
			reason = "RepositoryProvisioning"
		}
		message, _ := cond["message"].(string)
		return status, reason, message
	}
	return metav1.ConditionFalse, "RepositoryProvisioning", "waiting for GitHubRepository status"
}

func (r *ComponentReconciler) setCondition(component *platformv1alpha1.Component, cond metav1.Condition) {
	existing := findCondition(component.Status.Conditions, cond.Type)
	if existing == nil {
		component.Status.Conditions = append(component.Status.Conditions, cond)
		return
	}
	if existing.Status != cond.Status {
		existing.LastTransitionTime = metav1.Now()
	}
	existing.Status = cond.Status
	existing.Reason = cond.Reason
	existing.Message = cond.Message
	existing.ObservedGeneration = cond.ObservedGeneration
}

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func (r *ComponentReconciler) patchStatusIfChanged(ctx context.Context, statusBase, component *platformv1alpha1.Component) error {
	if equality.Semantic.DeepEqual(statusBase.Status, component.Status) {
		return nil
	}
	patch := client.MergeFrom(statusBase)
	return client.IgnoreNotFound(r.Status().Patch(ctx, component, patch))
}

// SetupWithManager sets up the controller with the Manager.
func (r *ComponentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	githubRepositoryType := &unstructured.Unstructured{}
	githubRepositoryType.SetGroupVersionKind(githubRepositoryGVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Component{}).
		Watches(
			githubRepositoryType,
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &platformv1alpha1.Component{}),
		).
		Named("component").
		Complete(r)
}
