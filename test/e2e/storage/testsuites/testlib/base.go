/*
Copyright 2018 The Kubernetes Authors.

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

package testlib

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/driverlib"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/patterns"
	imageutils "k8s.io/kubernetes/test/utils/image"
)

// TestSuite represents an interface for a set of tests which works with TestDriver
type TestSuite interface {
	// GetTestSuiteInfo returns the TestSuiteInfo for this TestSuite
	GetTestSuiteInfo() TestSuiteInfo
	// SkipUnsupportedTest skips the test if this TestSuite is not suitable to be tested with the combination of TestPattern and TestDriver
	SkipUnsupportedTest(patterns.TestPattern, driverlib.TestDriver)
	// ExecTest executes test of the testpattern for the driver
	ExecTest(driverlib.TestDriver, patterns.TestPattern)
}

// TestSuiteInfo represents a set of parameters for TestSuite
type TestSuiteInfo struct {
	Name         string                 // name of the TestSuite
	FeatureTag   string                 // featureTag for the TestSuite
	TestPatterns []patterns.TestPattern // Slice of TestPattern for the TestSuite
}

// TestResource represents an interface for resources that is used by TestSuite
type TestResource interface {
	// setupResource sets up test resources to be used for the tests with the
	// combination of TestDriver and TestPattern
	SetupResource(driverlib.TestDriver, patterns.TestPattern)
	// cleanupResource clean up the test resources created in SetupResource
	CleanupResource(driverlib.TestDriver, patterns.TestPattern)
}

// GetTestNameStr returns a string that represents the TestSuite and the pattern
func GetTestNameStr(suite TestSuite, pattern patterns.TestPattern) string {
	tsInfo := suite.GetTestSuiteInfo()
	return fmt.Sprintf("[Testpattern: %s]%s %s%s", pattern.Name, pattern.FeatureTag, tsInfo.Name, tsInfo.FeatureTag)
}

// RunTestSuite runs all testpatterns of all testSuites for a driver
func RunTestSuite(f *framework.Framework, config framework.VolumeTestConfig, driver driverlib.TestDriver, tsInits []func() TestSuite, tunePatternFunc func([]patterns.TestPattern) []patterns.TestPattern) {
	for _, testSuiteInit := range tsInits {
		suite := testSuiteInit()
		patterns := tunePatternFunc(suite.GetTestSuiteInfo().TestPatterns)

		for _, pattern := range patterns {
			suite.ExecTest(driver, pattern)
		}
	}
}

// SkipUnsupportedTest will skip tests if the combination of driver, testsuite, and testpattern
// is not suitable to be tested.
// Whether it needs to be skipped is checked by following steps:
// 1. Check if Whether volType is supported by driver from its interface
// 2. Check if fsType is supported by driver
// 3. Check with driver specific logic
// 4. Check with testSuite specific logic
func SkipUnsupportedTest(suite TestSuite, driver driverlib.TestDriver, pattern patterns.TestPattern) {
	dInfo := driver.GetDriverInfo()

	// 1. Check if Whether volType is supported by driver from its interface
	var isSupported bool
	switch pattern.VolType {
	case patterns.InlineVolume:
		_, isSupported = driver.(driverlib.InlineVolumeTestDriver)
	case patterns.PreprovisionedPV:
		_, isSupported = driver.(driverlib.PreprovisionedPVTestDriver)
	case patterns.DynamicPV:
		_, isSupported = driver.(driverlib.DynamicPVTestDriver)
	default:
		isSupported = false
	}

	if !isSupported {
		framework.Skipf("Driver %s doesn't support %v -- skipping", dInfo.Name, pattern.VolType)
	}

	// 2. Check if fsType is supported by driver
	if !dInfo.SupportedFsType.Has(pattern.FsType) {
		framework.Skipf("Driver %s doesn't support %v -- skipping", dInfo.Name, pattern.FsType)
	}

	// 3. Check with driver specific logic
	driver.SkipUnsupportedTest(pattern)

	// 4. Check with testSuite specific logic
	suite.SkipUnsupportedTest(pattern, driver)
}

// GenericVolumeTestResource is a generic implementation of TestResource that wil be able to
// be used in most of TestSuites.
// See volume_io.go or volumes.go in test/e2e/storage/testsuites/ for how to use this resource.
// Also, see subpath.go in the same directory for how to extend and use it.
type GenericVolumeTestResource struct {
	Driver    driverlib.TestDriver
	VolType   string
	VolSource *v1.VolumeSource
	Pvc       *v1.PersistentVolumeClaim
	Pv        *v1.PersistentVolume
	Sc        *storagev1.StorageClass

	DriverTestResource interface{}
}

var _ TestResource = &GenericVolumeTestResource{}

// SetupResource sets up GenericVolumeTestResource
func (r *GenericVolumeTestResource) SetupResource(driver driverlib.TestDriver, pattern patterns.TestPattern) {
	r.Driver = driver
	dInfo := driver.GetDriverInfo()
	f := dInfo.Framework
	cs := f.ClientSet
	fsType := pattern.FsType
	volType := pattern.VolType

	// Create volume for pre-provisioned volume tests
	r.DriverTestResource = driverlib.CreateVolume(driver, volType)

	switch volType {
	case patterns.InlineVolume:
		framework.Logf("Creating resource for inline volume")
		if iDriver, ok := driver.(driverlib.InlineVolumeTestDriver); ok {
			r.VolSource = iDriver.GetVolumeSource(false, fsType, r.DriverTestResource)
			r.VolType = dInfo.Name
		}
	case patterns.PreprovisionedPV:
		framework.Logf("Creating resource for pre-provisioned PV")
		if pDriver, ok := driver.(driverlib.PreprovisionedPVTestDriver); ok {
			pvSource := pDriver.GetPersistentVolumeSource(false, fsType, r.DriverTestResource)
			if pvSource != nil {
				r.VolSource, r.Pv, r.Pvc = createVolumeSourceWithPVCPV(f, dInfo.Name, pvSource, false)
			}
			r.VolType = fmt.Sprintf("%s-preprovisionedPV", dInfo.Name)
		}
	case patterns.DynamicPV:
		framework.Logf("Creating resource for dynamic PV")
		if dDriver, ok := driver.(driverlib.DynamicPVTestDriver); ok {
			claimSize := "5Gi"
			r.Sc = dDriver.GetDynamicProvisionStorageClass(fsType)

			By("creating a StorageClass " + r.Sc.Name)
			var err error
			r.Sc, err = cs.StorageV1().StorageClasses().Create(r.Sc)
			Expect(err).NotTo(HaveOccurred())

			if r.Sc != nil {
				r.VolSource, r.Pv, r.Pvc = createVolumeSourceWithPVCPVFromDynamicProvisionSC(
					f, dInfo.Name, claimSize, r.Sc, false, nil)
			}
			r.VolType = fmt.Sprintf("%s-dynamicPV", dInfo.Name)
		}
	default:
		framework.Failf("GenericVolumeTestResource doesn't support: %s", volType)
	}

	if r.VolSource == nil {
		framework.Skipf("Driver %s doesn't support %v -- skipping", dInfo.Name, volType)
	}
}

// CleanupResource cleans up GenericVolumeTestResource
func (r *GenericVolumeTestResource) CleanupResource(driver driverlib.TestDriver, pattern patterns.TestPattern) {
	dInfo := driver.GetDriverInfo()
	f := dInfo.Framework
	volType := pattern.VolType

	if r.Pvc != nil || r.Pv != nil {
		switch volType {
		case patterns.PreprovisionedPV:
			By("Deleting pv and pvc")
			if errs := framework.PVPVCCleanup(f.ClientSet, f.Namespace.Name, r.Pv, r.Pvc); len(errs) != 0 {
				framework.Failf("Failed to delete PVC or PV: %v", utilerrors.NewAggregate(errs))
			}
		case patterns.DynamicPV:
			By("Deleting pvc")
			// We only delete the PVC so that PV (and disk) can be cleaned up by dynamic provisioner
			if r.Pv.Spec.PersistentVolumeReclaimPolicy != v1.PersistentVolumeReclaimDelete {
				framework.Failf("Test framework does not currently support Dynamically Provisioned Persistent Volume %v specified with reclaim policy that isnt %v",
					r.Pv.Name, v1.PersistentVolumeReclaimDelete)
			}
			err := framework.DeletePersistentVolumeClaim(f.ClientSet, r.Pvc.Name, f.Namespace.Name)
			framework.ExpectNoError(err, "Failed to delete PVC %v", r.Pvc.Name)
			err = framework.WaitForPersistentVolumeDeleted(f.ClientSet, r.Pv.Name, 5*time.Second, 5*time.Minute)
			framework.ExpectNoError(err, "Persistent Volume %v not deleted by dynamic provisioner", r.Pv.Name)
		default:
			framework.Failf("Found PVC (%v) or PV (%v) but not running Preprovisioned or Dynamic test pattern", r.Pvc, r.Pv)
		}
	}

	if r.Sc != nil {
		By("Deleting sc")
		DeleteStorageClass(f.ClientSet, r.Sc.Name)
	}

	// Cleanup volume for pre-provisioned volume tests
	driverlib.DeleteVolume(driver, volType, r.DriverTestResource)
}

func createVolumeSourceWithPVCPV(
	f *framework.Framework,
	name string,
	pvSource *v1.PersistentVolumeSource,
	readOnly bool,
) (*v1.VolumeSource, *v1.PersistentVolume, *v1.PersistentVolumeClaim) {
	pvConfig := framework.PersistentVolumeConfig{
		NamePrefix:       fmt.Sprintf("%s-", name),
		StorageClassName: f.Namespace.Name,
		PVSource:         *pvSource,
	}
	pvcConfig := framework.PersistentVolumeClaimConfig{
		StorageClassName: &f.Namespace.Name,
	}

	framework.Logf("Creating PVC and PV")
	pv, pvc, err := framework.CreatePVCPV(f.ClientSet, pvConfig, pvcConfig, f.Namespace.Name, false)
	Expect(err).NotTo(HaveOccurred(), "PVC, PV creation failed")

	err = framework.WaitOnPVandPVC(f.ClientSet, f.Namespace.Name, pv, pvc)
	Expect(err).NotTo(HaveOccurred(), "PVC, PV failed to bind")

	volSource := &v1.VolumeSource{
		PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
			ClaimName: pvc.Name,
			ReadOnly:  readOnly,
		},
	}
	return volSource, pv, pvc
}

func createVolumeSourceWithPVCPVFromDynamicProvisionSC(
	f *framework.Framework,
	name string,
	claimSize string,
	sc *storagev1.StorageClass,
	readOnly bool,
	volMode *v1.PersistentVolumeMode,
) (*v1.VolumeSource, *v1.PersistentVolume, *v1.PersistentVolumeClaim) {
	cs := f.ClientSet
	ns := f.Namespace.Name

	By("creating a claim")
	pvc := GetClaim(claimSize, ns)
	pvc.Spec.StorageClassName = &sc.Name
	if volMode != nil {
		pvc.Spec.VolumeMode = volMode
	}

	var err error
	pvc, err = cs.CoreV1().PersistentVolumeClaims(ns).Create(pvc)
	Expect(err).NotTo(HaveOccurred())

	err = framework.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, cs, pvc.Namespace, pvc.Name, framework.Poll, framework.ClaimProvisionTimeout)
	Expect(err).NotTo(HaveOccurred())

	pvc, err = cs.CoreV1().PersistentVolumeClaims(pvc.Namespace).Get(pvc.Name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	pv, err := cs.CoreV1().PersistentVolumes().Get(pvc.Spec.VolumeName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	volSource := &v1.VolumeSource{
		PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
			ClaimName: pvc.Name,
			ReadOnly:  readOnly,
		},
	}
	return volSource, pv, pvc
}

func GetClaim(claimSize string, ns string) *v1.PersistentVolumeClaim {
	claim := v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pvc-",
			Namespace:    ns,
		},
		Spec: v1.PersistentVolumeClaimSpec{
			AccessModes: []v1.PersistentVolumeAccessMode{
				v1.ReadWriteOnce,
			},
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceName(v1.ResourceStorage): resource.MustParse(claimSize),
				},
			},
		},
	}

	return &claim
}

// DeleteStorageClass deletes the passed in StorageClass and catches errors other than "Not Found"
func DeleteStorageClass(cs clientset.Interface, className string) {
	err := cs.StorageV1().StorageClasses().Delete(className, nil)
	if err != nil && !apierrs.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

func SkipTestUntilBugfix(issueID string, driverName string, prefixes []string) {
	var needSkip bool
	for _, prefix := range prefixes {
		if strings.HasPrefix(driverName, prefix) {
			needSkip = true
		}
	}
	if needSkip {
		framework.Skipf("Due to issue #%s, this test with %s doesn't pass, skipping until it fixes", issueID, driverName)
	}
}

// StorageClassTest represents parameters to be used by provisioning tests
type StorageClassTest struct {
	Name               string
	CloudProviders     []string
	Provisioner        string
	StorageClassName   string
	Parameters         map[string]string
	DelayBinding       bool
	ClaimSize          string
	ExpectedSize       string
	PvCheck            func(volume *v1.PersistentVolume) error
	NodeName           string
	SkipWriteReadCheck bool
	VolumeMode         *v1.PersistentVolumeMode
}

// TestDynamicProvisioning tests dynamic provisioning with specified StorageClassTest and storageClass
func TestDynamicProvisioning(t StorageClassTest, client clientset.Interface, claim *v1.PersistentVolumeClaim, class *storagev1.StorageClass) *v1.PersistentVolume {
	var err error
	if class != nil {
		By("creating a StorageClass " + class.Name)
		class, err = client.StorageV1().StorageClasses().Create(class)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			framework.Logf("deleting storage class %s", class.Name)
			framework.ExpectNoError(client.StorageV1().StorageClasses().Delete(class.Name, nil))
		}()
	}

	By("creating a claim")
	claim, err = client.CoreV1().PersistentVolumeClaims(claim.Namespace).Create(claim)
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		framework.Logf("deleting claim %q/%q", claim.Namespace, claim.Name)
		// typically this claim has already been deleted
		err = client.CoreV1().PersistentVolumeClaims(claim.Namespace).Delete(claim.Name, nil)
		if err != nil && !apierrs.IsNotFound(err) {
			framework.Failf("Error deleting claim %q. Error: %v", claim.Name, err)
		}
	}()
	err = framework.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, client, claim.Namespace, claim.Name, framework.Poll, framework.ClaimProvisionTimeout)
	Expect(err).NotTo(HaveOccurred())

	By("checking the claim")
	// Get new copy of the claim
	claim, err = client.CoreV1().PersistentVolumeClaims(claim.Namespace).Get(claim.Name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	// Get the bound PV
	pv, err := client.CoreV1().PersistentVolumes().Get(claim.Spec.VolumeName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	// Check sizes
	expectedCapacity := resource.MustParse(t.ExpectedSize)
	pvCapacity := pv.Spec.Capacity[v1.ResourceName(v1.ResourceStorage)]
	Expect(pvCapacity.Value()).To(Equal(expectedCapacity.Value()), "pvCapacity is not equal to expectedCapacity")

	requestedCapacity := resource.MustParse(t.ClaimSize)
	claimCapacity := claim.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	Expect(claimCapacity.Value()).To(Equal(requestedCapacity.Value()), "claimCapacity is not equal to requestedCapacity")

	// Check PV properties
	By("checking the PV")
	expectedAccessModes := []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce}
	Expect(pv.Spec.AccessModes).To(Equal(expectedAccessModes))
	Expect(pv.Spec.ClaimRef.Name).To(Equal(claim.ObjectMeta.Name))
	Expect(pv.Spec.ClaimRef.Namespace).To(Equal(claim.ObjectMeta.Namespace))
	if class == nil {
		Expect(pv.Spec.PersistentVolumeReclaimPolicy).To(Equal(v1.PersistentVolumeReclaimDelete))
	} else {
		Expect(pv.Spec.PersistentVolumeReclaimPolicy).To(Equal(*class.ReclaimPolicy))
		Expect(pv.Spec.MountOptions).To(Equal(class.MountOptions))
	}
	if t.VolumeMode != nil {
		Expect(pv.Spec.VolumeMode).NotTo(BeNil())
		Expect(*pv.Spec.VolumeMode).To(Equal(*t.VolumeMode))
	}

	// Run the checker
	if t.PvCheck != nil {
		err = t.PvCheck(pv)
		Expect(err).NotTo(HaveOccurred())
	}

	if !t.SkipWriteReadCheck {
		// We start two pods:
		// - The first writes 'hello word' to the /mnt/test (= the volume).
		// - The second one runs grep 'hello world' on /mnt/test.
		// If both succeed, Kubernetes actually allocated something that is
		// persistent across pods.
		By("checking the created volume is writable and has the PV's mount options")
		command := "echo 'hello world' > /mnt/test/data"
		// We give the first pod the secondary responsibility of checking the volume has
		// been mounted with the PV's mount options, if the PV was provisioned with any
		for _, option := range pv.Spec.MountOptions {
			// Get entry, get mount options at 6th word, replace brackets with commas
			command += fmt.Sprintf(" && ( mount | grep 'on /mnt/test' | awk '{print $6}' | sed 's/^(/,/; s/)$/,/' | grep -q ,%s, )", option)
		}
		command += " || (mount | grep 'on /mnt/test'; false)"
		runInPodWithVolume(client, claim.Namespace, claim.Name, t.NodeName, command)

		By("checking the created volume is readable and retains data")
		runInPodWithVolume(client, claim.Namespace, claim.Name, t.NodeName, "grep 'hello world' /mnt/test/data")
	}
	By(fmt.Sprintf("deleting claim %q/%q", claim.Namespace, claim.Name))
	framework.ExpectNoError(client.CoreV1().PersistentVolumeClaims(claim.Namespace).Delete(claim.Name, nil))

	// Wait for the PV to get deleted if reclaim policy is Delete. (If it's
	// Retain, there's no use waiting because the PV won't be auto-deleted and
	// it's expected for the caller to do it.) Technically, the first few delete
	// attempts may fail, as the volume is still attached to a node because
	// kubelet is slowly cleaning up the previous pod, however it should succeed
	// in a couple of minutes. Wait 20 minutes to recover from random cloud
	// hiccups.
	if pv.Spec.PersistentVolumeReclaimPolicy == v1.PersistentVolumeReclaimDelete {
		By(fmt.Sprintf("deleting the claim's PV %q", pv.Name))
		framework.ExpectNoError(framework.WaitForPersistentVolumeDeleted(client, pv.Name, 5*time.Second, 20*time.Minute))
	}

	return pv
}

// runInPodWithVolume runs a command in a pod with given claim mounted to /mnt directory.
func runInPodWithVolume(c clientset.Interface, ns, claimName, nodeName, command string) {
	pod := &v1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pvc-volume-tester-",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "volume-tester",
					Image:   imageutils.GetE2EImage(imageutils.BusyBox),
					Command: []string{"/bin/sh"},
					Args:    []string{"-c", command},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "my-volume",
							MountPath: "/mnt/test",
						},
					},
				},
			},
			RestartPolicy: v1.RestartPolicyNever,
			Volumes: []v1.Volume{
				{
					Name: "my-volume",
					VolumeSource: v1.VolumeSource{
						PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
							ClaimName: claimName,
							ReadOnly:  false,
						},
					},
				},
			},
		},
	}

	if len(nodeName) != 0 {
		pod.Spec.NodeName = nodeName
	}
	pod, err := c.CoreV1().Pods(ns).Create(pod)
	framework.ExpectNoError(err, "Failed to create pod: %v", err)
	defer func() {
		body, err := c.CoreV1().Pods(ns).GetLogs(pod.Name, &v1.PodLogOptions{}).Do().Raw()
		if err != nil {
			framework.Logf("Error getting logs for pod %s: %v", pod.Name, err)
		} else {
			framework.Logf("Pod %s has the following logs: %s", pod.Name, body)
		}
		framework.DeletePodOrFail(c, ns, pod.Name)
	}()
	framework.ExpectNoError(framework.WaitForPodSuccessInNamespaceSlow(c, pod.Name, pod.Namespace))
}
