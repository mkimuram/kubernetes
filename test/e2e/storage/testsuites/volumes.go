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

// This test checks that various VolumeSources are working.

// test/e2e/common/volumes.go duplicates the GlusterFS test from this file.  Any changes made to this
// test should be made there as well.

package testsuites

import (
	"fmt"

	. "github.com/onsi/ginkgo"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/driverlib"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/patterns"
)

type volumesTestSuite struct {
	tsInfo testlib.TestSuiteInfo
}

var _ testlib.TestSuite = &volumesTestSuite{}

// InitVolumesTestSuite returns volumesTestSuite that implements TestSuite interface
func InitVolumesTestSuite() testlib.TestSuite {
	return &volumesTestSuite{
		tsInfo: testlib.TestSuiteInfo{
			Name: "volumes",
			TestPatterns: []patterns.TestPattern{
				// Default fsType
				patterns.DefaultFsInlineVolume,
				patterns.DefaultFsPreprovisionedPV,
				patterns.DefaultFsDynamicPV,
				// ext3
				patterns.Ext3InlineVolume,
				patterns.Ext3PreprovisionedPV,
				patterns.Ext3DynamicPV,
				// ext4
				patterns.Ext4InlineVolume,
				patterns.Ext4PreprovisionedPV,
				patterns.Ext4DynamicPV,
				// xfs
				patterns.XfsInlineVolume,
				patterns.XfsPreprovisionedPV,
				patterns.XfsDynamicPV,
			},
		},
	}
}

func (t *volumesTestSuite) GetTestSuiteInfo() testlib.TestSuiteInfo {
	return t.tsInfo
}

func (t *volumesTestSuite) SkipUnsupportedTest(pattern patterns.TestPattern, driver driverlib.TestDriver) {
	dInfo := driver.GetDriverInfo()
	if !dInfo.IsPersistent {
		framework.Skipf("Driver %q does not provide persistency - skipping", dInfo.Name)
	}
}

func createVolumesTestInput(pattern patterns.TestPattern, resource testlib.GenericVolumeTestResource) volumesTestInput {
	var fsGroup *int64
	driver := resource.Driver
	dInfo := driver.GetDriverInfo()
	f := dInfo.Framework
	volSource := resource.VolSource

	if volSource == nil {
		framework.Skipf("Driver %q does not define volumeSource - skipping", dInfo.Name)
	}

	if dInfo.IsFsGroupSupported {
		fsGroupVal := int64(1234)
		fsGroup = &fsGroupVal
	}

	return volumesTestInput{
		f:       f,
		name:    dInfo.Name,
		config:  dInfo.Config,
		fsGroup: fsGroup,
		tests: []framework.VolumeTest{
			{
				Volume: *volSource,
				File:   "index.html",
				// Must match content
				ExpectedContent: fmt.Sprintf("Hello from %s from namespace %s",
					dInfo.Name, f.Namespace.Name),
			},
		},
	}
}

func (t *volumesTestSuite) ExecTest(driver driverlib.TestDriver, pattern patterns.TestPattern) {
	Context(testlib.GetTestNameStr(t, pattern), func() {
		var (
			resource     testlib.GenericVolumeTestResource
			input        volumesTestInput
			needsCleanup bool
		)

		BeforeEach(func() {
			needsCleanup = false
			// Skip unsupported tests to avoid unnecessary resource initialization
			testlib.SkipUnsupportedTest(t, driver, pattern)
			needsCleanup = true

			// Setup test resource for driver and testpattern
			resource = testlib.GenericVolumeTestResource{}
			resource.SetupResource(driver, pattern)

			// Create test input
			input = createVolumesTestInput(pattern, resource)
		})

		AfterEach(func() {
			if needsCleanup {
				resource.CleanupResource(driver, pattern)
			}
		})

		testVolumes(&input)
	})
}

type volumesTestInput struct {
	f       *framework.Framework
	name    string
	config  framework.VolumeTestConfig
	fsGroup *int64
	tests   []framework.VolumeTest
}

func testVolumes(input *volumesTestInput) {
	It("should be mountable", func() {
		f := input.f
		cs := f.ClientSet
		defer framework.VolumeTestCleanup(f, input.config)

		volumeTest := input.tests
		framework.InjectHtml(cs, input.config, volumeTest[0].Volume, volumeTest[0].ExpectedContent)
		framework.TestVolumeClient(cs, input.config, input.fsGroup, input.tests)
	})
}
