/*
Copyright 2018 Pusher Ltd.

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

package gittrackobject

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	farosv1alpha1 "github.com/pusher/faros/pkg/apis/faros/v1alpha1"
	"github.com/pusher/faros/pkg/controller/gittrackobject/metrics"
	gittrackobjectutils "github.com/pusher/faros/pkg/controller/gittrackobject/utils"
	farosflags "github.com/pusher/faros/pkg/flags"
	farosclient "github.com/pusher/faros/pkg/utils/client"
	testutils "github.com/pusher/faros/test/utils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = FDescribe("GitTrackObject Suite", func() {
	var c client.Client
	var m testutils.Matcher
	var r *ReconcileGitTrackObject
	var mgr manager.Manager

	var stop chan struct{}
	var stopInformers chan struct{}

	var expectedRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}}
	var expectedClusterRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "example"}}
	var gitTrack *farosv1alpha1.GitTrack
	var requests chan reconcile.Request
	var testEvents chan TestEvent

	const timeout = time.Second * 5
	const consistentlyTimeout = time.Second

	BeforeEach(func() {
		// Setup the Manager and Controller.  Wrap the Controller Reconcile function so it writes each request to a
		// channel when it is finished.
		var err error
		cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
		mgr, err = manager.New(cfg, manager.Options{
			Namespace:          farosflags.Namespace,
			MetricsBindAddress: "0", // Disable serving metrics while testing
		})
		Expect(err).NotTo(HaveOccurred())

		applier, err := farosclient.NewApplier(cfg, farosclient.Options{})
		Expect(err).NotTo(HaveOccurred())

		c = mgr.GetClient()
		m = testutils.Matcher{Client: mgr.GetClient(), FarosClient: applier}

		recFn := newReconciler(mgr)
		r = recFn.(*ReconcileGitTrackObject)
		recFn, testEvents = SetupTestEventRecorder(recFn)
		recFn, requests = SetupTestReconcile(recFn)
		Expect(add(mgr, recFn)).NotTo(HaveOccurred())

		stopInformers = r.StopChan()
		stop = StartTestManager(mgr)

		// Create a GitTrack to own the ClusterGitTrackObjects
		// The Reconciler wont reconcile CGTOs that aren't owned by the a GT in their
		// namespace
		gitTrack = &farosv1alpha1.GitTrack{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "testgittrack",
				Namespace: "default",
			},
			Spec: farosv1alpha1.GitTrackSpec{
				Reference:  "foo",
				Repository: "bar",
			},
		}
		m.Create(gitTrack).Should(Succeed())

		// Reset all metrics before each test
		metrics.InSync.Reset()
	})

	AfterEach(func() {
		// Stop Controller and informers before cleaning up
		close(stop)
		close(stopInformers)
		// Clean up all resources as GC is disabled in the control plane
		testutils.DeleteAll(cfg, timeout,
			&farosv1alpha1.GitTrackList{},
			&farosv1alpha1.GitTrackObjectList{},
			&farosv1alpha1.ClusterGitTrackObjectList{},
			&appsv1.DeploymentList{},
			&rbacv1.ClusterRoleBindingList{},
			&corev1.EventList{},
		)
	})

	Context("ReconcileGitTrackObject", func() {
		Context("with a GitTrackObject", func() {
			var gto *farosv1alpha1.GitTrackObject
			var child *appsv1.Deployment

			BeforeEach(func() {
				gto = testutils.ExampleGitTrackObject.DeepCopy()
				gto.SetOwnerReferences([]metav1.OwnerReference{
					{
						APIVersion: "faros.pusher.com/v1alpha1",
						Kind:       "GitTrack",
						UID:        gitTrack.UID,
						Name:       gitTrack.Name,
					},
				})
				child = testutils.ExampleDeployment.DeepCopy()
				Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, child)).To(Succeed())
			})

			Context("with valid data", func() {
				BeforeEach(func() {
					// Create and fetch the instance to make sure caches are synced
					m.Create(gto).Should(Succeed())
					// Wait twice for the extra reconcile for status updates
					Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
					Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
					m.Get(gto, timeout).Should(Succeed())
				})

				It("should create the child resource", func() {
					m.Get(child, timeout).Should(Succeed())
				})

				It("should add an owner reference to the child", func() {
					m.Get(child, timeout).Should(Succeed())
					m.Eventually(child, timeout).
						Should(testutils.WithOwnerReferences(ContainElement(testutils.GetGitTrackObjectOwnerRef(gto))))
				})

				It("should add a last applied annotation to the child", func() {
					m.Get(child, timeout).Should(Succeed())
					m.Eventually(child, timeout).
						Should(testutils.WithAnnotations(HaveKey(farosclient.LastAppliedAnnotation)))
				})

				Context("when the child has the update strategy", func() {
					var originalVersion string
					var originalUID types.UID

					BeforeEach(func() {
						child.Spec.Template.SetAnnotations(map[string]string{"updated": "annotations"})
						child.SetOwnerReferences([]metav1.OwnerReference{testutils.GetGitTrackObjectOwnerRef(gto)})
						m.Update(child).Should(Succeed())
						Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
						m.Get(child, timeout).Should(Succeed())

						originalVersion = child.GetResourceVersion()
						originalUID = child.GetUID()
					})

					Context("update", func() {
						BeforeEach(func() {
							specData := testutils.ExampleDeployment.DeepCopy()
							annotations := map[string]string{"faros.pusher.com/update-strategy": string(gittrackobjectutils.DefaultUpdateStrategy)}
							specData.SetAnnotations(annotations)
							Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

							m.Update(gto, timeout).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
						})

						It("should update the child", func() {
							m.Eventually(child, timeout).Should(testutils.WithResourceVersion(Not(Equal(originalVersion))))
						})

						It("should not replace the child", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithUID(Equal(originalUID)))
						})
					})

					Context("never", func() {
						BeforeEach(func() {
							specData := testutils.ExampleDeployment.DeepCopy()
							annotations := map[string]string{"faros.pusher.com/update-strategy": string(gittrackobjectutils.NeverUpdateStrategy)}
							specData.SetAnnotations(annotations)
							Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

							m.Update(gto, timeout).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
						})

						It("should not update the child", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithPodTemplateAnnotations(HaveKeyWithValue("updated", "annotations")))
						})

						It("should not replace the child", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithUID(Equal(originalUID)))
						})
					})

					Context("recreate", func() {
						Context("without conflicts", func() {
							BeforeEach(func() {
								specData := testutils.ExampleDeployment.DeepCopy()
								annotations := map[string]string{"faros.pusher.com/update-strategy": string(gittrackobjectutils.RecreateUpdateStrategy)}
								specData.SetAnnotations(annotations)
								Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

								m.Update(gto, timeout).Should(Succeed())
								Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
							})

							It("should update the child", func() {
								m.Eventually(child, timeout).Should(testutils.WithResourceVersion(Not(Equal(originalVersion))))
							})

							It("should not replace the child", func() {
								m.Consistently(child, consistentlyTimeout).Should(testutils.WithUID(Equal(originalUID)))
							})
						})
					})
				})

				Context("when no errors occur", func() {
					It("should update the status", func() {
						m.Eventually(gto).Should(
							testutils.WithGitTrackObjectStatusConditions(
								ContainElement(
									SatisfyAll(
										testutils.WithGitTrackObjectConditionType(Equal(farosv1alpha1.ObjectInSyncType)),
										testutils.WithGitTrackObjectConditionStatus(Equal(corev1.ConditionTrue)),
										testutils.WithGitTrackObjectConditionReason(Equal(string(gittrackobjectutils.ChildAppliedSuccess))),
										testutils.WithGitTrackObjectConditionMessage(Equal("")),
									),
								),
							),
						)
					})

					It("should update the in-sync metric", func() {
						gauge, err := GetGauge(metrics.InSync, gto)
						Expect(err).NotTo(HaveOccurred())
						Expect(gauge.GetValue()).To(Equal(1.0))
					})
				})

				Context("when the GitTrackObject is updated", func() {
					BeforeEach(func() {
						// Make sure the first reconcile has happened
						m.Get(child, timeout).Should(Succeed())
					})

					Context("and the child spec is updated", func() {
						BeforeEach(func() {
							// Make an update to the spec
							specData := testutils.ExampleDeployment.DeepCopy()
							annotations := map[string]string{"updated": "annotations"}
							specData.Spec.Template.SetAnnotations(annotations)
							Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

							m.Update(gto).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
						})

						It("should update the child resource", func() {
							m.Eventually(child, timeout).Should(testutils.WithPodTemplateAnnotations(HaveKeyWithValue("updated", "annotations")))
						})
					})

					Context("and the object metdata is updated", func() {
						var originalVersion string

						BeforeEach(func() {
							originalVersion = child.GetResourceVersion()

							// Make an update to the GTO metadata
							annotations := map[string]string{"updated": "annotations"}
							gto.SetAnnotations(annotations)

							m.Update(gto).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
						})

						It("should not update the child resource", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithResourceVersion(Equal(originalVersion)))
						})
					})
				})

				Context("if a child resource is deleted", func() {
					var originalUID types.UID

					BeforeEach(func() {
						// Wait for reconcile from child create before deleting
						Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))

						m.Get(child, timeout).Should(Succeed())
						originalUID = child.GetUID()
						m.Delete(child).Should(Succeed())
					})

					It("should be recreated", func() {
						Eventually(func() error {
							key := types.NamespacedName{Namespace: child.GetNamespace(), Name: child.GetName()}
							err := c.Get(context.TODO(), key, child)
							if err != nil {
								return err
							}
							if child.GetUID() == originalUID {
								return fmt.Errorf("child not yet recreated")
							}
							return nil
						}).Should(Succeed())
					})
				})

				Context("should send events", func() {
					var events *corev1.EventList
					BeforeEach(func() {
						events = &corev1.EventList{}
						m.Eventually(events, timeout).ShouldNot(testutils.WithItems(BeEmpty()))
					})

					It("for attempting to create the child", func() {
						m.Eventually(events, timeout).Should(testutils.WithItems(ContainElement(
							SatisfyAll(
								testutils.WithReason(Equal("CreateStarted")),
								testutils.WithInvolvedObjectKind(Equal(gto.GetObjectKind().GroupVersionKind().Kind)),
								testutils.WithInvolvedObjectName(Equal(gto.GetName())),
								testutils.WithInvolvedObjectNamespace(Equal(gto.GetNamespace())),
								testutils.WithEventType(Equal(string(corev1.EventTypeNormal))),
							),
						)))
					})

					It("for successfully to creating the child", func() {
						m.Eventually(events, timeout).Should(testutils.WithItems(ContainElement(
							SatisfyAll(
								testutils.WithReason(Equal("CreateSuccessful")),
								testutils.WithInvolvedObjectKind(Equal(gto.GetObjectKind().GroupVersionKind().Kind)),
								testutils.WithInvolvedObjectName(Equal(gto.GetName())),
								testutils.WithInvolvedObjectNamespace(Equal(gto.GetNamespace())),
								testutils.WithEventType(Equal(string(corev1.EventTypeNormal))),
							),
						)))
					})

					It("to the namespace the controller is restricted to", func() {
						for range events.Items {
							event := <-testEvents
							Expect(event.Namespace).To(Equal(farosflags.Namespace))
						}
					})
				})

				Context("if the child spec is modified", func() {
					Context("in a conflicting manner", func() {
						BeforeEach(func() {
							Expect(child.Spec.Template.Spec.Containers).To(HaveLen(1))
							child.Spec.Template.Spec.Containers[0].Image = "nginx:latest"
							m.Update(child).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
							m.Get(child, timeout).Should(Succeed())
						})

						It("should reset the child to the desired state", func() {
							m.Eventually(child, timeout).
								Should(testutils.WithContainers(SatisfyAll(
									ContainElement(testutils.WithImage(Equal("nginx"))),
									Not(ContainElement(testutils.WithImage(Equal("nginx:latest")))),
								)))
						})
					})

					Context("in a non-conflicting manner", func() {
						BeforeEach(func() {
							labels := child.Spec.Template.GetLabels()
							Expect(labels).ToNot(HaveKey("extra"))
							labels["extra"] = "label"
							child.Spec.Template.SetLabels(labels)
							m.Update(child).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
							m.Get(child, timeout).Should(Succeed())
						})

						It("should not reset the child", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithPodTemplateLabels(HaveKeyWithValue("extra", "label")))
						})
					})
				})

				Context("if the child meta is modified", func() {
					BeforeEach(func() {

					})

					It("should reset the child to the desired state", func() {

					})
				})
			})

			Context("with invalid data", func() {
				BeforeEach(func() {
					// Break the JSON data
					gto.Spec.Data = gto.Spec.Data[10:]

					// Create and fetch the instance to make sure caches are synced
					m.Create(gto).Should(Succeed())
					// Wait twice for the extra reconcile for status updates
					Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
					Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))
					m.Get(gto, timeout).Should(Succeed())
				})

				Context("should update the status", func() {
					It("to represent the failure", func() {
						m.Eventually(gto, timeout).Should(
							testutils.WithGitTrackObjectStatusConditions(
								ContainElement(
									SatisfyAll(
										testutils.WithGitTrackObjectConditionType(Equal(farosv1alpha1.ObjectInSyncType)),
										testutils.WithGitTrackObjectConditionStatus(Equal(corev1.ConditionFalse)),
										testutils.WithGitTrackObjectConditionReason(Equal(string(gittrackobjectutils.ErrorUnmarshallingData))),
									),
								),
							),
						)
					})
				})

				Context("should send an event", func() {
					var events *corev1.EventList
					BeforeEach(func() {
						events = &corev1.EventList{}
						m.Eventually(events, timeout).ShouldNot(testutils.WithItems(BeEmpty()))
					})

					It("to represent the failure", func() {
						m.Eventually(events, timeout).Should(testutils.WithItems(ContainElement(
							SatisfyAll(
								testutils.WithReason(Equal("UnmarshalFailed")),
								testutils.WithInvolvedObjectKind(Equal(gto.GetObjectKind().GroupVersionKind().Kind)),
								testutils.WithInvolvedObjectName(Equal(gto.GetName())),
								testutils.WithInvolvedObjectNamespace(Equal(gto.GetNamespace())),
								testutils.WithEventType(Equal(string(corev1.EventTypeWarning))),
							),
						)))
					})
				})
			})

			Context("in a different namespace", func() {
				var ns *corev1.Namespace

				BeforeEach(func() {
					ns = &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "not-default",
						},
					}
					m.Create(ns)

					gto.SetNamespace("not-default")
					m.Create(gto).Should(Succeed())
				})

				AfterEach(func() {
					m.Delete(gto).Should(Succeed())
					m.Get(gto, timeout).ShouldNot(Succeed())
				})

				It("should not be reconciled", func() {
					Consistently(requests, consistentlyTimeout).ShouldNot(Receive(Equal(expectedRequest)))
				})
			})
		})

		Context("with a ClusterGitTrackObject", func() {
			var gto *farosv1alpha1.ClusterGitTrackObject
			var child *rbacv1.ClusterRoleBinding

			BeforeEach(func() {
				gto = testutils.ExampleClusterGitTrackObject.DeepCopy()
				gto.SetOwnerReferences([]metav1.OwnerReference{
					{
						APIVersion: "faros.pusher.com/v1alpha1",
						Kind:       "GitTrack",
						UID:        gitTrack.UID,
						Name:       gitTrack.Name,
					},
				})
				child = testutils.ExampleClusterRoleBinding.DeepCopy()
				Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, child)).To(Succeed())
			})

			Context("with valid data", func() {
				BeforeEach(func() {
					// Create and fetch the instance to make sure caches are synced
					m.Create(gto).Should(Succeed())
					Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
					m.Get(gto, timeout).Should(Succeed())
				})

				It("should create the child resource", func() {
					m.Get(child, timeout).Should(Succeed())
				})

				It("should add an owner reference to the child", func() {
					m.Get(child, timeout).Should(Succeed())
					m.Eventually(child, timeout).
						Should(testutils.WithOwnerReferences(ContainElement(testutils.GetClusterGitTrackObjectOwnerRef(gto))))
				})

				It("should add a last applied annotation to the child", func() {
					m.Get(child, timeout).Should(Succeed())
					m.Eventually(child, timeout).
						Should(testutils.WithAnnotations(HaveKey(farosclient.LastAppliedAnnotation)))
				})

				Context("when the child has the update strategy", func() {
					var originalVersion string
					var originalUID types.UID

					BeforeEach(func() {
						child.SetAnnotations(map[string]string{"updated": "annotations"})
						child.SetOwnerReferences([]metav1.OwnerReference{testutils.GetClusterGitTrackObjectOwnerRef(gto)})
						m.Update(child).Should(Succeed())
						Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
						m.Get(child, timeout).Should(Succeed())

						originalVersion = child.GetResourceVersion()
						originalUID = child.GetUID()
					})

					Context("update", func() {
						BeforeEach(func() {
							specData := testutils.ExampleClusterRoleBinding.DeepCopy()
							annotations := map[string]string{"faros.pusher.com/update-strategy": string(gittrackobjectutils.DefaultUpdateStrategy)}
							specData.SetAnnotations(annotations)
							Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

							m.Update(gto, timeout).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
						})

						It("should update the child", func() {
							m.Eventually(child, timeout).Should(testutils.WithResourceVersion(Not(Equal(originalVersion))))
						})

						It("should not replace the child", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithUID(Equal(originalUID)))
						})
					})

					Context("never", func() {
						BeforeEach(func() {
							specData := testutils.ExampleClusterRoleBinding.DeepCopy()
							annotations := map[string]string{"faros.pusher.com/update-strategy": string(gittrackobjectutils.NeverUpdateStrategy)}
							specData.Subjects = []rbacv1.Subject{}
							specData.SetAnnotations(annotations)
							Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

							m.Update(gto, timeout).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
						})

						It("should not update the child", func() {
							m.Consistently(child, consistentlyTimeout).ShouldNot(testutils.WithSubjects(BeEmpty()))
						})

						It("should not replace the child", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithUID(Equal(originalUID)))
						})
					})

					Context("recreate", func() {
						Context("with conflicts", func() {
							BeforeEach(func() {
								specData := testutils.ExampleClusterRoleBinding.DeepCopy()
								// Create a conflict (this field is immutable)
								specData.RoleRef.Name = "changed"
								annotations := map[string]string{"faros.pusher.com/update-strategy": string(gittrackobjectutils.RecreateUpdateStrategy)}
								specData.SetAnnotations(annotations)
								Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

								go func() {
									defer GinkgoRecover()
									// We are expecting a delete but we have no GC so have to do it manually
									m.Eventually(child, timeout).Should(testutils.WithFinalizers(ContainElement("foregroundDeletion")))
									child.SetFinalizers([]string{})
									m.Update(child).Should(Succeed())
									m.Get(child, timeout).ShouldNot(Succeed())
								}()

								m.Update(gto, timeout).Should(Succeed())
								Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
							})

							It("should replace the child", func() {
								m.Get(child, timeout).ShouldNot(Succeed())
								m.Get(child, timeout).Should(Succeed())
								m.Eventually(child, timeout).Should(testutils.WithUID(Not(Equal(originalUID))))
							})
						})
					})
				})

				Context("when the ClusterGitTrackObject is updated", func() {
					BeforeEach(func() {
						// Make sure the first reconcile has happened
						m.Get(child, timeout).Should(Succeed())
					})

					Context("and the child spec is updated", func() {
						BeforeEach(func() {
							// Make an update to the spec
							specData := testutils.ExampleClusterRoleBinding.DeepCopy()
							annotations := map[string]string{"updated": "annotations"}
							specData.SetAnnotations(annotations)
							Expect(testutils.SetGitTrackObjectInterfaceSpec(gto, specData)).To(Succeed())

							m.Update(gto).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
						})

						It("should update the child resource", func() {
							m.Eventually(child, timeout).Should(testutils.WithAnnotations(HaveKeyWithValue("updated", "annotations")))
						})
					})

					Context("and the object metdata is updated", func() {
						var originalVersion string

						BeforeEach(func() {
							originalVersion = child.GetResourceVersion()

							// Make an update to the GTO metadata
							annotations := map[string]string{"updated": "annotations"}
							gto.SetAnnotations(annotations)

							m.Update(gto).Should(Succeed())
							Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
						})

						It("should not update the child resource", func() {
							m.Consistently(child, consistentlyTimeout).Should(testutils.WithResourceVersion(Equal(originalVersion)))
						})
					})
				})

				Context("if a child resource is deleted", func() {
					var originalUID types.UID

					BeforeEach(func() {
						// Wait for reconcile from child create before deleting
						Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))

						m.Get(child, timeout).Should(Succeed())
						originalUID = child.GetUID()
						m.Delete(child).Should(Succeed())
					})

					It("should be recreated", func() {
						Eventually(func() error {
							key := types.NamespacedName{Namespace: child.GetNamespace(), Name: child.GetName()}
							err := c.Get(context.TODO(), key, child)
							if err != nil {
								return err
							}
							if child.GetUID() == originalUID {
								return fmt.Errorf("child not yet recreated")
							}
							return nil
						}).Should(Succeed())
					})
				})

				Context("should send events", func() {
					var events *corev1.EventList
					BeforeEach(func() {
						events = &corev1.EventList{}
						m.Eventually(events, timeout).ShouldNot(testutils.WithItems(BeEmpty()))
					})

					It("for attempting to create the child", func() {
						m.Eventually(events, timeout).Should(testutils.WithItems(ContainElement(
							SatisfyAll(
								testutils.WithReason(Equal("CreateStarted")),
								testutils.WithInvolvedObjectKind(Equal(gto.GetObjectKind().GroupVersionKind().Kind)),
								testutils.WithInvolvedObjectName(Equal(gto.GetName())),
								testutils.WithEventType(Equal(string(corev1.EventTypeNormal))),
							),
						)))
					})

					It("for successfully to creating the child", func() {
						m.Eventually(events, timeout).Should(testutils.WithItems(ContainElement(
							SatisfyAll(
								testutils.WithReason(Equal("CreateSuccessful")),
								testutils.WithInvolvedObjectKind(Equal(gto.GetObjectKind().GroupVersionKind().Kind)),
								testutils.WithInvolvedObjectName(Equal(gto.GetName())),
								testutils.WithEventType(Equal(string(corev1.EventTypeNormal))),
							),
						)))
					})

					It("to the namespace the controller is restricted to", func() {
						for range events.Items {
							event := <-testEvents
							Expect(event.Namespace).To(Equal(farosflags.Namespace))
						}
					})
				})
			})

			Context("with invalid data", func() {
				BeforeEach(func() {
					// Break the JSON data
					gto.Spec.Data = gto.Spec.Data[10:]

					// Create and fetch the instance to make sure caches are synced
					m.Create(gto).Should(Succeed())
					// Wait twice for the extra reconcile for status updates
					Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
					Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
					m.Get(gto, timeout).Should(Succeed())
				})

				Context("should update the status", func() {
					It("to represent the failure", func() {
						m.Eventually(gto, timeout).Should(
							testutils.WithGitTrackObjectStatusConditions(
								ContainElement(
									SatisfyAll(
										testutils.WithGitTrackObjectConditionType(Equal(farosv1alpha1.ObjectInSyncType)),
										testutils.WithGitTrackObjectConditionStatus(Equal(corev1.ConditionFalse)),
										testutils.WithGitTrackObjectConditionReason(Equal(string(gittrackobjectutils.ErrorUnmarshallingData))),
									),
								),
							),
						)
					})
				})

				Context("should send an event", func() {
					var events *corev1.EventList
					BeforeEach(func() {
						events = &corev1.EventList{}
						m.Eventually(events, timeout).ShouldNot(testutils.WithItems(BeEmpty()))
					})

					It("to represent the failure", func() {
						m.Eventually(events, timeout).Should(testutils.WithItems(ContainElement(
							SatisfyAll(
								testutils.WithReason(Equal("UnmarshalFailed")),
								testutils.WithInvolvedObjectKind(Equal(gto.GetObjectKind().GroupVersionKind().Kind)),
								testutils.WithInvolvedObjectName(Equal(gto.GetName())),
								testutils.WithEventType(Equal(string(corev1.EventTypeWarning))),
							),
						)))
					})
				})
			})

			Context("with an owner in a different namespace", func() {
				var ns *corev1.Namespace

				BeforeEach(func() {
					ns = &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "not-default",
						},
					}
					m.Create(ns)

					gitTrack = &farosv1alpha1.GitTrack{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "testgittrack",
							Namespace: "not-default",
						},
						Spec: farosv1alpha1.GitTrackSpec{
							Reference:  "foo",
							Repository: "bar",
						},
					}
					m.Create(gitTrack).Should(Succeed())

					gto.SetOwnerReferences([]metav1.OwnerReference{
						{
							APIVersion: "faros.pusher.com/v1alpha1",
							Kind:       "GitTrack",
							UID:        gitTrack.UID,
							Name:       gitTrack.Name,
						},
					})
					m.Create(gto).Should(Succeed())
				})

				AfterEach(func() {
					m.Delete(gitTrack).Should(Succeed())
					m.Get(gitTrack, timeout).ShouldNot(Succeed())
				})

				It("should not be reconciled", func() {
					Consistently(requests, consistentlyTimeout).ShouldNot(Receive(Equal(expectedClusterRequest)))
				})
			})
		})
	})
})

/*


	var (


		differentNamespaceOwnerTest = func() {
			var ns *corev1.Namespace
			var gt *farosv1alpha1.GitTrack
			BeforeEach(func() {
				CreateClusterInstance([]byte(exampleClusterRoleBinding))
				Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))
				Eventually(requests, timeout).Should(Receive(Equal(expectedClusterRequest)))

				ns = &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "not-default",
					},
				}
				c.Create(context.TODO(), ns)

				gt = &farosv1alpha1.GitTrack{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testgittrack",
						Namespace: "cluster-not-default",
					},
					Spec: farosv1alpha1.GitTrackSpec{
						Reference:  "foo",
						Repository: "bar",
					},
				}
				Expect(c.Create(context.TODO(), gt)).NotTo(HaveOccurred())

				key := types.NamespacedName{Name: "example"}
				Expect(c.Get(context.TODO(), key, clusterInstance)).NotTo(HaveOccurred())
				clusterInstance.SetOwnerReferences([]metav1.OwnerReference{
					{
						APIVersion: "faros.pusher.com/v1alpha1",
						Kind:       "GitTrack",
						Name:       gt.Name,
						UID:        gt.UID,
					},
				})
				Expect(c.Update(context.TODO(), clusterInstance)).NotTo(HaveOccurred())
			})

			It("should not reconcile it", func() {
				Consistently(requests).ShouldNot(Receive())
			})

			It("should not reconcile when the child is modified", func() {
				crb := &rbacv1.ClusterRoleBinding{}
				key := types.NamespacedName{Name: "example"}
				Expect(c.Get(context.TODO(), key, crb)).NotTo(HaveOccurred())

				crb.SetLabels(map[string]string{})
				Expect(c.Update(context.TODO(), crb)).NotTo(HaveOccurred())

				Consistently(requests).ShouldNot(Receive())
			})
		}



		}





*/
