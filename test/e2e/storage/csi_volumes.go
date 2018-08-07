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
	"math/rand"

	"k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"

	clientset "k8s.io/client-go/kubernetes"
	kubeletapis "k8s.io/kubernetes/pkg/kubelet/apis"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/utils"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	csiExternalProvisionerClusterRoleName string = "system:csi-external-provisioner"
	csiExternalAttacherClusterRoleName    string = "system:csi-external-attacher"
	csiDriverRegistrarClusterRoleName     string = "csi-driver-registrar"
	csiDriverSecretAccessClusterRoleName  string = "csi-secret-access"
)

var csiTestDrivers = map[string]func(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver{
	"CSI/hostpath":                           initHostpathCSIDriver,
	"CSI/gcePD [Feature: GCE PD CSI Plugin]": initGcePDCSIDriver,
	"CSI/Ceph-RBD [Feature:Volumes]":         initRbdCSIDriver,
	// Skip cephfs test until ceph/ceph-csi#48 is fixed
	//"CSI/CephFS [Feature:Volumes]":   initCephfsCSIDriver,
}

var csiTestCases = map[string]func() testCase{
	"volumes":                          initVolumesTestCase,
	"volumeIO":                         initVolumeIOTestCase,
	"volumeMode [Feature:BlockVolume]": initVolumeModeTestCase,
	"subPath":                          initSubPathTestCase,
	"dynamicProvision":                 initDynamicProvisioningTestCase,
}

var _ = utils.SIGDescribe("CSI Volumes", func() {
	f := framework.NewDefaultFramework("csi-mock-plugin")

	var (
		cs     clientset.Interface
		ns     *v1.Namespace
		node   v1.Node
		config framework.VolumeTestConfig
	)

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		nodes := framework.GetReadySchedulableNodesOrDie(cs)
		node = nodes.Items[rand.Intn(len(nodes.Items))]
		config = framework.VolumeTestConfig{
			Namespace:         ns.Name,
			Prefix:            "csi",
			ClientNodeName:    node.Name,
			ServerNodeName:    node.Name,
			WaitForCompletion: true,
		}
		csiDriverRegistrarClusterRole(config)
		csiDriverSecretAccessClusterRole(config)
	})

	for testCaseName, testCaseInit := range csiTestCases {
		curTestCaseName := testCaseName
		curTestCase := testCaseInit()
		testPatterns := curTestCase.getTestPatterns()
		curTestFunc := curTestCase.getTestFunc()

		for testPatternName, testPattern := range testPatterns {
			curTestPatternName := testPatternName
			curTestPattern := testPattern

			for driverName, initDriver := range csiTestDrivers {
				curDriverName := driverName
				curInitDriver := initDriver
				Context(fmt.Sprintf("Driver: %s, Test: %s, TestPattern: %s",
					curDriverName, curTestCaseName, curTestPatternName), func() {
					var (
						driver       testDriver
						testCase     testCase
						testResource testResource
						testInput    testInput
					)

					BeforeEach(func() {
						driver = curInitDriver(f, config, node)
						// Skip tests before driver creates test resource to avoid unnecessary initialization
						skipTestForDriver(curTestPattern, driver)

						driver.createDriver()
						testCase = curTestCase
						testResource = testCase.initTestResource()
						testResource.setup(driver, curTestPattern)
						testInput = testCase.createTestInput(curTestPattern, testResource)
					})

					AfterEach(func() {
						testResource.cleanup()
						driver.cleanupDriver()
					})

					curTestFunc(&testInput)
				})
			}
		}
	}
})

// CSI hostpath
type hostpathCSIDriver struct {
	combinedClusterRoleNames []string
	serviceAccount           *v1.ServiceAccount

	driverInfo driverInfo
}

func initHostpathCSIDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	ns := f.Namespace
	fsGroup := int64(1234)

	return &hostpathCSIDriver{
		combinedClusterRoleNames: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
			csiDriverRegistrarClusterRoleName,
		},
		driverInfo: driverInfo{
			name:    "csihostpath",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"": true, // Default fsType
			},
			isBlockSupported: false,
			expectedContent:  "Hello from hostpath from namespace " + ns.Name,
			needInjectHTML:   true,
			testFile:         "hostpath_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (h *hostpathCSIDriver) getDriverInfo() driverInfo {
	return h.driverInfo
}

func (h *hostpathCSIDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(h.driverInfo, fsType)
	provisioner := "csi-hostpath"
	parameters := map[string]string{}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := h.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", h.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (h *hostpathCSIDriver) createDriver() {
	By("deploying csi hostpath driver")
	f := h.driverInfo.f
	cs := f.ClientSet
	config := h.driverInfo.config
	h.serviceAccount = csiServiceAccount(cs, config, "hostpath", false)
	csiClusterRoleBindings(cs, config, false, h.serviceAccount, h.combinedClusterRoleNames)
	csiHostPathPod(cs, config, false, f, h.serviceAccount)
}

func (h *hostpathCSIDriver) cleanupDriver() {
	By("uninstalling csi hostpath driver")
	f := h.driverInfo.f
	cs := f.ClientSet
	config := h.driverInfo.config
	csiHostPathPod(cs, config, true, f, h.serviceAccount)
	csiClusterRoleBindings(cs, config, true, h.serviceAccount, h.combinedClusterRoleNames)
	csiServiceAccount(cs, config, "hostpath", true)
}

// CSI gce
type gcePDCSIDriver struct {
	controllerClusterRoles   []string
	nodeClusterRoles         []string
	controllerServiceAccount *v1.ServiceAccount
	nodeServiceAccount       *v1.ServiceAccount

	driverInfo driverInfo
}

func initGcePDCSIDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	framework.SkipUnlessProviderIs("gce", "gke")

	ns := f.Namespace
	fsGroup := int64(1234)

	// PD will be created in framework.TestContext.CloudConfig.Zone zone,
	// so pods should be also scheduled there.
	config.NodeSelector = map[string]string{
		kubeletapis.LabelZoneFailureDomain: framework.TestContext.CloudConfig.Zone,
	}

	return &gcePDCSIDriver{
		nodeClusterRoles: []string{
			csiDriverRegistrarClusterRoleName,
		},
		controllerClusterRoles: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
		},
		driverInfo: driverInfo{
			name:    "csigce",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"":     true, // Default fsType
				"ext2": true,
				"ext3": true,
				"ext4": true,
				"xfs":  true,
			},
			isBlockSupported: false,
			expectedContent:  "Hello from GCE from namespace " + ns.Name,
			needInjectHTML:   true,
			testFile:         "gce_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (g *gcePDCSIDriver) getDriverInfo() driverInfo {
	return g.driverInfo
}

func (g *gcePDCSIDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(g.driverInfo, fsType)

	node := g.driverInfo.node
	nodeZone, ok := node.GetLabels()[kubeletapis.LabelZoneFailureDomain]
	Expect(ok).To(BeTrue(), "Could not get label %v from node %v", kubeletapis.LabelZoneFailureDomain, node.GetName())

	provisioner := "csi-gce-pd"
	parameters := map[string]string{
		"type": "pd-standard",
		"zone": nodeZone,
	}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := g.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", g.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (g *gcePDCSIDriver) createDriver() {
	By("deploying gce-pd driver")
	f := g.driverInfo.f
	cs := f.ClientSet
	config := g.driverInfo.config
	g.controllerServiceAccount = csiServiceAccount(cs, config, "gce-controller", false /* setup */)
	g.nodeServiceAccount = csiServiceAccount(cs, config, "gce-node", false /* setup */)
	csiClusterRoleBindings(cs, config, false /* setup */, g.controllerServiceAccount, g.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, false /* setup */, g.nodeServiceAccount, g.nodeClusterRoles)
	deployGCEPDCSIDriver(cs, config, false /* setup */, f, g.nodeServiceAccount, g.controllerServiceAccount)
}

func (g *gcePDCSIDriver) cleanupDriver() {
	By("uninstalling gce-pd driver")
	f := g.driverInfo.f
	cs := f.ClientSet
	config := g.driverInfo.config
	deployGCEPDCSIDriver(cs, config, true /* teardown */, f, g.nodeServiceAccount, g.controllerServiceAccount)
	csiClusterRoleBindings(cs, config, true /* teardown */, g.controllerServiceAccount, g.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, true /* teardown */, g.nodeServiceAccount, g.nodeClusterRoles)
	csiServiceAccount(cs, config, "gce-controller", true /* teardown */)
	csiServiceAccount(cs, config, "gce-node", true /* teardown */)
}

// CSI Ceph RBD
type rbdCSIDriver struct {
	controllerClusterRoles   []string
	nodeClusterRoles         []string
	controllerServiceAccount *v1.ServiceAccount
	nodeServiceAccount       *v1.ServiceAccount
	serverPod                *v1.Pod
	secret                   *v1.Secret
	serverIP                 string

	driverInfo driverInfo
}

func initRbdCSIDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	fsGroup := int64(1234)
	return &rbdCSIDriver{
		nodeClusterRoles: []string{
			csiDriverRegistrarClusterRoleName,
		},
		controllerClusterRoles: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
			csiDriverSecretAccessClusterRoleName,
		},
		driverInfo: driverInfo{
			name:    "csirbd",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"":     true, // Default fsType
				"ext2": true,
				"ext3": true,
				"ext4": true,
			},
			isBlockSupported:   true,
			expectedContent:    "Hello from RBD", // content of test/images/volumes-tester/rbd/create_block.sh
			needInjectHTML:     false,
			testFile:           "ceph-rbd_io_test",
			skipWriteReadCheck: true,
			f:                  f,
			config:             config,
			node:               node,
		},
	}
}

func (r *rbdCSIDriver) getDriverInfo() driverInfo {
	return r.driverInfo
}

func (r *rbdCSIDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(r.driverInfo, fsType)
	provisioner := "csi-rbdplugin"
	parameters := map[string]string{
		"monitors": r.serverIP,
		"pool":     "rbd",
		"csiProvisionerSecretName":      r.secret.Name,
		"csiProvisionerSecretNamespace": r.driverInfo.config.Namespace,
	}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := r.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", r.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (r *rbdCSIDriver) createDriver() {
	By("deploying csi rbd driver")
	f := r.driverInfo.f
	cs := f.ClientSet
	config := r.driverInfo.config
	r.controllerServiceAccount = csiServiceAccount(cs, config, "rbd-controller", false /* setup */)
	r.nodeServiceAccount = csiServiceAccount(cs, config, "rbd-node", false /* setup */)
	csiClusterRoleBindings(cs, config, false /* setup */, r.controllerServiceAccount, r.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, false /* setup */, r.nodeServiceAccount, r.nodeClusterRoles)
	deployRbdCSIDriver(cs, config, false /* setup */, f, r.nodeServiceAccount, r.controllerServiceAccount, r)
}

func (r *rbdCSIDriver) cleanupDriver() {
	By("uninstalling csi rbd driver")
	f := r.driverInfo.f
	cs := f.ClientSet
	config := r.driverInfo.config
	deployRbdCSIDriver(cs, config, true /* teardown */, f, r.nodeServiceAccount, r.controllerServiceAccount, r)
	csiClusterRoleBindings(cs, config, true /* teardown */, r.controllerServiceAccount, r.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, true /* teardown */, r.nodeServiceAccount, r.nodeClusterRoles)
	csiServiceAccount(cs, config, "rbd-controller", true /* teardown */)
	csiServiceAccount(cs, config, "rbd-node", true /* teardown */)
}

// CSI Cephfs
type cephfsCSIDriver struct {
	controllerClusterRoles   []string
	nodeClusterRoles         []string
	controllerServiceAccount *v1.ServiceAccount
	nodeServiceAccount       *v1.ServiceAccount
	serverPod                *v1.Pod
	secret                   *v1.Secret
	serverIP                 string

	driverInfo driverInfo
}

func initCephfsCSIDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	fsGroup := int64(1234)
	return &cephfsCSIDriver{
		nodeClusterRoles: []string{
			csiDriverRegistrarClusterRoleName,
		},
		controllerClusterRoles: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
			csiDriverSecretAccessClusterRoleName,
		},
		driverInfo: driverInfo{
			name:    "csiceph",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"": true, // Default fsType
			},
			isBlockSupported:   false,
			expectedContent:    "Hello Ceph!", // content of test/images/volumes-tester/rbd/create_block.sh
			needInjectHTML:     false,
			testFile:           "ceph-ceph_io_test",
			skipWriteReadCheck: true,
			f:                  f,
			config:             config,
			node:               node,
		},
	}
}

func (c *cephfsCSIDriver) getDriverInfo() driverInfo {
	return c.driverInfo
}

func (c *cephfsCSIDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(c.driverInfo, fsType)
	provisioner := "csi-cephfsplugin"
	parameters := map[string]string{
		"monitors":        c.serverIP,
		"provisionVolume": "true",
		"pool":            "cephfs_data",
		"csiProvisionerSecretName":      c.secret.Name,
		"csiProvisionerSecretNamespace": c.driverInfo.config.Namespace,
	}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := c.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", c.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (c *cephfsCSIDriver) createDriver() {
	By("deploying csi ceph driver")
	f := c.driverInfo.f
	cs := f.ClientSet
	config := c.driverInfo.config
	c.controllerServiceAccount = csiServiceAccount(cs, config, "ceph-controller", false /* setup */)
	c.nodeServiceAccount = csiServiceAccount(cs, config, "ceph-node", false /* setup */)
	csiClusterRoleBindings(cs, config, false /* setup */, c.controllerServiceAccount, c.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, false /* setup */, c.nodeServiceAccount, c.nodeClusterRoles)
	deployCephfsCSIDriver(cs, config, false /* setup */, f, c.nodeServiceAccount, c.controllerServiceAccount, c)
}

func (c *cephfsCSIDriver) cleanupDriver() {
	By("uninstalling csi ceph driver")
	f := c.driverInfo.f
	cs := f.ClientSet
	config := c.driverInfo.config
	deployCephfsCSIDriver(cs, config, true /* teardown */, f, c.nodeServiceAccount, c.controllerServiceAccount, c)
	csiClusterRoleBindings(cs, config, true /* teardown */, c.controllerServiceAccount, c.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, true /* teardown */, c.nodeServiceAccount, c.nodeClusterRoles)
	csiServiceAccount(cs, config, "ceph-controller", true /* teardown */)
	csiServiceAccount(cs, config, "ceph-node", true /* teardown */)
}
