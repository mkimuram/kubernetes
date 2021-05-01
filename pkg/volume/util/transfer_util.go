/*
Copyright 2021 The Kubernetes Authors.

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

package util

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// IsTransferInProgress checks if transfer is in progress
func IsTransferInProgress(pvc *v1.PersistentVolumeClaim) bool {
	return HasPersistentVolumeConditionType(pvc, v1.PersistentVolumeClaimTransferring)
}

// MarkTransferInProgress marks transfer as in progress
func MarkTransferInProgress(
	pvc *v1.PersistentVolumeClaim,
	kubeClient clientset.Interface) (*v1.PersistentVolumeClaim, error) {

	if IsTransferInProgress(pvc) {
		// Already marked as TransferInProgress
		return pvc, nil
	}

	// Mark PVC as Transfer Started
	progressCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimTransferring,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Message:            "Waiting for receiver PVC to receive the bound PV and block consuming this PVC.",
	}
	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = append(newPVC.Status.Conditions, progressCondition)
	return PatchPVCStatus(pvc /*oldPVC*/, newPVC, kubeClient)
}

// IsTransferFinished checks if transfer is finished
func IsTransferFinished(pvc *v1.PersistentVolumeClaim) bool {
	return HasPersistentVolumeConditionType(pvc, v1.PersistentVolumeClaimTransferred)
}

// MarkTransferFinished marks transfer as completed
func MarkTransferFinished(
	pvc *v1.PersistentVolumeClaim,
	kubeClient clientset.Interface) (*v1.PersistentVolumeClaim, error) {

	if !IsTransferInProgress(pvc) && IsTransferFinished(pvc) {
		// Already marked as TransferFinished
		return pvc, nil
	}

	// Mark PVC as Transfer finished
	finishedCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimTransferred,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Message:            "Transferring volume has been completed.",
	}

	newPVC := pvc.DeepCopy()
	newPVC = RemoveConditionsOnPVC(newPVC, []v1.PersistentVolumeClaimConditionType{v1.PersistentVolumeClaimTransferring})
	newPVC.Status.Conditions = append(newPVC.Status.Conditions, finishedCondition)
	return PatchPVCStatus(pvc /*oldPVC*/, newPVC, kubeClient)
}

// IsReceiveInProgress checks if receive is in progress
func IsReceiveInProgress(pvc *v1.PersistentVolumeClaim) bool {
	return HasPersistentVolumeConditionType(pvc, v1.PersistentVolumeClaimReceiving)
}

// MarkReceiveInProgress marks receive as in progress
func MarkReceiveInProgress(
	pvc *v1.PersistentVolumeClaim,
	kubeClient clientset.Interface) (*v1.PersistentVolumeClaim, error) {

	if IsReceiveInProgress(pvc) {
		// Already marked as ReceiveInProgress
		return pvc, nil
	}

	// Mark PVC as Receive Started
	progressCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimReceiving,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Message:            "Receiving transferred volume.",
	}
	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = append(newPVC.Status.Conditions, progressCondition)
	return PatchPVCStatus(pvc /*oldPVC*/, newPVC, kubeClient)
}

// MarkReceiveFinished marks receive as completed
func MarkReceiveFinished(
	pvc *v1.PersistentVolumeClaim,
	kubeClient clientset.Interface) (*v1.PersistentVolumeClaim, error) {

	if !IsReceiveInProgress(pvc) && pvc.Status.Phase == v1.ClaimBound {
		// Already marked as TransferFinished
		return pvc, nil
	}

	newPVC := pvc.DeepCopy()
	newPVC = RemoveConditionsOnPVC(newPVC, []v1.PersistentVolumeClaimConditionType{v1.PersistentVolumeClaimReceiving})
	return PatchPVCStatus(pvc /*oldPVC*/, newPVC, kubeClient)
}

// HasPersistentVolumeConditionType returns if PersistentVolumeConditions has specified conditionType
func HasPersistentVolumeConditionType(pvc *v1.PersistentVolumeClaim, conditionType v1.PersistentVolumeClaimConditionType) bool {
	for _, condition := range pvc.Status.Conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}

// RemoveConditionsOnPVC removes specified condition types from pvc
func RemoveConditionsOnPVC(
	pvc *v1.PersistentVolumeClaim,
	conditionTypes []v1.PersistentVolumeClaimConditionType) *v1.PersistentVolumeClaim {
	// Make map of conditionTypes to remove
	removeConditionMap := map[v1.PersistentVolumeClaimConditionType]bool{}
	for _, conditionType := range conditionTypes {
		removeConditionMap[conditionType] = true
	}

	// Create newConditions with the condition that doesn't match with removeConditionMap
	newConditions := []v1.PersistentVolumeClaimCondition{}
	for _, condition := range pvc.Status.Conditions {
		if _, ok := removeConditionMap[condition.Type]; !ok {
			newConditions = append(newConditions, condition)
		}
	}

	pvc.Status.Conditions = newConditions

	return pvc
}
