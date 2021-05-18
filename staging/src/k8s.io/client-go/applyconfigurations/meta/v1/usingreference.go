/*
Copyright The Kubernetes Authors.

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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

import (
	types "k8s.io/apimachinery/pkg/types"
)

// UsingReferenceApplyConfiguration represents an declarative configuration of the UsingReference type for use
// with apply.
type UsingReferenceApplyConfiguration struct {
	APIVersion *string    `json:"apiVersion,omitempty"`
	Kind       *string    `json:"kind,omitempty"`
	Namespace  *string    `json:"namespace,omitempty"`
	Name       *string    `json:"name,omitempty"`
	UID        *types.UID `json:"uid,omitempty"`
	Controller *string    `json:"controller,omitempty"`
}

// UsingReferenceApplyConfiguration constructs an declarative configuration of the UsingReference type for use with
// apply.
func UsingReference() *UsingReferenceApplyConfiguration {
	return &UsingReferenceApplyConfiguration{}
}

// WithAPIVersion sets the APIVersion field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the APIVersion field is set to the value of the last call.
func (b *UsingReferenceApplyConfiguration) WithAPIVersion(value string) *UsingReferenceApplyConfiguration {
	b.APIVersion = &value
	return b
}

// WithKind sets the Kind field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Kind field is set to the value of the last call.
func (b *UsingReferenceApplyConfiguration) WithKind(value string) *UsingReferenceApplyConfiguration {
	b.Kind = &value
	return b
}

// WithNamespace sets the Namespace field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Namespace field is set to the value of the last call.
func (b *UsingReferenceApplyConfiguration) WithNamespace(value string) *UsingReferenceApplyConfiguration {
	b.Namespace = &value
	return b
}

// WithName sets the Name field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Name field is set to the value of the last call.
func (b *UsingReferenceApplyConfiguration) WithName(value string) *UsingReferenceApplyConfiguration {
	b.Name = &value
	return b
}

// WithUID sets the UID field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the UID field is set to the value of the last call.
func (b *UsingReferenceApplyConfiguration) WithUID(value types.UID) *UsingReferenceApplyConfiguration {
	b.UID = &value
	return b
}

// WithController sets the Controller field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Controller field is set to the value of the last call.
func (b *UsingReferenceApplyConfiguration) WithController(value string) *UsingReferenceApplyConfiguration {
	b.Controller = &value
	return b
}
