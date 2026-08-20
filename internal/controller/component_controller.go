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
	"fmt"
	"net/url"
	"strings"

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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

// scaffoldRequestGVK is the scaffold-operator repo's scaffold.taskapp.io/v1alpha1
// ScaffoldRequest. Component's controller creates it once its GitHubRepository
// is ready, but deliberately does not own it — see PLATFORM_API_ARCHITECTURE.md's
// CREATION EXCEPTION: ScaffoldRequest section.
var scaffoldRequestGVK = schema.GroupVersionKind{
	Group:   "scaffold.taskapp.io",
	Version: "v1alpha1",
	Kind:    "ScaffoldRequest",
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
// +kubebuilder:rbac:groups=scaffold.taskapp.io,resources=scaffoldrequests,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=scaffold.taskapp.io,resources=scaffoldrequests/status,verbs=get

// Reconcile creates and keeps in sync the GitHubRepository XR owned by this
// Component, optionally creates a ScaffoldRequest once that repository is
// ready, and reflects both statuses back onto Component.status.
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

	scaffoldReq, err := r.reconcileScaffold(ctx, component, repo)
	if err != nil {
		return ctrl.Result{}, err
	}
	r.updateScaffoldStatus(component, scaffoldReq)

	r.setReadyCondition(component)

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
	// autoInit is always true: GitHub's Git Data API (which scaffold-operator
	// uses to write its scaffold commit) unconditionally rejects writes with
	// 409 "Git Repository is empty" against a repository with zero commits
	// -- confirmed live, not a timing issue. auto_init gives scaffold-operator
	// a valid starting commit to build its one scaffold commit on top of
	// (parent + base_tree); scaffold-operator's own safety check is what
	// recognizes that lone starting commit as the expected baseline rather
	// than unknown content -- see operators/scaffold-operator's RepositoryState.
	if err := unstructured.SetNestedField(desired.Object, true, "spec", "autoInit"); err != nil {
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
}

// reconcileScaffold creates a ScaffoldRequest once spec.scaffold is set and
// the owned GitHubRepository is ready, then never re-enters — no
// drift-correction and no re-creation once Component's own Scaffolded
// condition has gone True, even if the ScaffoldRequest is later deleted
// out-of-band or spec.scaffold changes (per platform-scaffolds' own
// commitment not to auto-migrate an already-scaffolded repo).
func (r *ComponentReconciler) reconcileScaffold(ctx context.Context, component *platformv1alpha1.Component, repo *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	log := logf.FromContext(ctx)

	if component.Spec.Scaffold == nil {
		return nil, nil
	}

	repositoryReady := findCondition(component.Status.Conditions, "RepositoryReady")
	if repositoryReady == nil || repositoryReady.Status != metav1.ConditionTrue {
		return nil, nil
	}

	alreadyScaffolded := false
	if scaffolded := findCondition(component.Status.Conditions, "Scaffolded"); scaffolded != nil && scaffolded.Status == metav1.ConditionTrue {
		alreadyScaffolded = true
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(scaffoldRequestGVK)
	err := r.Get(ctx, types.NamespacedName{Name: component.Name, Namespace: component.Namespace}, existing)
	if apimeta.IsNoMatchError(err) {
		log.Info("ScaffoldRequest CRD not yet installed, skipping")
		return nil, nil
	}
	if errors.IsNotFound(err) {
		if alreadyScaffolded {
			return nil, nil
		}
		desired, buildErr := buildScaffoldRequest(component, repo)
		if buildErr != nil {
			return nil, buildErr
		}
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		log.Info("created ScaffoldRequest", "scaffoldrequest", desired.GetName())
		return desired, nil
	}
	if err != nil {
		return nil, err
	}

	return existing, nil
}

// buildScaffoldRequest resolves componentName/repositoryName/owner/template/
// version once and writes them directly into spec — the ScaffoldRequest is
// self-contained, so the scaffold operator never needs to read Component.
// Deliberately no controllerutil.SetControllerReference call: this is the
// one place in this controller that intentionally does not set an
// ownerReference, so the ScaffoldRequest survives Component deletion as a
// standalone audit record — see PLATFORM_API_ARCHITECTURE.md's CREATION
// EXCEPTION: ScaffoldRequest section.
func buildScaffoldRequest(component *platformv1alpha1.Component, repo *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	owner, err := ownerFromRepository(repo)
	if err != nil {
		return nil, err
	}

	desired := &unstructured.Unstructured{}
	desired.SetGroupVersionKind(scaffoldRequestGVK)
	desired.SetName(component.Name)
	desired.SetNamespace(component.Namespace)
	desired.SetLabels(map[string]string{
		"platform.taskapp.io/component":  component.Name,
		"platform.taskapp.io/managed-by": "component-controller",
	})

	fields := map[string]any{
		"componentRef":   map[string]any{"name": component.Name},
		"componentName":  component.Name,
		"repositoryName": repositoryName(component),
		"owner":          owner,
		"template":       component.Spec.Scaffold.Template,
		"version":        component.Spec.Scaffold.Version,
	}
	for field, value := range fields {
		if err := unstructured.SetNestedField(desired.Object, value, "spec", field); err != nil {
			return nil, err
		}
	}

	return desired, nil
}

// ownerFromRepository extracts the GitHub owner/org from the ready
// GitHubRepository XR's status.repoURL (e.g. https://github.com/entr0pian/foo
// -> entr0pian). The XR's spec carries no explicit owner field — the repo's
// real owner is only known once Crossplane reports back the URL it actually
// created (the org is implicit in the crossplane-github-credentials PAT).
func ownerFromRepository(repo *unstructured.Unstructured) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("GitHubRepository XR not available")
	}
	repoURL, _, _ := unstructured.NestedString(repo.Object, "status", "repoURL")
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("parsing GitHubRepository status.repoURL %q: %w", repoURL, err)
	}
	owner, _, _ := strings.Cut(strings.TrimPrefix(parsed.Path, "/"), "/")
	if owner == "" {
		return "", fmt.Errorf("could not determine owner from GitHubRepository status.repoURL %q", repoURL)
	}
	return owner, nil
}

// updateScaffoldStatus mirrors the created ScaffoldRequest's status onto
// Component.Status.Scaffold and the Scaffolded condition, including
// surfacing a Blocked reason/message from the ScaffoldRequest so `kubectl
// get component` shows why scaffolding stalled rather than just "not done
// yet."
func (r *ComponentReconciler) updateScaffoldStatus(component *platformv1alpha1.Component, req *unstructured.Unstructured) {
	if component.Spec.Scaffold == nil || req == nil {
		return
	}

	status, reason, message := scaffoldCondition(req)

	template, _, _ := unstructured.NestedString(req.Object, "spec", "template")
	version, _, _ := unstructured.NestedString(req.Object, "spec", "version")
	templateRevision, _, _ := unstructured.NestedString(req.Object, "status", "templateRevision")
	commitSHA, _, _ := unstructured.NestedString(req.Object, "status", "commitSHA")

	component.Status.Scaffold = &platformv1alpha1.ScaffoldStatus{
		Template:         template,
		Version:          version,
		TemplateRevision: templateRevision,
		CommitSHA:        commitSHA,
		Completed:        status == metav1.ConditionTrue,
	}

	r.setCondition(component, metav1.Condition{
		Type:               "Scaffolded",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: component.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

// scaffoldCondition reads the ScaffoldRequest's own Completed/Blocked
// condition types (per operators/scaffold-operator's contract) and maps them
// onto Component's single Scaffolded condition.
func scaffoldCondition(req *unstructured.Unstructured) (metav1.ConditionStatus, string, string) {
	conditions, _, _ := unstructured.NestedSlice(req.Object, "status", "conditions")

	var blocked map[string]any
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		switch cond["type"] {
		case "Completed":
			if cond["status"] == "True" {
				message, _ := cond["message"].(string)
				if message == "" {
					message = "scaffold committed to repository"
				}
				return metav1.ConditionTrue, "Completed", message
			}
		case "Blocked":
			if cond["status"] == "True" {
				blocked = cond
			}
		}
	}

	if blocked != nil {
		reason, _ := blocked["reason"].(string)
		if reason == "" {
			reason = "Blocked"
		}
		message, _ := blocked["message"].(string)
		return metav1.ConditionFalse, reason, message
	}

	return metav1.ConditionFalse, "ScaffoldPending", "waiting for scaffold-operator to complete the ScaffoldRequest"
}

// setReadyCondition aggregates RepositoryReady, and — when a scaffold was
// requested — Scaffolded, into the single top-level Ready condition.
func (r *ComponentReconciler) setReadyCondition(component *platformv1alpha1.Component) {
	status := metav1.ConditionFalse
	reason := "RepositoryNotReady"
	message := "waiting for the owned GitHubRepository to become ready"

	repoReady := findCondition(component.Status.Conditions, "RepositoryReady")
	if repoReady != nil && repoReady.Status == metav1.ConditionTrue {
		status = metav1.ConditionTrue
		reason = "RepositoryReady"
		message = "owned GitHubRepository is ready"

		if component.Spec.Scaffold != nil {
			scaffolded := findCondition(component.Status.Conditions, "Scaffolded")
			if scaffolded != nil && scaffolded.Status == metav1.ConditionTrue {
				reason = "Scaffolded"
				message = "owned GitHubRepository is ready and scaffolded"
			} else {
				status = metav1.ConditionFalse
				reason = "ScaffoldNotReady"
				message = "waiting for the requested scaffold to complete"
				if scaffolded != nil {
					reason = scaffolded.Reason
					message = scaffolded.Message
				}
			}
		}
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

	scaffoldRequestType := &unstructured.Unstructured{}
	scaffoldRequestType.SetGroupVersionKind(scaffoldRequestGVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Component{}).
		Watches(
			githubRepositoryType,
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &platformv1alpha1.Component{}),
		).
		Watches(
			scaffoldRequestType,
			handler.EnqueueRequestsFromMapFunc(mapScaffoldRequestToComponent),
		).
		Named("component").
		Complete(r)
}

// mapScaffoldRequestToComponent enqueues the owning Component by reading the
// platform.taskapp.io/component label — ScaffoldRequest has no
// ownerReference to key off (see PLATFORM_API_ARCHITECTURE.md's CREATION
// EXCEPTION: ScaffoldRequest), unlike the GitHubRepository watch above.
func mapScaffoldRequestToComponent(_ context.Context, obj client.Object) []reconcile.Request {
	componentName := obj.GetLabels()["platform.taskapp.io/component"]
	if componentName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      componentName,
			Namespace: obj.GetNamespace(),
		},
	}}
}
