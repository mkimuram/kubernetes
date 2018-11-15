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

package testsuites

import (
	. "github.com/onsi/ginkgo"

	"k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/driverlib"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/patterns"
)

type provisioningTestSuite struct {
	tsInfo testlib.TestSuiteInfo
}

var _ testlib.TestSuite = &provisioningTestSuite{}

// InitProvisioningTestSuite returns provisioningTestSuite that implements TestSuite interface
func InitProvisioningTestSuite() testlib.TestSuite {
	return &provisioningTestSuite{
		tsInfo: testlib.TestSuiteInfo{
			Name: "provisioning",
			TestPatterns: []patterns.TestPattern{
				patterns.DefaultFsDynamicPV,
			},
		},
	}
}

func (p *provisioningTestSuite) GetTestSuiteInfo() testlib.TestSuiteInfo {
	return p.tsInfo
}

func (p *provisioningTestSuite) SkipUnsupportedTest(pattern patterns.TestPattern, driver driverlib.TestDriver) {
}

func createProvisioningTestInput(driver driverlib.TestDriver, pattern patterns.TestPattern) (provisioningTestResource, provisioningTestInput) {
	// Setup test resource for driver and testpattern
	resource := provisioningTestResource{}
	resource.SetupResource(driver, pattern)

	input := provisioningTestInput{
		testCase: testlib.StorageClassTest{
			ClaimSize:    resource.claimSize,
			ExpectedSize: resource.claimSize,
		},
		cs:    driver.GetDriverInfo().Framework.ClientSet,
		pvc:   resource.pvc,
		sc:    resource.sc,
		dInfo: driver.GetDriverInfo(),
	}

	if driver.GetDriverInfo().Config.ClientNodeName != "" {
		input.testCase.NodeName = driver.GetDriverInfo().Config.ClientNodeName
	}

	return resource, input
}

func (p *provisioningTestSuite) ExecTest(driver driverlib.TestDriver, pattern patterns.TestPattern) {
	Context(testlib.GetTestNameStr(p, pattern), func() {
		var (
			resource     provisioningTestResource
			input        provisioningTestInput
			needsCleanup bool
		)

		BeforeEach(func() {
			needsCleanup = false
			// Skip unsupported tests to avoid unnecessary resource initialization
			testlib.SkipUnsupportedTest(p, driver, pattern)
			needsCleanup = true

			// Create test input
			resource, input = createProvisioningTestInput(driver, pattern)
		})

		AfterEach(func() {
			if needsCleanup {
				resource.CleanupResource(driver, pattern)
			}
		})

		// Ginkgo's "Global Shared Behaviors" require arguments for a shared function
		// to be a single struct and to be passed as a pointer.
		// Please see https://onsi.github.io/ginkgo/#global-shared-behaviors for details.
		testProvisioning(&input)
	})
}

type provisioningTestResource struct {
	driver driverlib.TestDriver

	claimSize string
	sc        *storage.StorageClass
	pvc       *v1.PersistentVolumeClaim
}

var _ testlib.TestResource = &provisioningTestResource{}

func (p *provisioningTestResource) SetupResource(driver driverlib.TestDriver, pattern patterns.TestPattern) {
	// Setup provisioningTest resource
	switch pattern.VolType {
	case patterns.DynamicPV:
		if dDriver, ok := driver.(driverlib.DynamicPVTestDriver); ok {
			p.sc = dDriver.GetDynamicProvisionStorageClass("")
			if p.sc == nil {
				framework.Skipf("Driver %q does not define Dynamic Provision StorageClass - skipping", driver.GetDriverInfo().Name)
			}
			p.driver = driver
			p.claimSize = "5Gi"
			p.pvc = testlib.GetClaim(p.claimSize, driver.GetDriverInfo().Framework.Namespace.Name)
			p.pvc.Spec.StorageClassName = &p.sc.Name
			framework.Logf("In creating storage class object and pvc object for driver - sc: %v, pvc: %v", p.sc, p.pvc)
		}
	default:
		framework.Failf("Dynamic Provision test doesn't support: %s", pattern.VolType)
	}
}

func (p *provisioningTestResource) CleanupResource(driver driverlib.TestDriver, pattern patterns.TestPattern) {
}

type provisioningTestInput struct {
	testCase testlib.StorageClassTest
	cs       clientset.Interface
	pvc      *v1.PersistentVolumeClaim
	sc       *storage.StorageClass
	dInfo    *driverlib.DriverInfo
}

func testProvisioning(input *provisioningTestInput) {
	It("should provision storage with defaults", func() {
		testlib.TestDynamicProvisioning(input.testCase, input.cs, input.pvc, input.sc)
	})

	It("should provision storage with mount options", func() {
		if input.dInfo.SupportedMountOption == nil {
			framework.Skipf("Driver %q does not define supported mount option - skipping", input.dInfo.Name)
		}

		input.sc.MountOptions = input.dInfo.SupportedMountOption.Union(input.dInfo.RequiredMountOption).List()
		testlib.TestDynamicProvisioning(input.testCase, input.cs, input.pvc, input.sc)
	})

	It("should create and delete block persistent volumes", func() {
		if !input.dInfo.IsBlockSupported {
			framework.Skipf("Driver %q does not support BlockVolume - skipping", input.dInfo.Name)
		}
		block := v1.PersistentVolumeBlock
		input.testCase.VolumeMode = &block
		input.testCase.SkipWriteReadCheck = true
		input.pvc.Spec.VolumeMode = &block
		testlib.TestDynamicProvisioning(input.testCase, input.cs, input.pvc, input.sc)
	})
}
