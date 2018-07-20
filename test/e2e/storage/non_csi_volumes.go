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
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/storage/utils"
)

type inlineVolumeIOTest struct {
	name      string
	config    framework.VolumeTestConfig
	volSource v1.VolumeSource
	testFile  string
	podSec    v1.PodSecurityContext
	fileSizes []int64
}

type inlineVolumeTest struct {
	name    string
	config  framework.VolumeTestConfig
	fsGroup *int64
	tests   []framework.VolumeTest
}

type persistentVolumeModeTest struct {
	name     string
	config   framework.VolumeTestConfig
	pvSource v1.PersistentVolumeSource

	scName           string
	volBindMode      storagev1.VolumeBindingMode
	volMode          v1.PersistentVolumeMode
	isBlockSupported bool
}

type testDriver interface {
	createDriver()
	cleanupDriver()
	createInlineVolumeTest() inlineVolumeTest
	createInlineVolumeIOTest() inlineVolumeIOTest
	createPersistentVolumeModeTest() []persistentVolumeModeTest
}

var testDrivers = map[string]func(f *framework.Framework, config framework.VolumeTestConfig) testDriver{
	"NFS":                        initNFSDriver,
	"GlusterFS":                  initGlusterFSDriver,
	"iSCSI [Feature:Volumes]":    initISCSIDriver,
	"Ceph-RBD [Feature:Volumes]": initRbdDriver,
	"CephFS [Feature:Volumes]":   initCephFSDriver,
}

var _ = utils.SIGDescribe("Non-CSI", func() {
	f := framework.NewDefaultFramework("volumes")

	var (
		cs     clientset.Interface
		ns     *v1.Namespace
		config framework.VolumeTestConfig
	)

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		config = framework.VolumeTestConfig{
			Namespace: ns.Name,
			Prefix:    "volume",
		}
	})

	for driverName, initDriver := range testDrivers {
		curDriverName := driverName
		curInitDriver := initDriver

		Context(fmt.Sprintf("using driver: %s", curDriverName), func() {
			var (
				driver testDriver
			)

			BeforeEach(func() {
				driver = curInitDriver(f, config)
				driver.createDriver()
			})

			AfterEach(func() {
				driver.cleanupDriver()
			})

			// Tests originally executed in volumes.go
			It("should be mountable", func() {
				t := driver.createInlineVolumeTest()
				defer framework.VolumeTestCleanup(f, t.config)

				framework.TestVolumeClient(cs, t.config, t.fsGroup, t.tests)
			})

			// Tests originally executed in volume_io.go
			It("should write files of various sizes, verify size, validate content [Slow]", func() {
				t := driver.createInlineVolumeIOTest()
				err := testVolumeIO(f, cs, t.config, t.volSource, &t.podSec, t.testFile, t.fileSizes)
				Expect(err).NotTo(HaveOccurred())
			})

			// Tests originally executed in persistent_volues-volumemode.go
			It("should show a volume with proper volume mode to a pod", func() {
				tests := driver.createPersistentVolumeModeTest()
				for _, t := range tests {
					testVolumeMode(f, cs, t.config, ns.Name, t.scName, t.volBindMode, t.volMode, t.pvSource, t.isBlockSupported)
				}
			})
		})
	}
})

////////////////////////////////////////////////////////////////////////
// NFS
////////////////////////////////////////////////////////////////////////
type nfsDriver struct {
	serverIP  string
	serverPod *v1.Pod
	volSource v1.VolumeSource
	pvSource  v1.PersistentVolumeSource

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initNFSDriver(f *framework.Framework, config framework.VolumeTestConfig) testDriver {
	return &nfsDriver{
		f:      f,
		config: config,
	}
}

func (n *nfsDriver) createInlineVolumeTest() inlineVolumeTest {
	var fsGroup int64
	return inlineVolumeTest{
		name:    "nfs",
		config:  n.config,
		fsGroup: &fsGroup,
		tests: []framework.VolumeTest{
			{
				Volume: n.volSource,
				File:   "index.html",
				// Must match content of test/images/volumes-tester/nfs/index.html
				ExpectedContent: "Hello from NFS!",
			},
		},
	}
}

func (n *nfsDriver) createInlineVolumeIOTest() inlineVolumeIOTest {
	return inlineVolumeIOTest{
		name:      "nfs",
		config:    n.config,
		volSource: n.volSource,
		testFile:  "nfs_io_test",
		podSec: v1.PodSecurityContext{
			SELinuxOptions: &v1.SELinuxOptions{
				Level: "s0:c0,c1",
			},
		},
		fileSizes: []int64{fileSizeSmall, fileSizeMedium, fileSizeLarge},
	}
}

func (n *nfsDriver) createPersistentVolumeModeTest() []persistentVolumeModeTest {
	return []persistentVolumeModeTest{
		persistentVolumeModeTest{
			name:             "nfs",
			config:           n.config,
			pvSource:         n.pvSource,
			volMode:          v1.PersistentVolumeBlock,
			scName:           "nfs-sc-for-block",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: false,
		},
		persistentVolumeModeTest{
			name:             "nfs",
			config:           n.config,
			pvSource:         n.pvSource,
			volMode:          v1.PersistentVolumeFilesystem,
			scName:           "nfs-sc-for-file",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: false,
		},
	}
}

func (n *nfsDriver) createDriver() {
	By("deploying nfs driver")
	f := n.f
	cs := f.ClientSet
	ns := f.Namespace

	n.config, n.serverPod, n.serverIP = framework.NewNFSServer(cs, ns.Name, []string{})
	n.volSource = v1.VolumeSource{
		NFS: &v1.NFSVolumeSource{
			Server:   n.serverIP,
			Path:     "/",
			ReadOnly: false,
		},
	}
	n.pvSource = v1.PersistentVolumeSource{
		NFS: &v1.NFSVolumeSource{
			Server:   n.serverIP,
			Path:     "/",
			ReadOnly: false,
		},
	}
}

func (n *nfsDriver) cleanupDriver() {
	By("uninstalling nfs driver")
	f := n.f
	cs := f.ClientSet

	framework.Logf("Deleting NFS server pod %q...", n.serverPod.Name)
	err := framework.DeletePodWithWait(f, cs, n.serverPod)
	Expect(err).NotTo(HaveOccurred(), "NFS server pod failed to delete")
}

////////////////////////////////////////////////////////////////////////
// Gluster
////////////////////////////////////////////////////////////////////////
type glusterFSDriver struct {
	serverIP  string
	serverPod *v1.Pod
	volSource v1.VolumeSource

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initGlusterFSDriver(f *framework.Framework, config framework.VolumeTestConfig) testDriver {
	framework.SkipUnlessNodeOSDistroIs("gci")
	return &glusterFSDriver{
		f:      f,
		config: config,
	}
}
func (g *glusterFSDriver) createInlineVolumeTest() inlineVolumeTest {
	var fsGroup int64
	return inlineVolumeTest{
		name:    "gluster",
		config:  g.config,
		fsGroup: &fsGroup,
		tests: []framework.VolumeTest{
			{
				Volume: g.volSource,
				File:   "index.html",
				// Must match content of test/images/volumes-tester/gluster/index.html
				ExpectedContent: "Hello from GlusterFS!",
			},
		},
	}
}

func (g *glusterFSDriver) createInlineVolumeIOTest() inlineVolumeIOTest {
	var podSec v1.PodSecurityContext // Passing nil security context

	return inlineVolumeIOTest{
		name:      "GlusterFS",
		config:    g.config,
		volSource: g.volSource,
		testFile:  "gluster_io_test",
		podSec:    podSec,
		fileSizes: []int64{fileSizeSmall, fileSizeMedium},
	}
}

func (g *glusterFSDriver) createPersistentVolumeModeTest() []persistentVolumeModeTest {
	return []persistentVolumeModeTest{}
}

func (g *glusterFSDriver) createDriver() {
	By("deploying GlusterFS driver")
	f := g.f
	cs := f.ClientSet
	ns := f.Namespace
	name := g.config.Prefix + "-server"

	g.config, g.serverPod, g.serverIP = framework.NewGlusterfsServer(cs, ns.Name)
	g.volSource = v1.VolumeSource{
		Glusterfs: &v1.GlusterfsVolumeSource{
			EndpointsName: name,
			// 'test_vol' comes from test/images/volumes-tester/gluster/run_gluster.sh
			Path:     "test_vol",
			ReadOnly: false,
		},
	}
}

func (g *glusterFSDriver) cleanupDriver() {
	By("uninstalling GlusterFS driver")
	f := g.f
	cs := f.ClientSet
	ns := f.Namespace
	name := g.config.Prefix + "-server"

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

////////////////////////////////////////////////////////////////////////
// iSCSI
// The iscsiadm utility and iscsi target kernel modules must be installed on all nodes.
////////////////////////////////////////////////////////////////////////
type iSCSIDriver struct {
	serverIP  string
	serverPod *v1.Pod
	volSource v1.VolumeSource
	pvSource  v1.PersistentVolumeSource

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initISCSIDriver(f *framework.Framework, config framework.VolumeTestConfig) testDriver {
	return &iSCSIDriver{
		f:      f,
		config: config,
	}
}
func (i *iSCSIDriver) createInlineVolumeTest() inlineVolumeTest {
	fsGroup := int64(1234)
	return inlineVolumeTest{
		name:    "iscsi",
		config:  i.config,
		fsGroup: &fsGroup,
		tests: []framework.VolumeTest{
			{
				Volume: i.volSource,
				File:   "index.html",
				// Must match content of test/images/volumes-tester/iscsi/block.tar.gz
				ExpectedContent: "Hello from iSCSI",
			},
		},
	}
}

func (i *iSCSIDriver) createInlineVolumeIOTest() inlineVolumeIOTest {
	fsGroup := int64(1234)
	return inlineVolumeIOTest{
		name:      "iscsi",
		config:    i.config,
		volSource: i.volSource,
		testFile:  "iscsi_io_test",
		podSec: v1.PodSecurityContext{
			FSGroup: &fsGroup,
		},
		fileSizes: []int64{fileSizeSmall, fileSizeMedium},
	}
}

func (i *iSCSIDriver) createPersistentVolumeModeTest() []persistentVolumeModeTest {
	return []persistentVolumeModeTest{
		persistentVolumeModeTest{
			name:             "iscsi",
			config:           i.config,
			pvSource:         i.pvSource,
			volMode:          v1.PersistentVolumeBlock,
			scName:           "iscsi-sc-for-block",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: true,
		},
		persistentVolumeModeTest{
			name:             "iscsi",
			config:           i.config,
			pvSource:         i.pvSource,
			volMode:          v1.PersistentVolumeFilesystem,
			scName:           "iscsi-sc-for-file",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: true,
		},
	}
}

func (i *iSCSIDriver) createDriver() {
	By("deploying iSCSI driver")
	f := i.f
	cs := f.ClientSet
	ns := f.Namespace

	i.config, i.serverPod, i.serverIP = framework.NewISCSIServer(cs, ns.Name)
	i.volSource = v1.VolumeSource{
		ISCSI: &v1.ISCSIVolumeSource{
			TargetPortal: i.serverIP + ":3260",
			// from test/images/volumes-tester/iscsi/initiatorname.iscsi
			IQN:      "iqn.2003-01.org.linux-iscsi.f21.x8664:sn.4b0aae584f7c",
			Lun:      0,
			FSType:   "ext2",
			ReadOnly: false,
		},
	}
	i.pvSource = v1.PersistentVolumeSource{
		ISCSI: &v1.ISCSIPersistentVolumeSource{
			TargetPortal: i.serverIP + ":3260",
			IQN:          "iqn.2003-01.org.linux-iscsi.f21.x8664:sn.4b0aae584f7c",
			Lun:          0,
		},
	}
}

func (i *iSCSIDriver) cleanupDriver() {
	By("uninstalling iSCSI driver")
	f := i.f
	cs := f.ClientSet

	framework.Logf("Deleting iSCSI server pod %q...", i.serverPod.Name)
	err := framework.DeletePodWithWait(f, cs, i.serverPod)
	Expect(err).NotTo(HaveOccurred(), "iSCSI server pod failed to delete")
}

////////////////////////////////////////////////////////////////////////
// Ceph RBD
////////////////////////////////////////////////////////////////////////
type rbdDriver struct {
	serverIP  string
	serverPod *v1.Pod
	secret    *v1.Secret
	volSource v1.VolumeSource
	pvSource  v1.PersistentVolumeSource

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initRbdDriver(f *framework.Framework, config framework.VolumeTestConfig) testDriver {
	return &rbdDriver{
		f:      f,
		config: config,
	}
}
func (r *rbdDriver) createInlineVolumeTest() inlineVolumeTest {
	fsGroup := int64(1234)
	return inlineVolumeTest{
		name:    "rdb",
		config:  r.config,
		fsGroup: &fsGroup,
		tests: []framework.VolumeTest{
			{
				Volume: r.volSource,
				File:   "index.html",
				// Must match content of test/images/volumes-tester/rbd/create_block.sh
				ExpectedContent: "Hello from RBD",
			},
		},
	}
}

func (r *rbdDriver) createInlineVolumeIOTest() inlineVolumeIOTest {
	fsGroup := int64(1234)
	return inlineVolumeIOTest{
		name:      "rbd",
		config:    r.config,
		volSource: r.volSource,
		testFile:  "ceph-rbd_io_test",
		podSec: v1.PodSecurityContext{
			FSGroup: &fsGroup,
		},
		fileSizes: []int64{fileSizeSmall, fileSizeMedium},
	}
}

func (r *rbdDriver) createPersistentVolumeModeTest() []persistentVolumeModeTest {
	return []persistentVolumeModeTest{
		persistentVolumeModeTest{
			name:             "rbd",
			config:           r.config,
			pvSource:         r.pvSource,
			volMode:          v1.PersistentVolumeBlock,
			scName:           "rbd-sc-for-block",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: true,
		},
		persistentVolumeModeTest{
			name:             "rbd",
			config:           r.config,
			pvSource:         r.pvSource,
			volMode:          v1.PersistentVolumeFilesystem,
			scName:           "rbd-sc-for-file",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: true,
		},
	}
}

func (r *rbdDriver) createDriver() {
	By("deploying rbd driver")
	f := r.f
	cs := f.ClientSet
	ns := f.Namespace

	r.config, r.serverPod, r.secret, r.serverIP = framework.NewRBDServer(cs, ns.Name)

	r.volSource = v1.VolumeSource{
		RBD: &v1.RBDVolumeSource{
			CephMonitors: []string{r.serverIP},
			RBDPool:      "rbd",
			RBDImage:     "foo",
			RadosUser:    "admin",
			SecretRef: &v1.LocalObjectReference{
				Name: r.secret.Name,
			},
			FSType:   "ext2",
			ReadOnly: false,
		},
	}
	r.pvSource = v1.PersistentVolumeSource{
		RBD: &v1.RBDPersistentVolumeSource{
			CephMonitors: []string{r.serverIP},
			RBDPool:      "rbd",
			RBDImage:     "foo",
			RadosUser:    "admin",
			SecretRef: &v1.SecretReference{
				Name:      r.secret.Name,
				Namespace: ns.Name,
			},
			ReadOnly: false,
		},
	}
}

func (r *rbdDriver) cleanupDriver() {
	By("uninstalling rbd driver")
	f := r.f
	cs := f.ClientSet
	ns := f.Namespace

	var secErr error
	if r.secret != nil {
		framework.Logf("Deleting Ceph-RDB server secret %q...", r.secret.Name)
		secErr = cs.CoreV1().Secrets(ns.Name).Delete(r.secret.Name, &metav1.DeleteOptions{})
	}

	framework.Logf("Deleting rbd server pod %q...", r.serverPod.Name)
	err := framework.DeletePodWithWait(f, cs, r.serverPod)
	if secErr != nil || err != nil {
		if secErr != nil {
			framework.Logf("Ceph-RDB delete secret failed: %v", err)
		}
		if err != nil {
			framework.Logf("Ceph-RDB server pod delete failed: %v", err)
		}
	}
}

////////////////////////////////////////////////////////////////////////
// Ceph
////////////////////////////////////////////////////////////////////////
type cephFSDriver struct {
	serverIP  string
	serverPod *v1.Pod
	secret    *v1.Secret
	volSource v1.VolumeSource
	pvSource  v1.PersistentVolumeSource

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initCephFSDriver(f *framework.Framework, config framework.VolumeTestConfig) testDriver {
	return &cephFSDriver{
		f:      f,
		config: config,
	}
}
func (c *cephFSDriver) createInlineVolumeTest() inlineVolumeTest {
	var fsGroup int64
	return inlineVolumeTest{
		name:    "ceph",
		config:  c.config,
		fsGroup: &fsGroup,
		tests: []framework.VolumeTest{
			{
				Volume: c.volSource,
				File:   "index.html",
				// Must match content of test/images/volumes-tester/rbd/create_block.sh
				ExpectedContent: "Hello Ceph!",
			},
		},
	}
}

func (c *cephFSDriver) createInlineVolumeIOTest() inlineVolumeIOTest {
	var fsGroup int64
	return inlineVolumeIOTest{
		name:      "ceph",
		config:    c.config,
		volSource: c.volSource,
		testFile:  "ceph-ceph_io_test",
		podSec: v1.PodSecurityContext{
			FSGroup: &fsGroup,
		},
		fileSizes: []int64{fileSizeSmall, fileSizeMedium},
	}
}

func (c *cephFSDriver) createPersistentVolumeModeTest() []persistentVolumeModeTest {
	return []persistentVolumeModeTest{
		persistentVolumeModeTest{
			name:             "ceph",
			config:           c.config,
			pvSource:         c.pvSource,
			volMode:          v1.PersistentVolumeBlock,
			scName:           "ceph-sc-for-block",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: false,
		},
		persistentVolumeModeTest{
			name:             "ceph",
			config:           c.config,
			pvSource:         c.pvSource,
			volMode:          v1.PersistentVolumeFilesystem,
			scName:           "ceph-sc-for-file",
			volBindMode:      storagev1.VolumeBindingImmediate,
			isBlockSupported: false,
		},
	}
}

func (c *cephFSDriver) createDriver() {
	By("deploying cephfs driver")
	f := c.f
	cs := f.ClientSet
	ns := f.Namespace

	c.config, c.serverPod, c.secret, c.serverIP = framework.NewRBDServer(cs, ns.Name)

	c.volSource = v1.VolumeSource{
		CephFS: &v1.CephFSVolumeSource{
			Monitors: []string{c.serverIP + ":6789"},
			User:     "kube",
			SecretRef: &v1.LocalObjectReference{
				Name: c.secret.Name,
			},
			ReadOnly: false,
		},
	}
	c.pvSource = v1.PersistentVolumeSource{
		CephFS: &v1.CephFSPersistentVolumeSource{
			Monitors: []string{c.serverIP + ":6789"},
			User:     "kube",
			SecretRef: &v1.SecretReference{
				Name:      c.secret.Name,
				Namespace: ns.Name,
			},
			ReadOnly: false,
		},
	}
}

func (c *cephFSDriver) cleanupDriver() {
	By("uninstalling cephfs driver")
	f := c.f
	cs := f.ClientSet
	ns := f.Namespace

	var secErr error
	if c.secret != nil {
		framework.Logf("Deleting CephFS server secret %q...", c.secret.Name)
		secErr = cs.CoreV1().Secrets(ns.Name).Delete(c.secret.Name, &metav1.DeleteOptions{})
	}

	framework.Logf("Deleting CephFS server pod %q...", c.serverPod.Name)
	err := framework.DeletePodWithWait(f, cs, c.serverPod)
	if secErr != nil || err != nil {
		if secErr != nil {
			framework.Logf("CephFS delete secret failed: %v", err)
		}
		if err != nil {
			framework.Logf("CephFS server pod delete failed: %v", err)
		}
	}
}
