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

package storage

import (
	"fmt"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/utils"
)

const (
	noProvisioner = "kubernetes.io/no-provisioner"
	pvNamePrefix  = "pv"
)

type volumeModeTestCase struct {
	testPatterns map[string]testPattern
}

func initVolumeModeTestCase() testCase {
	return &volumeModeTestCase{
		testPatterns: map[string]testPattern{
			"FileSystem static PV": {
				testVolType: staticPV,
				volBindMode: storagev1.VolumeBindingImmediate,
				volMode:     v1.PersistentVolumeFilesystem,
			},
			"Block static PV": {
				testVolType: staticPV,
				volBindMode: storagev1.VolumeBindingImmediate,
				volMode:     v1.PersistentVolumeBlock,
			},
			"FileSystem dynamic PV": {
				testVolType: dynamicPV,
				volBindMode: storagev1.VolumeBindingImmediate,
				volMode:     v1.PersistentVolumeFilesystem,
			},
			"Block dynamic PV": {
				testVolType: dynamicPV,
				volBindMode: storagev1.VolumeBindingImmediate,
				volMode:     v1.PersistentVolumeBlock,
			},
		},
	}
}

func (t *volumeModeTestCase) getTestPatterns() map[string]testPattern {
	return t.testPatterns
}

func (t *volumeModeTestCase) initTestResource() testResource {
	return &volumeModeTestResource{}
}

func (t *volumeModeTestCase) createTestInput(
	testPattern testPattern,
	testResource testResource,
) testInput {
	if r, ok := testResource.(*volumeModeTestResource); ok {
		driver := r.driver
		driverInfo := driver.getDriverInfo()
		f := driverInfo.f

		return volumeModeTestInput{
			f:                f,
			sc:               r.sc,
			pvc:              r.pvc,
			pv:               r.pv,
			testVolType:      testPattern.testVolType,
			nodeName:         driverInfo.config.ClientNodeName,
			volMode:          testPattern.volMode,
			isBlockSupported: driverInfo.isBlockSupported,
		}
	}

	framework.Failf("Fail to convert testResource to xxxTestResource")
	return nil
}

func (t *volumeModeTestCase) getTestFunc() func(*testInput) {
	return testVolumeModeWithTestInput
}

func testVolumeModeWithTestInput(testInput *testInput) {
	Context("-", func() {
		var (
			t  volumeModeTestInput
			ok bool
		)

		BeforeEach(func() {
			testInputVal := *testInput
			if t, ok = testInputVal.(volumeModeTestInput); !ok {
				framework.Failf("Fail to convert testInput to volumeModeTestInput")
			}
		})

		testVolumeMode(&t)
	})
}

type volumeModeTestResource struct {
	driver testDriver

	sc  *storagev1.StorageClass
	pvc *v1.PersistentVolumeClaim
	pv  *v1.PersistentVolume
}

func (s *volumeModeTestResource) setup(driver testDriver, testPattern testPattern) {
	s.driver = driver

	driverInfo := s.driver.getDriverInfo()
	f := driverInfo.f
	ns := f.Namespace
	fsType := testPattern.fsType
	volBindMode := testPattern.volBindMode
	volMode := testPattern.volMode

	var (
		scName   string
		pvSource *v1.PersistentVolumeSource
	)

	skipCreatingResourceForProvider(testPattern)

	if testPattern.testVolType == staticPV {
		if volMode == v1.PersistentVolumeBlock {
			scName = fmt.Sprintf("%s-%s-sc-for-block", ns.Name, driverInfo.name)
		} else if volMode == v1.PersistentVolumeFilesystem {
			scName = fmt.Sprintf("%s-%s-sc-for-file", ns.Name, driverInfo.name)
		}
		if staticPVTestDriver, ok := driver.(staticPVTestDriver); ok {
			pvSource = staticPVTestDriver.getPersistentVolumeSource(false, fsType)
			if pvSource == nil {
				framework.Skipf("Driver %q does not define PersistentVolumeSource - skipping", driverInfo.name)
			}

			sc, pvConfig, pvcConfig := generateConfigsForStaticProvisionPVTest(scName, volBindMode, volMode, *pvSource)
			s.sc = sc
			s.pv = framework.MakePersistentVolume(pvConfig)
			s.pvc = framework.MakePersistentVolumeClaim(pvcConfig, ns.Name)
		}
	} else if testPattern.testVolType == dynamicPV {
		if dynamicPVTestDriver, ok := driver.(dynamicPVTestDriver); ok {
			s.sc = dynamicPVTestDriver.getDynamicProvisionStorageClass(fsType)
			if s.sc == nil {
				framework.Skipf("Driver %q does not define Dynamic Provision StorageClass - skipping", driverInfo.name)
			}
			s.sc.VolumeBindingMode = &volBindMode

			claimSize := "2Gi"
			s.pvc = getClaim(claimSize, ns.Name)
			s.pvc.Spec.StorageClassName = &s.sc.Name
			s.pvc.Spec.VolumeMode = &volMode
		}
	}
}

func (s *volumeModeTestResource) cleanup() {
	driverInfo := s.driver.getDriverInfo()
	f := driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace

	By("Deleting pv and pvc")
	errs := framework.PVPVCCleanup(cs, ns.Name, s.pv, s.pvc)
	if len(errs) > 0 {
		framework.Failf("Failed to delete PV and/or PVC: %v", utilerrors.NewAggregate(errs))
	}
	By("Deleting sc")
	if s.sc != nil {
		deleteStorageClass(cs, s.sc.Name)
	}
}

type volumeModeTestInput struct {
	f                *framework.Framework
	sc               *storagev1.StorageClass
	pvc              *v1.PersistentVolumeClaim
	pv               *v1.PersistentVolume
	testVolType      testVolType
	nodeName         string
	volMode          v1.PersistentVolumeMode
	isBlockSupported bool
}

func (i volumeModeTestInput) isTestInput() bool {
	return true
}

func testVolumeMode(t *volumeModeTestInput) {
	It("should create sc, pod, pv, and pvc, read/write to the pv, and delete all created resources", func() {
		skipBlockSupportTestIfUnsupported(t.volMode, t.isBlockSupported)
		f := t.f
		cs := f.ClientSet
		ns := f.Namespace
		var err error

		By("Creating sc")
		t.sc, err = cs.StorageV1().StorageClasses().Create(t.sc)
		Expect(err).NotTo(HaveOccurred())

		By("Creating pv and pvc")
		if t.testVolType == staticPV {
			t.pv, err = cs.CoreV1().PersistentVolumes().Create(t.pv)
			Expect(err).NotTo(HaveOccurred())

			// Prebind pv
			t.pvc.Spec.VolumeName = t.pv.Name
			t.pvc, err = cs.CoreV1().PersistentVolumeClaims(ns.Name).Create(t.pvc)
			Expect(err).NotTo(HaveOccurred())

			framework.ExpectNoError(framework.WaitOnPVandPVC(cs, ns.Name, t.pv, t.pvc))
		} else if t.testVolType == dynamicPV {
			t.pvc, err = cs.CoreV1().PersistentVolumeClaims(ns.Name).Create(t.pvc)
			Expect(err).NotTo(HaveOccurred())

			err = framework.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, cs, t.pvc.Namespace, t.pvc.Name, framework.Poll, framework.ClaimProvisionTimeout)
			Expect(err).NotTo(HaveOccurred())

			t.pvc, err = cs.CoreV1().PersistentVolumeClaims(t.pvc.Namespace).Get(t.pvc.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			t.pv, err = cs.CoreV1().PersistentVolumes().Get(t.pvc.Spec.VolumeName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
		}

		By("Creating pod")
		pod, err := framework.CreateSecPodWithNodeName(cs, ns.Name, []*v1.PersistentVolumeClaim{t.pvc},
			false, "", false, false, framework.SELinuxLabel,
			nil, t.nodeName, framework.PodStartTimeout)
		defer func() {
			framework.ExpectNoError(framework.DeletePodWithWait(f, cs, pod))
		}()
		Expect(err).NotTo(HaveOccurred())

		By("Checking if persistent volume exists as expected volume mode")
		checkVolumeModeOfPath(pod, t.volMode, "/mnt/volume1")

		By("Checking if read/write to persistent volume works properly")
		checkReadWriteToPath(pod, t.volMode, "/mnt/volume1")
	})

	It("should fail to create pod by failing to mount volume", func() {
		skipBlockUnsupportTestUnlessUnspported(t.volMode, t.isBlockSupported)
		f := t.f
		cs := f.ClientSet
		ns := f.Namespace
		var err error

		By("Creating sc")
		t.sc, err = cs.StorageV1().StorageClasses().Create(t.sc)
		Expect(err).NotTo(HaveOccurred())

		By("Creating pv and pvc")
		if t.testVolType == staticPV {
			t.pv, err = cs.CoreV1().PersistentVolumes().Create(t.pv)
			Expect(err).NotTo(HaveOccurred())

			// Prebind pv
			t.pvc.Spec.VolumeName = t.pv.Name
			t.pvc, err = cs.CoreV1().PersistentVolumeClaims(ns.Name).Create(t.pvc)
			Expect(err).NotTo(HaveOccurred())

			framework.ExpectNoError(framework.WaitOnPVandPVC(cs, ns.Name, t.pv, t.pvc))

			By("Creating pod")
			pod, err := framework.CreateSecPodWithNodeName(cs, ns.Name, []*v1.PersistentVolumeClaim{t.pvc},
				false, "", false, false, framework.SELinuxLabel,
				nil, t.nodeName, framework.PodStartTimeout)
			defer func() {
				framework.ExpectNoError(framework.DeletePodWithWait(f, cs, pod))
			}()
			// Static PV test should fail to create pod
			Expect(err).To(HaveOccurred())
		} else if t.testVolType == dynamicPV {
			t.pvc, err = cs.CoreV1().PersistentVolumeClaims(ns.Name).Create(t.pvc)
			Expect(err).NotTo(HaveOccurred())

			err = framework.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, cs, t.pvc.Namespace, t.pvc.Name, framework.Poll, framework.ClaimProvisionTimeout)
			// Dynamic PV test should fail to bind pv
			Expect(err).To(HaveOccurred())
		}

	})

	// TODO(mkimuram): Add more tests
}

func skipBlockSupportTestIfUnsupported(volMode v1.PersistentVolumeMode, isBlockSupported bool) {
	if volMode == v1.PersistentVolumeBlock && !isBlockSupported {
		framework.Skipf("Skip assertion for block test for block supported plugin.(Block unsupported)")
	}
}

func skipBlockUnsupportTestUnlessUnspported(volMode v1.PersistentVolumeMode, isBlockSupported bool) {
	if !(volMode == v1.PersistentVolumeBlock && !isBlockSupported) {
		framework.Skipf("Skip assertion for block test for block unsupported plugin.(Block suppported or FileSystem test)")
	}
}

func generateConfigsForStaticProvisionPVTest(scName string, volBindMode storagev1.VolumeBindingMode,
	volMode v1.PersistentVolumeMode, pvSource v1.PersistentVolumeSource) (*storagev1.StorageClass,
	framework.PersistentVolumeConfig, framework.PersistentVolumeClaimConfig) {
	// StorageClass
	scConfig := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: scName,
		},
		Provisioner:       noProvisioner,
		VolumeBindingMode: &volBindMode,
	}
	// PV
	pvConfig := framework.PersistentVolumeConfig{
		PVSource:         pvSource,
		NamePrefix:       pvNamePrefix,
		StorageClassName: scName,
		VolumeMode:       &volMode,
	}
	// PVC
	pvcConfig := framework.PersistentVolumeClaimConfig{
		AccessModes:      []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
		StorageClassName: &scName,
		VolumeMode:       &volMode,
	}

	return scConfig, pvConfig, pvcConfig
}

func checkVolumeModeOfPath(pod *v1.Pod, volMode v1.PersistentVolumeMode, path string) {
	if volMode == v1.PersistentVolumeBlock {
		// Check if block exists
		utils.VerifyExecInPodSucceed(pod, fmt.Sprintf("test -b %s", path))

		// Double check that it's not directory
		utils.VerifyExecInPodFail(pod, fmt.Sprintf("test -d %s", path), 1)
	} else {
		// Check if directory exists
		utils.VerifyExecInPodSucceed(pod, fmt.Sprintf("test -d %s", path))

		// Double check that it's not block
		utils.VerifyExecInPodFail(pod, fmt.Sprintf("test -b %s", path), 1)
	}
}

func checkReadWriteToPath(pod *v1.Pod, volMode v1.PersistentVolumeMode, path string) {
	if volMode == v1.PersistentVolumeBlock {
		// random -> file1
		utils.VerifyExecInPodSucceed(pod, "dd if=/dev/urandom of=/tmp/file1 bs=64 count=1")
		// file1 -> dev (write to dev)
		utils.VerifyExecInPodSucceed(pod, fmt.Sprintf("dd if=/tmp/file1 of=%s bs=64 count=1", path))
		// dev -> file2 (read from dev)
		utils.VerifyExecInPodSucceed(pod, fmt.Sprintf("dd if=%s of=/tmp/file2 bs=64 count=1", path))
		// file1 == file2 (check contents)
		utils.VerifyExecInPodSucceed(pod, "diff /tmp/file1 /tmp/file2")
		// Clean up temp files
		utils.VerifyExecInPodSucceed(pod, "rm -f /tmp/file1 /tmp/file2")

		// Check that writing file to block volume fails
		utils.VerifyExecInPodFail(pod, fmt.Sprintf("echo 'Hello world.' > %s/file1.txt", path), 1)
	} else {
		// text -> file1 (write to file)
		utils.VerifyExecInPodSucceed(pod, fmt.Sprintf("echo 'Hello world.' > %s/file1.txt", path))
		// grep file1 (read from file and check contents)
		utils.VerifyExecInPodSucceed(pod, fmt.Sprintf("grep 'Hello world.' %s/file1.txt", path))

		// Check that writing to directory as block volume fails
		utils.VerifyExecInPodFail(pod, fmt.Sprintf("dd if=/dev/urandom of=%s bs=64 count=1", path), 1)
	}
}
