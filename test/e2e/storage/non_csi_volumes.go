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
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/api/core/v1"
	rbacv1beta1 "k8s.io/api/rbac/v1beta1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	clientset "k8s.io/client-go/kubernetes"
	kubeletapis "k8s.io/kubernetes/pkg/kubelet/apis"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/utils"
	vspheretest "k8s.io/kubernetes/test/e2e/storage/vsphere"
)

var testDrivers = map[string]func(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver{
	"NFS":                        initNFSDriver,
	"GlusterFS":                  initGlusterFSDriver,
	"iSCSI [Feature:Volumes]":    initISCSIDriver,
	"Ceph-RBD [Feature:Volumes]": initRbdDriver,
	"CephFS [Feature:Volumes]":   initCephFSDriver,
	"hostpath":                   initHostpathDriver,
	"hostpathSymlink":            initHostpathSymlinkDriver,
	"emptydir":                   initEmptydirDriver,
	"cinder":                     initCinderDriver,
	"gce":                        initGceDriver,
	"vSphere":                    initVSphereDriver,
	"azure":                      initAzureDriver,
	"aws":                        initAwsDriver,
}

var testCases = map[string]func() testCase{
	"volumes":                          initVolumesTestCase,
	"volumeIO":                         initVolumeIOTestCase,
	"volumeMode [Feature:BlockVolume]": initVolumeModeTestCase,
	"subPath":                          initSubPathTestCase,
	"dynamicProvision":                 initDynamicProvisioningTestCase,
}

var _ = utils.SIGDescribe("Non-CSI", func() {
	f := framework.NewDefaultFramework("volumes")

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
			Namespace:      ns.Name,
			Prefix:         "volume",
			ClientNodeName: node.Name,
			ServerNodeName: node.Name,
		}
	})

	for testCaseName, testCaseInit := range testCases {
		curTestCaseName := testCaseName
		curTestCase := testCaseInit()
		testPatterns := curTestCase.getTestPatterns()
		curTestFunc := curTestCase.getTestFunc()

		for testPatternName, testPattern := range testPatterns {
			curTestPatternName := testPatternName
			curTestPattern := testPattern

			for driverName, initDriver := range testDrivers {
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

type testDriver interface {
	createDriver()
	cleanupDriver()
	getDriverInfo() driverInfo
}

type inlineVolumeTestDriver interface {
	testDriver
	getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource
}

type staticPVTestDriver interface {
	testDriver
	getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource
}

type dynamicPVTestDriver interface {
	testDriver
	getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass
}

type driverInfo struct {
	name                      string
	fsGroup                   *int64
	podSec                    v1.PodSecurityContext
	privilegedSecurityContext bool
	maxFileSize               int64

	// supportablity for driver
	supportedFsType  map[string]bool
	isBlockSupported bool

	// volumes test specific parameters
	expectedContent string
	needInjectHTML  bool

	// volumeIO test specific parameter
	testFile string

	// dynamicProvision specific parameter
	skipWriteReadCheck bool

	f      *framework.Framework
	config framework.VolumeTestConfig
	node   v1.Node
}

type testCase interface {
	getTestPatterns() map[string]testPattern
	initTestResource() testResource
	createTestInput(testPattern, testResource) testInput
	getTestFunc() func(*testInput)
}

type testVolType string

var (
	inlineVolume testVolType = "inlineVolume"
	staticPV     testVolType = "staticPV"
	dynamicPV    testVolType = "dynamicPV"
)

type testPattern struct {
	testVolType testVolType
	fsType      string
	volBindMode storagev1.VolumeBindingMode
	volMode     v1.PersistentVolumeMode
}

type testResource interface {
	setup(testDriver, testPattern)
	cleanup()
}

type testInput interface {
	isTestInput() bool
}

func skipTestForDriver(testPattern testPattern, driver testDriver) {
	driverInfo := driver.getDriverInfo()
	// Check if testVolType is supported by driver
	if testPattern.testVolType == inlineVolume {
		if _, ok := driver.(inlineVolumeTestDriver); !ok {
			framework.Skipf("Driver %s doesn't support %v -- skipping",
				driverInfo.name, testPattern.testVolType)
		}
	} else if testPattern.testVolType == staticPV {
		if _, ok := driver.(staticPVTestDriver); !ok {
			framework.Skipf("Driver %s doesn't support %v -- skipping",
				driverInfo.name, testPattern.testVolType)
		}
	} else if testPattern.testVolType == dynamicPV {
		if _, ok := driver.(dynamicPVTestDriver); !ok {
			framework.Skipf("Driver %s doesn't support %v -- skipping",
				driverInfo.name, testPattern.testVolType)
		}
	}
}

// Generic volume test resouce
type genericVolumeTestResource struct {
	driver    testDriver
	volSource *v1.VolumeSource
	pvc       *v1.PersistentVolumeClaim
	pv        *v1.PersistentVolume
	sc        *storagev1.StorageClass
}

func skipCreatingResourceForProvider(testPattern testPattern) {
	fsType := testPattern.fsType

	if fsType == "xfs" && framework.ProviderIs("gce", "gke") {
		framework.SkipUnlessNodeOSDistroIs("ubuntu")
	}
}

func (r *genericVolumeTestResource) setup(driver testDriver, testPattern testPattern) {
	r.driver = driver
	driverInfo := driver.getDriverInfo()
	f := driverInfo.f
	cs := f.ClientSet
	fsType := testPattern.fsType

	skipCreatingResourceForProvider(testPattern)

	if testPattern.testVolType == inlineVolume {
		if inlineVolumeTestDriver, ok := driver.(inlineVolumeTestDriver); ok {
			r.volSource = inlineVolumeTestDriver.getVolumeSource(false, fsType)
		}
	} else if testPattern.testVolType == staticPV {
		if staticPVTestDriver, ok := driver.(staticPVTestDriver); ok {
			pvSource := staticPVTestDriver.getPersistentVolumeSource(false, fsType)
			if pvSource != nil {
				r.volSource, r.pv, r.pvc = CreateVolumeSourceWithPVCPV(f, driverInfo.name, pvSource, false)
			}
		}
	} else if testPattern.testVolType == dynamicPV {
		if dynamicPVTestDriver, ok := driver.(dynamicPVTestDriver); ok {
			claimSize := "2Gi"
			r.sc = dynamicPVTestDriver.getDynamicProvisionStorageClass(fsType)

			By("creating a StorageClass " + r.sc.Name)
			var err error
			r.sc, err = cs.StorageV1().StorageClasses().Create(r.sc)
			Expect(err).NotTo(HaveOccurred())

			if r.sc != nil {
				r.volSource, r.pv, r.pvc = CreateVolumeSourceWithPVCPVFromDynamicProvisionSC(
					f, driverInfo.name, claimSize, r.sc, false, nil)
			}
		}
	}

	if r.volSource == nil {
		framework.Skipf("Driver %s doesn't support %v -- skipping", driverInfo.name, testPattern.testVolType)
	}
}

func (r *genericVolumeTestResource) cleanup() {
	driverInfo := r.driver.getDriverInfo()
	f := driverInfo.f

	if r.pvc != nil || r.pv != nil {
		By("Deleting pv and pvc")
		if errs := framework.PVPVCCleanup(f.ClientSet, f.Namespace.Name, r.pv, r.pvc); len(errs) != 0 {
			framework.Failf("Failed to delete PVC or PV: %v", utilerrors.NewAggregate(errs))
		}
	}

	if r.sc != nil {
		By("Deleting sc")
		deleteStorageClass(f.ClientSet, r.sc.Name)
	}
}

func skipIfFsTypeNotSupported(driverInfo driverInfo, fsType string) {
	if _, ok := driverInfo.supportedFsType[fsType]; !ok {
		framework.Skipf("Driver %s doesn't support %v -- skipping", driverInfo.name, fsType)
	}
}

// NFS
type nfsDriver struct {
	serverIP               string
	serverPod              *v1.Pod
	externalProvisionerPod *v1.Pod

	driverInfo driverInfo
}

func initNFSDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	var fsGroup int64
	return &nfsDriver{
		driverInfo: driverInfo{
			name:    "nfs",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				SELinuxOptions: &v1.SELinuxOptions{
					Level: "s0:c0,c1",
				},
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeLarge,
			supportedFsType: map[string]bool{
				"": true, // Default fsType
			},
			isBlockSupported: false,
			expectedContent:  "Hello from NFS!", // content of test/images/volumes-tester/nfs/index.html
			needInjectHTML:   false,
			testFile:         "nfs_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (n *nfsDriver) getDriverInfo() driverInfo {
	return n.driverInfo
}

func (n *nfsDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(n.driverInfo, fsType)
	return &v1.VolumeSource{
		NFS: &v1.NFSVolumeSource{
			Server:   n.serverIP,
			Path:     "/",
			ReadOnly: readOnly,
		},
	}
}

func (n *nfsDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	skipIfFsTypeNotSupported(n.driverInfo, fsType)
	return &v1.PersistentVolumeSource{
		NFS: &v1.NFSVolumeSource{
			Server:   n.serverIP,
			Path:     "/",
			ReadOnly: readOnly,
		},
	}
}

func (n *nfsDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(n.driverInfo, fsType)
	provisioner := externalPluginName
	parameters := map[string]string{"mountOptions": "vers=4.1"}
	ns := n.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", n.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (n *nfsDriver) createDriver() {
	By("deploying nfs driver")
	f := n.driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace

	n.driverInfo.config, n.serverPod, n.serverIP = framework.NewNFSServer(cs, ns.Name, []string{})

	// Create external provisioner
	// TODO(mkimuram): cluster-admin gives too much right but system:persistent-volume-provisioner
	// is not enough. We should create new clusterrole for testing.
	framework.BindClusterRole(cs.RbacV1beta1(), "cluster-admin", ns.Name,
		rbacv1beta1.Subject{Kind: rbacv1beta1.ServiceAccountKind, Namespace: ns.Name, Name: "default"})

	err := framework.WaitForAuthorizationUpdate(cs.AuthorizationV1beta1(),
		serviceaccount.MakeUsername(ns.Name, "default"),
		"", "get", schema.GroupResource{Group: "storage.k8s.io", Resource: "storageclasses"}, true)
	framework.ExpectNoError(err, "Failed to update authorization: %v", err)

	By("creating an external dynamic provisioner pod")
	n.externalProvisionerPod = startExternalProvisioner(cs, ns.Name)
}

func (n *nfsDriver) cleanupDriver() {
	By("uninstalling nfs driver")
	f := n.driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace

	framework.CleanUpVolumeServer(f, n.serverPod)

	// Delete external provisioner
	framework.DeletePodOrFail(cs, ns.Name, n.externalProvisionerPod.Name)
	clusterRoleBindingName := ns.Name + "--" + "cluster-admin"
	cs.RbacV1beta1().ClusterRoleBindings().Delete(clusterRoleBindingName, metav1.NewDeleteOptions(0))
}

// Gluster
type glusterFSDriver struct {
	serverIP  string
	serverPod *v1.Pod

	driverInfo driverInfo
}

func initGlusterFSDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	framework.SkipUnlessNodeOSDistroIs("gci")
	var fsGroup int64
	var podSec v1.PodSecurityContext

	return &glusterFSDriver{
		driverInfo: driverInfo{
			name:    "gluster",
			fsGroup: &fsGroup,
			podSec:  podSec,
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"": true, // Default fsType
			},
			isBlockSupported: false,
			expectedContent:  "Hello from GlusterFS!", // content of test/images/volumes-tester/gluster/index.html
			needInjectHTML:   false,
			testFile:         "gluster_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (g *glusterFSDriver) getDriverInfo() driverInfo {
	return g.driverInfo
}

func (g *glusterFSDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(g.driverInfo, fsType)
	name := g.driverInfo.config.Prefix + "-server"
	return &v1.VolumeSource{
		Glusterfs: &v1.GlusterfsVolumeSource{
			EndpointsName: name,
			// 'test_vol' comes from test/images/volumes-tester/gluster/run_gluster.sh
			Path:     "test_vol",
			ReadOnly: readOnly,
		},
	}
}

func (g *glusterFSDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	skipIfFsTypeNotSupported(g.driverInfo, fsType)
	// TODO: implement this method propery
	return nil
}

func (g *glusterFSDriver) createDriver() {
	By("deploying GlusterFS driver")
	f := g.driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace

	g.driverInfo.config, g.serverPod, g.serverIP = framework.NewGlusterfsServer(cs, ns.Name)
}

func (g *glusterFSDriver) cleanupDriver() {
	By("uninstalling GlusterFS driver")
	f := g.driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace
	name := g.driverInfo.config.Prefix + "-server"

	framework.Logf("Deleting Gluster endpoints %q...", name)
	epErr := cs.CoreV1().Endpoints(ns.Name).Delete(name, nil)
	framework.Logf("Deleting Gluster server pod %q...", g.serverPod.Name)
	err := framework.DeletePodWithWait(f, cs, g.serverPod)
	if epErr != nil || err != nil {
		if epErr != nil {
			framework.Logf("Gluster delete endpoints failed: %v", err)
		}
		if err != nil {
			framework.Logf("Gluster server pod delete failed: %v", err)
		}
		framework.Failf("Cleanup failed")
	}
}

// iSCSI
// The iscsiadm utility and iscsi target kernel modules must be installed on all nodes.
type iSCSIDriver struct {
	serverIP  string
	serverPod *v1.Pod

	driverInfo driverInfo
}

func initISCSIDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	fsGroup := int64(1234)
	return &iSCSIDriver{
		driverInfo: driverInfo{
			name:    "iscsi",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"":     true, // Default fsType
				"ext2": true,
				// TODO: fix iSCSI driver can work with ext3
				//"ext3": true,
				"ext4": true,
			},
			isBlockSupported: true,
			expectedContent:  "Hello from iSCSI", // content of test/images/volumes-tester/iscsi/block.tar.gz
			needInjectHTML:   false,
			testFile:         "iscsi_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (i *iSCSIDriver) getDriverInfo() driverInfo {
	return i.driverInfo
}

func (i *iSCSIDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(i.driverInfo, fsType)
	volSource := v1.VolumeSource{
		ISCSI: &v1.ISCSIVolumeSource{
			TargetPortal: i.serverIP + ":3260",
			// from test/images/volumes-tester/iscsi/initiatorname.iscsi
			IQN:      "iqn.2003-01.org.linux-iscsi.f21.x8664:sn.4b0aae584f7c",
			Lun:      0,
			ReadOnly: readOnly,
		},
	}
	if fsType != "" {
		volSource.ISCSI.FSType = fsType
	}
	return &volSource
}

func (i *iSCSIDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	skipIfFsTypeNotSupported(i.driverInfo, fsType)
	pvSource := v1.PersistentVolumeSource{
		ISCSI: &v1.ISCSIPersistentVolumeSource{
			TargetPortal: i.serverIP + ":3260",
			IQN:          "iqn.2003-01.org.linux-iscsi.f21.x8664:sn.4b0aae584f7c",
			Lun:          0,
			ReadOnly:     readOnly,
		},
	}
	if fsType != "" {
		pvSource.ISCSI.FSType = fsType
	}
	return &pvSource
}

func (i *iSCSIDriver) createDriver() {
	By("deploying iSCSI driver")
	f := i.driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace

	i.driverInfo.config, i.serverPod, i.serverIP = framework.NewISCSIServer(cs, ns.Name)
}

func (i *iSCSIDriver) cleanupDriver() {
	By("uninstalling iSCSI driver")
	f := i.driverInfo.f

	framework.CleanUpVolumeServer(f, i.serverPod)
}

// Ceph RBD
type rbdDriver struct {
	serverIP  string
	serverPod *v1.Pod
	secret    *v1.Secret

	driverInfo driverInfo
}

func initRbdDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	fsGroup := int64(1234)
	return &rbdDriver{
		driverInfo: driverInfo{
			name:    "rbd",
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
			isBlockSupported: true,
			expectedContent:  "Hello from RBD", // content of test/images/volumes-tester/rbd/create_block.sh
			needInjectHTML:   false,
			testFile:         "ceph-rbd_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (r *rbdDriver) getDriverInfo() driverInfo {
	return r.driverInfo
}

func (r *rbdDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(r.driverInfo, fsType)
	volSource := v1.VolumeSource{
		RBD: &v1.RBDVolumeSource{
			CephMonitors: []string{r.serverIP},
			RBDPool:      "rbd",
			RBDImage:     "foo",
			RadosUser:    "admin",
			SecretRef: &v1.LocalObjectReference{
				Name: r.secret.Name,
			},
			ReadOnly: readOnly,
		},
	}
	if fsType != "" {
		volSource.RBD.FSType = fsType
	}
	return &volSource
}

func (r *rbdDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	f := r.driverInfo.f
	ns := f.Namespace
	skipIfFsTypeNotSupported(r.driverInfo, fsType)

	pvSource := v1.PersistentVolumeSource{
		RBD: &v1.RBDPersistentVolumeSource{
			CephMonitors: []string{r.serverIP},
			RBDPool:      "rbd",
			RBDImage:     "foo",
			RadosUser:    "admin",
			SecretRef: &v1.SecretReference{
				Name:      r.secret.Name,
				Namespace: ns.Name,
			},
			ReadOnly: readOnly,
		},
	}
	if fsType != "" {
		pvSource.RBD.FSType = fsType
	}
	return &pvSource
}

func (r *rbdDriver) createDriver() {
	By("deploying rbd driver")
	f := r.driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace

	r.driverInfo.config, r.serverPod, r.secret, r.serverIP = framework.NewRBDServer(cs, ns.Name)
}

func (r *rbdDriver) cleanupDriver() {
	By("uninstalling rbd driver")
	f := r.driverInfo.f

	framework.CleanUpVolumeServerWithSecret(f, r.serverPod, r.secret)
}

// Ceph
type cephFSDriver struct {
	serverIP  string
	serverPod *v1.Pod
	secret    *v1.Secret

	driverInfo driverInfo
}

func initCephFSDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	var fsGroup int64
	return &cephFSDriver{
		driverInfo: driverInfo{
			name:    "ceph",
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
			expectedContent:  "Hello Ceph!", // content of test/images/volumes-tester/rbd/create_block.sh
			needInjectHTML:   false,
			testFile:         "ceph-ceph_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (c *cephFSDriver) getDriverInfo() driverInfo {
	return c.driverInfo
}

func (c *cephFSDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(c.driverInfo, fsType)
	return &v1.VolumeSource{
		CephFS: &v1.CephFSVolumeSource{
			Monitors: []string{c.serverIP + ":6789"},
			User:     "kube",
			SecretRef: &v1.LocalObjectReference{
				Name: c.secret.Name,
			},
			ReadOnly: readOnly,
		},
	}
}

func (c *cephFSDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	skipIfFsTypeNotSupported(c.driverInfo, fsType)
	f := c.driverInfo.f
	ns := f.Namespace

	return &v1.PersistentVolumeSource{
		CephFS: &v1.CephFSPersistentVolumeSource{
			Monitors: []string{c.serverIP + ":6789"},
			User:     "kube",
			SecretRef: &v1.SecretReference{
				Name:      c.secret.Name,
				Namespace: ns.Name,
			},
			ReadOnly: readOnly,
		},
	}
}

func (c *cephFSDriver) createDriver() {
	By("deploying cephfs driver")
	f := c.driverInfo.f
	cs := f.ClientSet
	ns := f.Namespace

	c.driverInfo.config, c.serverPod, c.secret, c.serverIP = framework.NewRBDServer(cs, ns.Name)
}

func (c *cephFSDriver) cleanupDriver() {
	By("uninstalling cephfs driver")
	f := c.driverInfo.f

	framework.CleanUpVolumeServerWithSecret(f, c.serverPod, c.secret)
}

// Hostpath
type hostpathDriver struct {
	driverInfo driverInfo
}

func initHostpathDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	ns := f.Namespace
	var fsGroup int64

	return &hostpathDriver{
		driverInfo: driverInfo{
			name:    "hostpath",
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

func (h *hostpathDriver) getDriverInfo() driverInfo {
	return h.driverInfo
}

func (h *hostpathDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(h.driverInfo, fsType)
	// hostpath doesn't support readOnly volume
	if readOnly {
		return nil
	}
	return &v1.VolumeSource{
		HostPath: &v1.HostPathVolumeSource{
			Path: "/tmp",
		},
	}
}

func (h *hostpathDriver) createDriver() {
}

func (h *hostpathDriver) cleanupDriver() {
}

// HostpathSymlink
type hostpathSymlinkDriver struct {
	sourcePath string
	targetPath string
	prepPod    *v1.Pod

	driverInfo driverInfo
}

func initHostpathSymlinkDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	ns := f.Namespace
	var fsGroup int64

	return &hostpathSymlinkDriver{
		driverInfo: driverInfo{
			name:    "hostpathSymlink",
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
			expectedContent:  "Hello from hostpathSymlink from namespace " + ns.Name,
			needInjectHTML:   true,
			testFile:         "hostpathSymlink_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (h *hostpathSymlinkDriver) getDriverInfo() driverInfo {
	return h.driverInfo
}

func (h *hostpathSymlinkDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(h.driverInfo, fsType)
	// hostpath doesn't support readOnly volume
	if readOnly {
		return nil
	}
	return &v1.VolumeSource{
		HostPath: &v1.HostPathVolumeSource{
			Path: h.targetPath,
		},
	}
}

func (h *hostpathSymlinkDriver) createDriver() {
	f := h.driverInfo.f

	h.sourcePath = fmt.Sprintf("/tmp/%v", f.Namespace.Name)
	h.targetPath = fmt.Sprintf("/tmp/%v-link", f.Namespace.Name)

	node := h.driverInfo.node
	cmd := fmt.Sprintf("mkdir %v -m 777 && ln -s %v %v", h.sourcePath, h.sourcePath, h.targetPath)
	privileged := true

	// Launch pod to initialize hostpath directory and symlink
	h.prepPod = &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("hostpath-symlink-prep-%s", f.Namespace.Name),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    fmt.Sprintf("init-volume-%s", f.Namespace.Name),
					Image:   "busybox",
					Command: []string{"/bin/sh", "-ec", cmd},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      volumeName,
							MountPath: "/tmp",
						},
					},
					SecurityContext: &v1.SecurityContext{
						Privileged: &privileged,
					},
				},
			},
			RestartPolicy: v1.RestartPolicyNever,
			Volumes: []v1.Volume{
				{
					Name: volumeName,
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: "/tmp",
						},
					},
				},
			},
			NodeName: node.Name,
		},
	}
	// h.prepPod will be reused in cleanupDriver.
	pod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(h.prepPod)
	Expect(err).ToNot(HaveOccurred(), "while creating hostpath init pod")

	err = framework.WaitForPodSuccessInNamespace(f.ClientSet, pod.Name, pod.Namespace)
	Expect(err).ToNot(HaveOccurred(), "while waiting for hostpath init pod to succeed")

	err = framework.DeletePodWithWait(f, f.ClientSet, pod)
	Expect(err).ToNot(HaveOccurred(), "while deleting hostpath init pod")
}

func (h *hostpathSymlinkDriver) cleanupDriver() {
	f := h.driverInfo.f

	cmd := fmt.Sprintf("rm -rf %v&& rm -rf %v", h.targetPath, h.sourcePath)
	h.prepPod.Spec.Containers[0].Command = []string{"/bin/sh", "-ec", cmd}

	pod, err := f.ClientSet.CoreV1().Pods(f.Namespace.Name).Create(h.prepPod)
	Expect(err).ToNot(HaveOccurred(), "while creating hostpath teardown pod")

	err = framework.WaitForPodSuccessInNamespace(f.ClientSet, pod.Name, pod.Namespace)
	Expect(err).ToNot(HaveOccurred(), "while waiting for hostpath teardown pod to succeed")

	err = framework.DeletePodWithWait(f, f.ClientSet, pod)
	Expect(err).ToNot(HaveOccurred(), "while deleting hostpath teardown pod")
}

// emptydir
type emptydirDriver struct {
	driverInfo driverInfo
}

func initEmptydirDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	var fsGroup int64
	return &emptydirDriver{
		driverInfo: driverInfo{
			name:    "emptydir",
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
			// emtpydir doesn't provide persistency, therefore it should not test volumes test,
			// therefore, initializing expectedContent and needInjectHTML are intentionally skipped.
			testFile: "emptydir_io_test",
			f:        f,
			config:   config,
			node:     node,
		},
	}
}

func (e *emptydirDriver) getDriverInfo() driverInfo {
	return e.driverInfo
}

func (e *emptydirDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(e.driverInfo, fsType)
	// emptydir doesn't support readOnly volume
	if readOnly {
		return nil
	}
	return &v1.VolumeSource{
		EmptyDir: &v1.EmptyDirVolumeSource{},
	}
}

func (e *emptydirDriver) createDriver() {
}

func (e *emptydirDriver) cleanupDriver() {
}

// Cinder
// This driver assumes that OpenStack client tools are installed
// (/usr/bin/nova, /usr/bin/cinder and /usr/bin/keystone)
// and that the usual OpenStack authentication env. variables are set
// (OS_USERNAME, OS_PASSWORD, OS_TENANT_NAME at least).
type cinderDriver struct {
	volumeName string
	volumeID   string

	driverInfo driverInfo
}

func initCinderDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	framework.SkipUnlessProviderIs("openstack")

	ns := f.Namespace
	fsGroup := int64(1234)
	return &cinderDriver{
		driverInfo: driverInfo{
			name:    "cinder",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"":     true, // Default fsType
				"ext3": true,
			},
			isBlockSupported: false,
			expectedContent:  "Hello from Cinder from namespace " + ns.Name,
			needInjectHTML:   true,
			testFile:         "cinder_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (c *cinderDriver) getDriverInfo() driverInfo {
	return c.driverInfo
}

func (c *cinderDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(c.driverInfo, fsType)
	volSource := v1.VolumeSource{
		Cinder: &v1.CinderVolumeSource{
			VolumeID: c.volumeID,
			ReadOnly: readOnly,
		},
	}
	if fsType != "" {
		volSource.Cinder.FSType = fsType
	}
	return &volSource
}

func (c *cinderDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	skipIfFsTypeNotSupported(c.driverInfo, fsType)
	// TODO: implement this method propery
	return nil
}

func (c *cinderDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(c.driverInfo, fsType)
	provisioner := "kubernetes.io/cinder"
	parameters := map[string]string{}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := c.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", c.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (c *cinderDriver) createDriver() {
	f := c.driverInfo.f
	ns := f.Namespace

	// We assume that namespace.Name is a random string
	c.volumeName = ns.Name
	By("creating a test Cinder volume")
	output, err := exec.Command("cinder", "create", "--display-name="+c.volumeName, "1").CombinedOutput()
	outputString := string(output[:])
	framework.Logf("cinder output:\n%s", outputString)
	Expect(err).NotTo(HaveOccurred())

	// Parse 'id'' from stdout. Expected format:
	// |     attachments     |                  []                  |
	// |  availability_zone  |                 nova                 |
	// ...
	// |          id         | 1d6ff08f-5d1c-41a4-ad72-4ef872cae685 |
	c.volumeID = ""
	for _, line := range strings.Split(outputString, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 5 {
			continue
		}
		if fields[1] != "id" {
			continue
		}
		c.volumeID = fields[3]
		break
	}
	framework.Logf("Volume ID: %s", c.volumeID)
	Expect(c.volumeID).NotTo(Equal(""))
}

func (c *cinderDriver) cleanupDriver() {
	DeleteCinderVolume(c.volumeName)
}

func DeleteCinderVolume(name string) error {
	// Try to delete the volume for several seconds - it takes
	// a while for the plugin to detach it.
	var output []byte
	var err error
	timeout := time.Second * 120

	framework.Logf("Waiting up to %v for removal of cinder volume %s", timeout, name)
	for start := time.Now(); time.Since(start) < timeout; time.Sleep(5 * time.Second) {
		output, err = exec.Command("cinder", "delete", name).CombinedOutput()
		if err == nil {
			framework.Logf("Cinder volume %s deleted", name)
			return nil
		} else {
			framework.Logf("Failed to delete volume %s: %v", name, err)
		}
	}
	framework.Logf("Giving up deleting volume %s: %v\n%s", name, err, string(output[:]))
	return err
}

// GCE
type gceDriver struct {
	volumeName string

	driverInfo driverInfo
}

func initGceDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	framework.SkipUnlessProviderIs("gce", "gke")

	ns := f.Namespace
	fsGroup := int64(1234)

	// PD will be created in framework.TestContext.CloudConfig.Zone zone,
	// so pods should be also scheduled there.
	config.NodeSelector = map[string]string{
		kubeletapis.LabelZoneFailureDomain: framework.TestContext.CloudConfig.Zone,
	}

	return &gceDriver{
		driverInfo: driverInfo{
			name:    "gce",
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

func (g *gceDriver) getDriverInfo() driverInfo {
	return g.driverInfo
}

func (g *gceDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	// TODO: Uncomment below once pd deletiong code in cleanup becomes work well
	/*
		skipIfFsTypeNotSupported(g.driverInfo, fsType)
		volSource := v1.VolumeSource{
			GCEPersistentDisk: &v1.GCEPersistentDiskVolumeSource{
				PDName:   g.volumeName,
				ReadOnly: readOnly,
			},
		}
		if fsType != "" {
				volSource.GCEPersistentDisk.FSType = fsType
		}
		return &volSource
	*/
	return nil
}

func (g *gceDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	// TODO: implement this method propery
	skipIfFsTypeNotSupported(g.driverInfo, fsType)
	return nil
}

func (g *gceDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(g.driverInfo, fsType)
	provisioner := "kubernetes.io/gce-pd"
	parameters := map[string]string{}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := g.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", g.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (g *gceDriver) createDriver() {
	// TODO: Uncomment below, once pd deletiong code in cleanup becomes work well
	/*
		By("creating a test gce pd volume")
		var err error
		g.volumeName, err = framework.CreatePDWithRetry()
		Expect(err).NotTo(HaveOccurred())
	*/
}

func (g *gceDriver) cleanupDriver() {
	// TODO: Fix below detachAndDeletePDs call to pass right node
	/*
		config := g.driverInfo.config
		// The volume would be mounted on config.clientNodeName
		detachAndDeletePDs(g.volumeName, []types.NodeName{types.NodeName(config.ClientNodeName)})
	*/
}

// vSphere
type vSphereDriver struct {
	volumePath string
	nodeInfo   *vspheretest.NodeInfo

	driverInfo driverInfo
}

func initVSphereDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	framework.SkipUnlessProviderIs("vsphere")

	ns := f.Namespace
	fsGroup := int64(1234)
	return &vSphereDriver{
		driverInfo: driverInfo{
			name:    "vSphere",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"":     true, // Default fsType
				"ext4": true,
			},
			isBlockSupported: false,
			expectedContent:  "Hello from vSphere from namespace " + ns.Name,
			needInjectHTML:   true,
			testFile:         "vSphere_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (v *vSphereDriver) getDriverInfo() driverInfo {
	return v.driverInfo
}

func (v *vSphereDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(v.driverInfo, fsType)
	volSource := v1.VolumeSource{
		VsphereVolume: &v1.VsphereVirtualDiskVolumeSource{
			VolumePath: v.volumePath,
		},
	}
	if fsType != "" {
		volSource.VsphereVolume.FSType = fsType
	}
	return &volSource
}

func (v *vSphereDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	// TODO: implement this method propery
	skipIfFsTypeNotSupported(v.driverInfo, fsType)
	return nil
}

func (v *vSphereDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(v.driverInfo, fsType)
	provisioner := "kubernetes.io/vsphere-volume"
	parameters := map[string]string{}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := v.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", v.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (v *vSphereDriver) createDriver() {
	f := v.driverInfo.f
	vspheretest.Bootstrap(f)
	v.nodeInfo = vspheretest.GetReadySchedulableRandomNodeInfo()
	var err error
	v.volumePath, err = v.nodeInfo.VSphere.CreateVolume(&vspheretest.VolumeOptions{}, v.nodeInfo.DataCenterRef)
	Expect(err).NotTo(HaveOccurred())
}

func (v *vSphereDriver) cleanupDriver() {
	v.nodeInfo.VSphere.DeleteVolume(v.volumePath, v.nodeInfo.DataCenterRef)
}

// Azure
type azureDriver struct {
	volumeName string

	driverInfo driverInfo
}

func initAzureDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	framework.SkipUnlessProviderIs("azure")

	ns := f.Namespace
	fsGroup := int64(1234)
	return &azureDriver{
		driverInfo: driverInfo{
			name:    "azure",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"":     true, // Default fsType
				"ext4": true,
			},
			isBlockSupported: false,
			expectedContent:  "Hello Azure from namespace " + ns.Name,
			needInjectHTML:   true,
			testFile:         "azure_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (a *azureDriver) getDriverInfo() driverInfo {
	return a.driverInfo
}

func (a *azureDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	skipIfFsTypeNotSupported(a.driverInfo, fsType)
	diskName := a.volumeName[(strings.LastIndex(a.volumeName, "/") + 1):]

	volSource := v1.VolumeSource{
		AzureDisk: &v1.AzureDiskVolumeSource{
			DiskName:    diskName,
			DataDiskURI: volumeName,
			ReadOnly:    &readOnly,
		},
	}
	if fsType != "" {
		volSource.AzureDisk.FSType = &fsType
	}
	return &volSource
}

func (a *azureDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	// TODO: implement this method propery
	skipIfFsTypeNotSupported(a.driverInfo, fsType)
	return nil
}

func (a *azureDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(a.driverInfo, fsType)
	provisioner := "kubernetes.io/azure-disk"
	parameters := map[string]string{}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := a.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", a.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (a *azureDriver) createDriver() {
	By("creating a test azure disk volume")
	var err error
	a.volumeName, err = framework.CreatePDWithRetry()
	Expect(err).NotTo(HaveOccurred())
}

func (a *azureDriver) cleanupDriver() {
	framework.DeletePDWithRetry(a.volumeName)
}

// AWS
type awsDriver struct {
	driverInfo driverInfo
}

func initAwsDriver(f *framework.Framework, config framework.VolumeTestConfig, node v1.Node) testDriver {
	framework.SkipUnlessProviderIs("aws")

	ns := f.Namespace
	fsGroup := int64(1234)
	return &awsDriver{
		driverInfo: driverInfo{
			name:    "aws",
			fsGroup: &fsGroup,
			podSec: v1.PodSecurityContext{
				FSGroup: &fsGroup,
			},
			privilegedSecurityContext: true,
			maxFileSize:               fileSizeMedium,
			supportedFsType: map[string]bool{
				"":     true, // Default fsType
				"ext3": true,
			},
			isBlockSupported: false,
			expectedContent:  "Hello Aws from namespace " + ns.Name,
			needInjectHTML:   true,
			testFile:         "aws_io_test",
			f:                f,
			config:           config,
			node:             node,
		},
	}
}

func (a *awsDriver) getDriverInfo() driverInfo {
	return a.driverInfo
}

func (a *awsDriver) getVolumeSource(readOnly bool, fsType string) *v1.VolumeSource {
	// TODO: implement this method propery
	skipIfFsTypeNotSupported(a.driverInfo, fsType)
	return nil
}

func (a *awsDriver) getPersistentVolumeSource(readOnly bool, fsType string) *v1.PersistentVolumeSource {
	// TODO: implement this method propery
	skipIfFsTypeNotSupported(a.driverInfo, fsType)
	return nil
}

func (a *awsDriver) getDynamicProvisionStorageClass(fsType string) *storagev1.StorageClass {
	skipIfFsTypeNotSupported(a.driverInfo, fsType)
	provisioner := "kubernetes.io/aws-ebs"
	parameters := map[string]string{}
	if fsType != "" {
		parameters["fsType"] = fsType
	}
	ns := a.driverInfo.f.Namespace.Name
	suffix := fmt.Sprintf("%s-sc", a.driverInfo.name)

	return getStorageClass(provisioner, parameters, ns, suffix)
}

func (a *awsDriver) createDriver() {
}

func (a *awsDriver) cleanupDriver() {
}
