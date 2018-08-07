/*
Copyright 2015 The Kubernetes Authors.

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

/*
 * This test checks that various VolumeSources are working.
 *
 * There are two ways, how to test the volumes:
 * 1) With containerized server (NFS, Ceph, Gluster, iSCSI, ...)
 * The test creates a server pod, exporting simple 'index.html' file.
 * Then it uses appropriate VolumeSource to import this file into a client pod
 * and checks that the pod can see the file. It does so by importing the file
 * into web server root and loadind the index.html from it.
 *
 * These tests work only when privileged containers are allowed, exporting
 * various filesystems (NFS, GlusterFS, ...) usually needs some mounting or
 * other privileged magic in the server pod.
 *
 * Note that the server containers are for testing purposes only and should not
 * be used in production.
 *
 * 2) With server outside of Kubernetes (Cinder, ...)
 * Appropriate server (e.g. OpenStack Cinder) must exist somewhere outside
 * the tested Kubernetes cluster. The test itself creates a new volume,
 * and checks, that Kubernetes can use it as a volume.
 */

// test/e2e/common/volumes.go duplicates the GlusterFS test from this file.  Any changes made to this
// test should be made there as well.

package storage

import (
	. "github.com/onsi/ginkgo"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/utils"
)

type volumesTestCase struct {
	testPatterns map[string]testPattern
}

func initVolumesTestCase() testCase {
	return &volumesTestCase{
		testPatterns: map[string]testPattern{
			// Default fsType
			"Volume (default fsType)": {
				testVolType: inlineVolume,
			},
			"Static PV (default fsType)": {
				testVolType: staticPV,
			},
			"Dynamic PV (default fsType)": {
				testVolType: dynamicPV,
			},
			// ext3
			"Volume (ext3)": {
				testVolType: inlineVolume,
				fsType:      "ext3",
			},
			"Static PV (ext3)": {
				testVolType: staticPV,
				fsType:      "ext3",
			},
			"Dynamic PV (ext3)": {
				testVolType: dynamicPV,
				fsType:      "ext3",
			},
			// ext4
			"Volume (ext4)": {
				testVolType: inlineVolume,
				fsType:      "ext4",
			},
			"Static PV (ext4)": {
				testVolType: staticPV,
				fsType:      "ext4",
			},
			"Dynamic PV (ext4)": {
				testVolType: dynamicPV,
				fsType:      "ext4",
			},
			// xfs
			"Volume (xfs)": {
				testVolType: inlineVolume,
				fsType:      "xfs",
			},
			"Static PV (xfs)": {
				testVolType: staticPV,
				fsType:      "xfs",
			},
			"Dynamic PV (xfs)": {
				testVolType: dynamicPV,
				fsType:      "xfs",
			},
		},
	}
}

func (t *volumesTestCase) getTestPatterns() map[string]testPattern {
	return t.testPatterns
}

func (t *volumesTestCase) initTestResource() testResource {
	return &genericVolumeTestResource{}
}

func (t *volumesTestCase) createTestInput(
	testPattern testPattern,
	testResource testResource,
) testInput {
	if r, ok := testResource.(*genericVolumeTestResource); ok {
		driver := r.driver
		driverInfo := driver.getDriverInfo()
		f := driverInfo.f
		volSource := r.volSource

		if volSource == nil {
			framework.Skipf("Driver %q does not define volumeSource - skipping", driverInfo.name)
		}

		if driverInfo.expectedContent == "" {
			framework.Skipf("Expected content for driver %q not found - skipping", driverInfo.name)
		}

		needInjectHTML := driverInfo.needInjectHTML
		// For dynamic provisioned pv, needInjectHTML should always be true
		if testPattern.testVolType == dynamicPV {
			needInjectHTML = true
		}

		return volumesTestInput{
			f:              f,
			name:           driverInfo.name,
			config:         driverInfo.config,
			fsGroup:        driverInfo.fsGroup,
			needInjectHTML: needInjectHTML,
			tests: []framework.VolumeTest{
				{
					Volume: *volSource,
					File:   "index.html",
					// Must match content
					ExpectedContent: driverInfo.expectedContent,
				},
			},
		}
	}

	framework.Failf("Fail to convert testResource to volumesTestResource")
	return nil
}

func (t *volumesTestCase) getTestFunc() func(*testInput) {
	return testVolumesWithTestInput
}

func testVolumesWithTestInput(testInput *testInput) {
	Context("-", func() {
		var (
			t  volumesTestInput
			ok bool
		)

		BeforeEach(func() {
			testInputVal := *testInput
			if t, ok = testInputVal.(volumesTestInput); !ok {
				framework.Failf("Fail to convert testInput to volumesTestInput")
			}
		})

		testVolumes(&t)
	})
}

type volumesTestInput struct {
	f              *framework.Framework
	name           string
	config         framework.VolumeTestConfig
	fsGroup        *int64
	needInjectHTML bool
	tests          []framework.VolumeTest
}

func (t volumesTestInput) isTestInput() bool {
	return true
}

func testVolumes(t *volumesTestInput) {
	It("should be mountable", func() {
		f := t.f
		cs := f.ClientSet
		defer framework.VolumeTestCleanup(f, t.config)

		if t.needInjectHTML {
			volumeTest := t.tests
			framework.InjectHtml(cs, t.config, volumeTest[0].Volume, volumeTest[0].ExpectedContent)
		}
		framework.TestVolumeClient(cs, t.config, t.fsGroup, t.tests)
	})
}

// These tests need privileged containers, which are disabled by default.
var _ = utils.SIGDescribe("Volumes", func() {
	f := framework.NewDefaultFramework("volume")

	// note that namespace deletion is handled by delete-namespace flag
	// filled inside BeforeEach
	var cs clientset.Interface
	var namespace *v1.Namespace

	BeforeEach(func() {
		cs = f.ClientSet
		namespace = f.Namespace
	})

	////////////////////////////////////////////////////////////////////////
	// ConfigMap
	////////////////////////////////////////////////////////////////////////
	Describe("ConfigMap", func() {
		It("should be mountable", func() {
			config := framework.VolumeTestConfig{
				Namespace: namespace.Name,
				Prefix:    "configmap",
			}

			defer framework.VolumeTestCleanup(f, config)
			configMap := &v1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ConfigMap",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: config.Prefix + "-map",
				},
				Data: map[string]string{
					"first":  "this is the first file",
					"second": "this is the second file",
					"third":  "this is the third file",
				},
			}
			if _, err := cs.CoreV1().ConfigMaps(namespace.Name).Create(configMap); err != nil {
				framework.Failf("unable to create test configmap: %v", err)
			}
			defer func() {
				_ = cs.CoreV1().ConfigMaps(namespace.Name).Delete(configMap.Name, nil)
			}()

			// Test one ConfigMap mounted several times to test #28502
			tests := []framework.VolumeTest{
				{
					Volume: v1.VolumeSource{
						ConfigMap: &v1.ConfigMapVolumeSource{
							LocalObjectReference: v1.LocalObjectReference{
								Name: config.Prefix + "-map",
							},
							Items: []v1.KeyToPath{
								{
									Key:  "first",
									Path: "firstfile",
								},
							},
						},
					},
					File:            "firstfile",
					ExpectedContent: "this is the first file",
				},
				{
					Volume: v1.VolumeSource{
						ConfigMap: &v1.ConfigMapVolumeSource{
							LocalObjectReference: v1.LocalObjectReference{
								Name: config.Prefix + "-map",
							},
							Items: []v1.KeyToPath{
								{
									Key:  "second",
									Path: "secondfile",
								},
							},
						},
					},
					File:            "secondfile",
					ExpectedContent: "this is the second file",
				},
			}
			framework.TestVolumeClient(cs, config, nil, tests)
		})
	})
})
