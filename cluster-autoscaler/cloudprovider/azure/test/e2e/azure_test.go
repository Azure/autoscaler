//go:build e2e

/*
Copyright 2024 The Kubernetes Authors.

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

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Azure Provider", func() {
	var (
		namespace *corev1.Namespace
	)

	BeforeEach(func() {
		Eventually(allVMSSStable, "10m", "30s").Should(Succeed())

		namespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "azure-e2e-",
			},
		}
		Expect(k8s.Create(ctx, namespace)).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8s.Delete(ctx, namespace)).To(Succeed())
		Eventually(func() bool {
			err := k8s.Get(ctx, client.ObjectKeyFromObject(namespace), &corev1.Namespace{})
			return apierrors.IsNotFound(err)
		}, "1m", "5s").Should(BeTrue(), "Namespace "+namespace.Name+" still exists")
	})

	It("scales up AKS node pools when pending Pods exist", func() {
		ensureHelmValues(map[string]interface{}{
			"extraArgs": map[string]interface{}{
				"scale-down-delay-after-add":       "10s",
				"scale-down-unneeded-time":         "10s",
				"scale-down-candidates-pool-ratio": "1.0",
				"unremovable-node-recheck-timeout": "10s",
				"skip-nodes-with-system-pods":      "false",
				"skip-nodes-with-local-storage":    "false",
			},
		})

		nodes := &corev1.NodeList{}
		Expect(k8s.List(ctx, nodes)).To(Succeed())
		nodeCountBefore := len(nodes.Items)

		By("Creating 100 Pods")
		// https://raw.githubusercontent.com/kubernetes/website/main/content/en/examples/application/php-apache.yaml
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "php-apache",
				Namespace: namespace.Name,
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"run": "php-apache",
					},
				},
				Replicas: ptr.To[int32](100),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"run": "php-apache",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "php-apache",
								Image: "registry.k8s.io/hpa-example",
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("500m"),
									},
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8s.Create(ctx, deploy)).To(Succeed())

		By("Waiting for more Ready Nodes to exist")
		Eventually(func() (int, error) {
			readyCount := 0
			nodes := &corev1.NodeList{}
			if err := k8s.List(ctx, nodes); err != nil {
				return 0, err
			}
			for _, node := range nodes.Items {
				for _, cond := range node.Status.Conditions {
					if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
						readyCount++
						break
					}
				}
			}
			return readyCount, nil
		}, "10m", "10s").Should(BeNumerically(">", nodeCountBefore))

		Eventually(allVMSSStable, "10m", "30s").Should(Succeed())

		By("Deleting 100 Pods")
		Expect(k8s.Delete(ctx, deploy)).To(Succeed())

		By("Waiting for the original number of Nodes to be Ready")
		Eventually(func(g Gomega) {
			nodes := &corev1.NodeList{}
			g.Expect(k8s.List(ctx, nodes)).To(Succeed())
			g.Expect(nodes.Items).To(SatisfyAll(
				HaveLen(nodeCountBefore),
				ContainElements(Satisfy(func(node corev1.Node) bool {
					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
							return true
						}
					}
					return false
				})),
			))
		}, "20m", "10s").Should(Succeed())
	})

	// TODO: This test requires a deallocate-mode node pool and the ability to induce
	// a start failure. Uncomment and adapt once the test infrastructure supports
	// configuring scale-down-mode on VMSS and simulating capacity errors.
	//
	// Design: After a deallocate-mode node pool scales down (VMs deallocated),
	// trigger a scale-up that causes BeginStart to fail (e.g., constrained SKU).
	// CAS should:
	//   1. Detect the failure via instanceStatusFromVM (InstanceCreating + ErrorInfo)
	//   2. Register a failed scale-up → exponential backoff on the node group
	//   3. NOT delete the deallocated VMs (deleteCreatedNodesWithErrors guard)
	//   4. Emit a ScaleUpFailed Kubernetes event
	//
	// Verification signals:
	//   - K8s events: ScaleUpFailed on the node group
	//   - CAS metrics: failed_scale_ups_total{reason="start-deallocated-failed"} > 0
	//   - VMSS state: deallocated VMs still exist (not deleted)
	//   - Node count: unchanged (no new nodes joined)
	PIt("backs off deallocate-mode node groups on failed VM start", func() {
		ensureHelmValues(map[string]interface{}{
			"extraArgs": map[string]interface{}{
				"scale-down-delay-after-add":       "10s",
				"scale-down-unneeded-time":         "10s",
				"scale-down-candidates-pool-ratio": "1.0",
				"unremovable-node-recheck-timeout": "10s",
				"skip-nodes-with-system-pods":      "false",
				"skip-nodes-with-local-storage":    "false",
			},
		})

		nodes := &corev1.NodeList{}
		Expect(k8s.List(ctx, nodes)).To(Succeed())
		nodeCountBefore := len(nodes.Items)

		By("Creating Pods to trigger scale-up on a deallocate-mode pool")
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deallocate-backoff-test",
				Namespace: namespace.Name,
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "deallocate-backoff-test"},
				},
				Replicas: ptr.To[int32](10),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "deallocate-backoff-test"},
					},
					Spec: corev1.PodSpec{
						// TODO: Add nodeSelector/affinity to target the deallocate-mode pool
						Containers: []corev1.Container{
							{
								Name:  "pause",
								Image: "registry.k8s.io/pause:3.9",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8s.Create(ctx, deploy)).To(Succeed())

		By("Waiting for scale-up and subsequent scale-down (VMs deallocated)")
		Eventually(func() (int, error) {
			readyCount := 0
			nodes := &corev1.NodeList{}
			if err := k8s.List(ctx, nodes); err != nil {
				return 0, err
			}
			for _, node := range nodes.Items {
				for _, cond := range node.Status.Conditions {
					if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
						readyCount++
						break
					}
				}
			}
			return readyCount, nil
		}, "10m", "10s").Should(BeNumerically(">", nodeCountBefore))

		By("Deleting the deployment to trigger scale-down → deallocate")
		Expect(k8s.Delete(ctx, deploy)).To(Succeed())
		Eventually(allVMSSStable, "20m", "30s").Should(Succeed())

		// TODO: At this point, the deallocate-mode VMSS should have deallocated VMs.
		// Now we need to induce a start failure. Options:
		//   Option A: Use Azure SDK to set a policy/quota that prevents VM start
		//   Option B: Use a constrained SKU that's out of capacity
		//   Option C: Corrupt the VM's CSE so it fails on restart
		//
		// Then create new Pods to trigger scale-up (which will try to start deallocated VMs).

		By("Creating Pods to trigger scale-up (restart of deallocated VMs)")
		deploy2 := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "deallocate-backoff-trigger",
				Namespace: namespace.Name,
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "deallocate-backoff-trigger"},
				},
				Replicas: ptr.To[int32](10),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "deallocate-backoff-trigger"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "pause",
								Image: "registry.k8s.io/pause:3.9",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8s.Create(ctx, deploy2)).To(Succeed())

		By("Verifying CAS emits ScaleUpFailed event (backoff triggered)")
		Eventually(func() bool {
			events := &corev1.EventList{}
			if err := k8s.List(ctx, events, client.InNamespace(namespace.Name)); err != nil {
				return false
			}
			for _, event := range events.Items {
				if event.Reason == "ScaleUpFailed" {
					return true
				}
			}
			return false
		}, "15m", "10s").Should(BeTrue(), "Expected ScaleUpFailed event from CAS backoff")

		By("Verifying deallocated VMs were NOT deleted")
		// TODO: Use vmss client to list instances and verify deallocated VMs still exist
		// pager := vmss.NewListPager(resourceGroup, nil)
		// Count instances with PowerState/deallocated — should be > 0

		By("Verifying node count did not increase (backoff prevented new scale-up)")
		nodes = &corev1.NodeList{}
		Expect(k8s.List(ctx, nodes)).To(Succeed())
		Expect(len(nodes.Items)).To(Equal(nodeCountBefore))

		By("Cleanup")
		Expect(k8s.Delete(ctx, deploy2)).To(Succeed())
	})
})
