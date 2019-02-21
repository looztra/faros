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

package utils

import (
	"context"

	"github.com/onsi/gomega"
	gtypes "github.com/onsi/gomega/types"
	farosv1alpha1 "github.com/pusher/faros/pkg/apis/faros/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Matcher has Gomega Matchers that use the controller-runtime client
type Matcher struct {
	Client client.Client
}

// Object is the combination of two interfaces as a helper for passing
// Kubernetes objects between methods
type Object interface {
	runtime.Object
	metav1.Object
}

// Create creates the object on the API server
func (m *Matcher) Create(obj Object, extras ...interface{}) gomega.GomegaAssertion {
	err := m.Client.Create(context.TODO(), obj)
	return gomega.Expect(err, extras)
}

// Update udpates the object on the API server
func (m *Matcher) Update(obj Object, intervals ...interface{}) gomega.GomegaAsyncAssertion {
	update := func() error {
		return m.Client.Update(context.TODO(), obj)
	}
	return gomega.Eventually(update, intervals...)
}

// Get gets the object from the API server
func (m *Matcher) Get(obj Object, intervals ...interface{}) gomega.GomegaAsyncAssertion {
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	get := func() error {
		return m.Client.Get(context.TODO(), key, obj)
	}
	return gomega.Eventually(get, intervals...)
}

// Eventually continually gets the object from the API for comparison
func (m *Matcher) Eventually(obj runtime.Object, intervals ...interface{}) gomega.GomegaAsyncAssertion {
	// If the object is a list, return a list
	if meta.IsListType(obj) {
		return m.eventuallyList(obj, intervals...)
	}
	if o, ok := obj.(Object); ok {
		return m.eventuallyObject(o, intervals...)
	}
	//Should not get here
	panic("Unknown object.")
}

// eventuallyObject gets an individual object from the API server
func (m *Matcher) eventuallyObject(obj Object, intervals ...interface{}) gomega.GomegaAsyncAssertion {
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
	get := func() Object {
		err := m.Client.Get(context.TODO(), key, obj)
		if err != nil {
			panic(err)
		}
		return obj
	}
	return gomega.Eventually(get, intervals...)
}

// eventuallyList gets a list type  from the API server
func (m *Matcher) eventuallyList(obj runtime.Object, intervals ...interface{}) gomega.GomegaAsyncAssertion {
	list := func() runtime.Object {
		err := m.Client.List(context.TODO(), &client.ListOptions{}, obj)
		if err != nil {
			panic(err)
		}
		return obj
	}
	return gomega.Eventually(list, intervals...)
}

// WithUnstructuredObject returns the objects inner object
func WithUnstructuredObject(matcher gtypes.GomegaMatcher) gtypes.GomegaMatcher {
	return gomega.WithTransform(func(ev event.GenericEvent) unstructured.Unstructured {
		u, ok := ev.Object.(*unstructured.Unstructured)
		if !ok {
			panic("Non unstructured object")
		}
		return *u
	}, matcher)
}

// WithGitTrackObjectStatusConditions returns the GitTrackObjects status conditions
func WithGitTrackObjectStatusConditions(matcher gtypes.GomegaMatcher) gtypes.GomegaMatcher {
	return gomega.WithTransform(func(gto farosv1alpha1.GitTrackObjectInterface) []farosv1alpha1.GitTrackObjectCondition {
		return gto.GetStatus().Conditions
	}, matcher)
}

// WithGitTrackObjectConditionType returns the GitTrackObjectsCondition's type
func WithGitTrackObjectConditionType(matcher gtypes.GomegaMatcher) gtypes.GomegaMatcher {
	return gomega.WithTransform(func(c farosv1alpha1.GitTrackObjectCondition) farosv1alpha1.GitTrackObjectConditionType {
		return c.Type
	}, matcher)
}

// WithGitTrackObjectConditionStatus returns the GitTrackObjectsCondition's status
func WithGitTrackObjectConditionStatus(matcher gtypes.GomegaMatcher) gtypes.GomegaMatcher {
	return gomega.WithTransform(func(c farosv1alpha1.GitTrackObjectCondition) corev1.ConditionStatus {
		return c.Status
	}, matcher)
}

// WithGitTrackObjectConditionReason returns the GitTrackObjectsCondition's reason
func WithGitTrackObjectConditionReason(matcher gtypes.GomegaMatcher) gtypes.GomegaMatcher {
	return gomega.WithTransform(func(c farosv1alpha1.GitTrackObjectCondition) string {
		return c.Reason
	}, matcher)
}

// WithGitTrackObjectConditionMessage returns the GitTrackObjectsCondition's message
func WithGitTrackObjectConditionMessage(matcher gtypes.GomegaMatcher) gtypes.GomegaMatcher {
	return gomega.WithTransform(func(c farosv1alpha1.GitTrackObjectCondition) string {
		return c.Message
	}, matcher)
}
