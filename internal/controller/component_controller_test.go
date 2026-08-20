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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1alpha1 "github.com/entr0pian/component-operator/api/v1alpha1"
)

// newComponent builds a minimal, valid Component for a given name in the
// "default" namespace. Each test uses its own component name so specs don't
// collide — envtest tears the whole suite down at once, so no per-test
// cleanup is required.
func newComponent(name string) *platformv1alpha1.Component {
	return &platformv1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: platformv1alpha1.ComponentSpec{
			Owner: "team-payments",
		},
	}
}

func newComponentWithScaffold(name string) *platformv1alpha1.Component {
	component := newComponent(name)
	component.Spec.Scaffold = &platformv1alpha1.ScaffoldSpec{
		Template: "golang-service",
		Version:  "0.1.0",
	}
	return component
}

func getGitHubRepository(ctx context.Context, name string) *unstructured.Unstructured {
	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(githubRepositoryGVK)
	ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, repo)).To(Succeed())
	return repo
}

func setReadyCondition(ctx context.Context, repo *unstructured.Unstructured, status, reason, message string) {
	Expect(unstructured.SetNestedSlice(repo.Object, []any{
		map[string]any{
			"type":    "Ready",
			"status":  status,
			"reason":  reason,
			"message": message,
		},
	}, "status", "conditions")).To(Succeed())
	Expect(k8sClient.Status().Update(ctx, repo)).To(Succeed())
}

// makeRepositoryReady sets status.repoURL and a True Ready condition on the
// component's owned GitHubRepository — the two things reconcileScaffold
// requires before it will create a ScaffoldRequest.
func makeRepositoryReady(ctx context.Context, name string) {
	repo := getGitHubRepository(ctx, name)
	Expect(unstructured.SetNestedField(repo.Object, "https://github.com/entr0pian/"+name, "status", "repoURL")).To(Succeed())
	setReadyCondition(ctx, repo, "True", "Available", "repository ready")
}

func getScaffoldRequest(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	req := &unstructured.Unstructured{}
	req.SetGroupVersionKind(scaffoldRequestGVK)
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, req)
	return req, err
}

func setScaffoldCompleted(ctx context.Context, req *unstructured.Unstructured, templateRevision, commitSHA string) {
	Expect(unstructured.SetNestedSlice(req.Object, []any{
		map[string]any{
			"type":    "Completed",
			"status":  "True",
			"reason":  "Completed",
			"message": "scaffold committed to repository",
		},
	}, "status", "conditions")).To(Succeed())
	Expect(unstructured.SetNestedField(req.Object, templateRevision, "status", "templateRevision")).To(Succeed())
	Expect(unstructured.SetNestedField(req.Object, commitSHA, "status", "commitSHA")).To(Succeed())
	Expect(k8sClient.Status().Update(ctx, req)).To(Succeed())
}

var _ = Describe("Component Controller", func() {
	ctx := context.Background()

	reconcileComponent := func(name string) {
		r := &ComponentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	Context("Create", func() {
		It("creates a GitHubRepository owned by the Component", func() {
			const name = "payments-create"
			Expect(k8sClient.Create(ctx, newComponent(name))).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponent(name))).To(Succeed())
			})

			reconcileComponent(name)

			repo := getGitHubRepository(ctx, name)
			repoName, _, _ := unstructured.NestedString(repo.Object, "spec", "repoName")
			Expect(repoName).To(Equal(name))

			visibility, _, _ := unstructured.NestedString(repo.Object, "spec", "visibility")
			Expect(visibility).To(Equal("private"))

			componentRefName, _, _ := unstructured.NestedString(repo.Object, "spec", "componentRef", "name")
			Expect(componentRefName).To(Equal(name))

			autoInit, _, _ := unstructured.NestedBool(repo.Object, "spec", "autoInit")
			Expect(autoInit).To(BeTrue())

			Expect(repo.GetLabels()).To(HaveKeyWithValue("platform.taskapp.io/component", name))
			Expect(repo.GetLabels()).To(HaveKeyWithValue("platform.taskapp.io/owner", "team-payments"))
			Expect(repo.GetLabels()).To(HaveKeyWithValue("platform.taskapp.io/managed-by", "component-controller"))

			owner := metav1.GetControllerOf(repo)
			Expect(owner).NotTo(BeNil())
			Expect(owner.Kind).To(Equal("Component"))
			Expect(owner.Name).To(Equal(name))
		})
	})

	Context("Idempotency", func() {
		It("issues no update when nothing has changed", func() {
			const name = "payments-idempotent"
			Expect(k8sClient.Create(ctx, newComponent(name))).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponent(name))).To(Succeed())
			})

			reconcileComponent(name)
			resourceVersionAfterCreate := getGitHubRepository(ctx, name).GetResourceVersion()

			reconcileComponent(name)
			resourceVersionAfterSecondReconcile := getGitHubRepository(ctx, name).GetResourceVersion()

			Expect(resourceVersionAfterSecondReconcile).To(Equal(resourceVersionAfterCreate))
		})
	})

	Context("Spec update propagation", func() {
		It("patches only repoName/visibility on the owned GitHubRepository", func() {
			const name = "payments-update"
			component := newComponent(name)
			Expect(k8sClient.Create(ctx, component)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponent(name))).To(Succeed())
			})

			reconcileComponent(name)
			Expect(getGitHubRepository(ctx, name)).NotTo(BeNil())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, component)).To(Succeed())
			component.Spec.Repository.Visibility = "internal"
			Expect(k8sClient.Update(ctx, component)).To(Succeed())

			reconcileComponent(name)

			repo := getGitHubRepository(ctx, name)
			visibility, _, _ := unstructured.NestedString(repo.Object, "spec", "visibility")
			Expect(visibility).To(Equal("internal"))
			repoName, _, _ := unstructured.NestedString(repo.Object, "spec", "repoName")
			Expect(repoName).To(Equal(name))
		})
	})

	Context("Child readiness", func() {
		It("reflects a ready GitHubRepository onto Component.status", func() {
			const name = "payments-ready"
			Expect(k8sClient.Create(ctx, newComponent(name))).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponent(name))).To(Succeed())
			})

			reconcileComponent(name)

			repo := getGitHubRepository(ctx, name)
			Expect(unstructured.SetNestedField(repo.Object, "https://github.com/entr0pian/payments-ready", "status", "repoURL")).To(Succeed())
			setReadyCondition(ctx, repo, "True", "Available", "repository ready")

			reconcileComponent(name)

			component := &platformv1alpha1.Component{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, component)).To(Succeed())

			Expect(component.Status.Repository).NotTo(BeNil())
			Expect(component.Status.Repository.Ready).To(BeTrue())
			Expect(component.Status.Repository.URL).To(Equal("https://github.com/entr0pian/payments-ready"))

			repoReady := findCondition(component.Status.Conditions, "RepositoryReady")
			Expect(repoReady).NotTo(BeNil())
			Expect(repoReady.Status).To(Equal(metav1.ConditionTrue))

			ready := findCondition(component.Status.Conditions, "Ready")
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("Child failure", func() {
		It("surfaces the GitHubRepository's failure reason/message onto Component", func() {
			const name = "payments-fail"
			Expect(k8sClient.Create(ctx, newComponent(name))).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponent(name))).To(Succeed())
			})

			reconcileComponent(name)

			repo := getGitHubRepository(ctx, name)
			setReadyCondition(ctx, repo, "False", "ProviderRateLimited", "GitHub API rate limit exceeded")

			reconcileComponent(name)

			component := &platformv1alpha1.Component{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, component)).To(Succeed())

			repoReady := findCondition(component.Status.Conditions, "RepositoryReady")
			Expect(repoReady).NotTo(BeNil())
			Expect(repoReady.Status).To(Equal(metav1.ConditionFalse))
			Expect(repoReady.Reason).To(Equal("ProviderRateLimited"))
			Expect(repoReady.Message).To(Equal("GitHub API rate limit exceeded"))

			ready := findCondition(component.Status.Conditions, "Ready")
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		})
	})

	Context("Cascade delete", func() {
		// envtest does not run the Kubernetes garbage collector, so deleting
		// the Component here will not actually cascade-delete the owned
		// GitHubRepository within this test. What we can and do verify is the
		// one property that makes that cascade happen in a real cluster: a
		// correctly set controller ownerReference from GitHubRepository back
		// to Component (see PLATFORM_API_ARCHITECTURE.md's OWNERSHIP
		// EXCEPTION section).
		It("sets a controller ownerReference enabling cascade deletion", func() {
			const name = "payments-cascade"
			component := newComponent(name)
			Expect(k8sClient.Create(ctx, component)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponent(name))).To(Succeed())
			})

			reconcileComponent(name)

			repo := getGitHubRepository(ctx, name)
			owner := metav1.GetControllerOf(repo)
			Expect(owner).NotTo(BeNil())
			Expect(owner.UID).To(Equal(component.UID))
			Expect(owner.Kind).To(Equal("Component"))
			Expect(owner.APIVersion).To(Equal(platformv1alpha1.GroupVersion.String()))
		})
	})

	Context("Scaffold", func() {
		It("does not create a ScaffoldRequest before the GitHubRepository is ready", func() {
			const name = "payments-scaffold-not-ready"
			Expect(k8sClient.Create(ctx, newComponentWithScaffold(name))).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponentWithScaffold(name))).To(Succeed())
			})

			reconcileComponent(name)

			_, err := getScaffoldRequest(ctx, name)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("creates a ScaffoldRequest with the resolved fields once the GitHubRepository is ready", func() {
			const name = "payments-scaffold-create"
			Expect(k8sClient.Create(ctx, newComponentWithScaffold(name))).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponentWithScaffold(name))).To(Succeed())
			})

			reconcileComponent(name)
			makeRepositoryReady(ctx, name)
			reconcileComponent(name)

			req, err := getScaffoldRequest(ctx, name)
			Expect(err).NotTo(HaveOccurred())

			componentRefName, _, _ := unstructured.NestedString(req.Object, "spec", "componentRef", "name")
			Expect(componentRefName).To(Equal(name))
			componentName, _, _ := unstructured.NestedString(req.Object, "spec", "componentName")
			Expect(componentName).To(Equal(name))
			repositoryName, _, _ := unstructured.NestedString(req.Object, "spec", "repositoryName")
			Expect(repositoryName).To(Equal(name))
			owner, _, _ := unstructured.NestedString(req.Object, "spec", "owner")
			Expect(owner).To(Equal("entr0pian"))
			template, _, _ := unstructured.NestedString(req.Object, "spec", "template")
			Expect(template).To(Equal("golang-service"))
			version, _, _ := unstructured.NestedString(req.Object, "spec", "version")
			Expect(version).To(Equal("0.1.0"))

			Expect(req.GetLabels()).To(HaveKeyWithValue("platform.taskapp.io/component", name))

			// No ownerReference — see PLATFORM_API_ARCHITECTURE.md's
			// CREATION EXCEPTION: ScaffoldRequest section.
			Expect(metav1.GetControllerOf(req)).To(BeNil())

			component := &platformv1alpha1.Component{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, component)).To(Succeed())
			scaffolded := findCondition(component.Status.Conditions, "Scaffolded")
			Expect(scaffolded).NotTo(BeNil())
			Expect(scaffolded.Status).To(Equal(metav1.ConditionFalse))
			Expect(scaffolded.Reason).To(Equal("ScaffoldPending"))

			ready := findCondition(component.Status.Conditions, "Ready")
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal("ScaffoldPending"))
		})

		It("mirrors a completed ScaffoldRequest onto Component.status and never re-enters afterwards", func() {
			const name = "payments-scaffold-complete"
			Expect(k8sClient.Create(ctx, newComponentWithScaffold(name))).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, newComponentWithScaffold(name))).To(Succeed())
			})

			reconcileComponent(name)
			makeRepositoryReady(ctx, name)
			reconcileComponent(name)

			req, err := getScaffoldRequest(ctx, name)
			Expect(err).NotTo(HaveOccurred())
			setScaffoldCompleted(ctx, req, "abc1234", "def5678")

			reconcileComponent(name)

			component := &platformv1alpha1.Component{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, component)).To(Succeed())

			Expect(component.Status.Scaffold).NotTo(BeNil())
			Expect(component.Status.Scaffold.Completed).To(BeTrue())
			Expect(component.Status.Scaffold.TemplateRevision).To(Equal("abc1234"))
			Expect(component.Status.Scaffold.CommitSHA).To(Equal("def5678"))

			scaffolded := findCondition(component.Status.Conditions, "Scaffolded")
			Expect(scaffolded).NotTo(BeNil())
			Expect(scaffolded.Status).To(Equal(metav1.ConditionTrue))

			ready := findCondition(component.Status.Conditions, "Ready")
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal("Scaffolded"))

			// Hard stop: deleting the ScaffoldRequest out-of-band after
			// completion must not cause it to be recreated on a later
			// reconcile, and the previously-mirrored status must survive.
			Expect(k8sClient.Delete(ctx, req)).To(Succeed())

			reconcileComponent(name)

			_, err = getScaffoldRequest(ctx, name)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, component)).To(Succeed())
			Expect(component.Status.Scaffold).NotTo(BeNil())
			Expect(component.Status.Scaffold.Completed).To(BeTrue())
			Expect(component.Status.Scaffold.CommitSHA).To(Equal("def5678"))
			scaffolded = findCondition(component.Status.Conditions, "Scaffolded")
			Expect(scaffolded).NotTo(BeNil())
			Expect(scaffolded.Status).To(Equal(metav1.ConditionTrue))
		})
	})
})
