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
	"time"

	"k8s.io/api/core/v1"

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

type csiTestDriver interface {
	createCSIDriver()
	cleanupCSIDriver()
	createStorageClassTest(node v1.Node) storageClassTest
}

var csiTestDrivers = map[string]func(f *framework.Framework, config framework.VolumeTestConfig) csiTestDriver{
	"hostPath": initCSIHostpath,
	// Feature tag to skip test in CI, pending fix of #62237
	"[Feature: GCE PD CSI Plugin] gcePD": initCSIgcePD,
	"[Feature:Volumes] rbd":              initCSIrbd,
	// Skip cephfs test until ceph/ceph-csi#48 is fixed
	//"[Feature:Volumes] cephfs":           initCSIcephfs,
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
		nodes := framework.GetReadySchedulableNodesOrDie(f.ClientSet)
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

	for driverName, initCSIDriver := range csiTestDrivers {
		curDriverName := driverName
		curInitCSIDriver := initCSIDriver

		Context(fmt.Sprintf("CSI plugin test using CSI driver: %s", curDriverName), func() {
			var (
				driver csiTestDriver
			)

			BeforeEach(func() {
				driver = curInitCSIDriver(f, config)
				driver.createCSIDriver()
			})

			AfterEach(func() {
				driver.cleanupCSIDriver()
			})

			It("should provision storage", func() {
				t := driver.createStorageClassTest(node)
				claim := newClaim(t, ns.GetName(), "")
				class := newStorageClass(t, ns.GetName(), "")
				claim.Spec.StorageClassName = &class.ObjectMeta.Name
				testDynamicProvisioning(t, cs, claim, class)
			})
		})
	}
})

type hostpathCSIDriver struct {
	combinedClusterRoleNames []string
	serviceAccount           *v1.ServiceAccount

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initCSIHostpath(f *framework.Framework, config framework.VolumeTestConfig) csiTestDriver {
	return &hostpathCSIDriver{
		combinedClusterRoleNames: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
			csiDriverRegistrarClusterRoleName,
		},
		f:      f,
		config: config,
	}
}

func (h *hostpathCSIDriver) createStorageClassTest(node v1.Node) storageClassTest {
	return storageClassTest{
		name:         "csi-hostpath",
		provisioner:  "csi-hostpath",
		parameters:   map[string]string{},
		claimSize:    "1Gi",
		expectedSize: "1Gi",
		nodeName:     node.Name,
	}
}

func (h *hostpathCSIDriver) createCSIDriver() {
	By("deploying csi hostpath driver")
	f := h.f
	cs := f.ClientSet
	config := h.config
	h.serviceAccount = csiServiceAccount(cs, config, "hostpath", false)
	csiClusterRoleBindings(cs, config, false, h.serviceAccount, h.combinedClusterRoleNames)
	csiHostPathPod(cs, config, false, f, h.serviceAccount)
}

func (h *hostpathCSIDriver) cleanupCSIDriver() {
	By("uninstalling csi hostpath driver")
	f := h.f
	cs := f.ClientSet
	config := h.config
	csiHostPathPod(cs, config, true, f, h.serviceAccount)
	csiClusterRoleBindings(cs, config, true, h.serviceAccount, h.combinedClusterRoleNames)
	csiServiceAccount(cs, config, "hostpath", true)
}

type gcePDCSIDriver struct {
	controllerClusterRoles   []string
	nodeClusterRoles         []string
	controllerServiceAccount *v1.ServiceAccount
	nodeServiceAccount       *v1.ServiceAccount

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initCSIgcePD(f *framework.Framework, config framework.VolumeTestConfig) csiTestDriver {
	cs := f.ClientSet
	framework.SkipUnlessProviderIs("gce", "gke")
	// Currently you will need to manually add the required GCP Credentials as a secret "cloud-sa"
	// kubectl create generic cloud-sa --from-file=PATH/TO/cloud-sa.json --namespace={{config.Namespace}}
	// TODO(#62561): Inject the necessary credentials automatically to the driver containers in e2e test
	framework.SkipUnlessSecretExistsAfterWait(cs, "cloud-sa", config.Namespace, 3*time.Minute)

	return &gcePDCSIDriver{
		nodeClusterRoles: []string{
			csiDriverRegistrarClusterRoleName,
		},
		controllerClusterRoles: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
		},
		f:      f,
		config: config,
	}
}

func (g *gcePDCSIDriver) createStorageClassTest(node v1.Node) storageClassTest {
	nodeZone, ok := node.GetLabels()[kubeletapis.LabelZoneFailureDomain]
	Expect(ok).To(BeTrue(), "Could not get label %v from node %v", kubeletapis.LabelZoneFailureDomain, node.GetName())

	return storageClassTest{
		name:         "csi-gce-pd",
		provisioner:  "csi-gce-pd",
		parameters:   map[string]string{"type": "pd-standard", "zone": nodeZone},
		claimSize:    "5Gi",
		expectedSize: "5Gi",
		nodeName:     node.Name,
	}
}

func (g *gcePDCSIDriver) createCSIDriver() {
	By("deploying gce-pd driver")
	f := g.f
	cs := f.ClientSet
	config := g.config
	g.controllerServiceAccount = csiServiceAccount(cs, config, "gce-controller", false /* teardown */)
	g.nodeServiceAccount = csiServiceAccount(cs, config, "gce-node", false /* teardown */)
	csiClusterRoleBindings(cs, config, false /* teardown */, g.controllerServiceAccount, g.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, false /* teardown */, g.nodeServiceAccount, g.nodeClusterRoles)
	deployGCEPDCSIDriver(cs, config, false /* teardown */, f, g.nodeServiceAccount, g.controllerServiceAccount)
}

func (g *gcePDCSIDriver) cleanupCSIDriver() {
	By("uninstalling gce-pd driver")
	f := g.f
	cs := f.ClientSet
	config := g.config
	deployGCEPDCSIDriver(cs, config, true /* teardown */, f, g.nodeServiceAccount, g.controllerServiceAccount)
	csiClusterRoleBindings(cs, config, true /* teardown */, g.controllerServiceAccount, g.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, true /* teardown */, g.nodeServiceAccount, g.nodeClusterRoles)
	csiServiceAccount(cs, config, "gce-controller", true /* teardown */)
	csiServiceAccount(cs, config, "gce-node", true /* teardown */)
}

type rbdCSIDriver struct {
	controllerClusterRoles   []string
	nodeClusterRoles         []string
	controllerServiceAccount *v1.ServiceAccount
	nodeServiceAccount       *v1.ServiceAccount
	serverPod                *v1.Pod
	secret                   *v1.Secret
	serverIP                 string

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initCSIrbd(f *framework.Framework, config framework.VolumeTestConfig) csiTestDriver {
	return &rbdCSIDriver{
		nodeClusterRoles: []string{
			csiDriverRegistrarClusterRoleName,
		},
		controllerClusterRoles: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
			csiDriverSecretAccessClusterRoleName,
		},

		f:      f,
		config: config,
	}
}

func (r *rbdCSIDriver) createStorageClassTest(node v1.Node) storageClassTest {
	return storageClassTest{
		name:        "csi-rbdplugin",
		provisioner: "csi-rbdplugin",
		parameters: map[string]string{
			"monitors": r.serverIP,
			"pool":     "rbd",
			"csiProvisionerSecretName":      r.secret.Name,
			"csiProvisionerSecretNamespace": r.config.Namespace,
		},
		claimSize:          "1Gi",
		expectedSize:       "1Gi",
		nodeName:           node.Name,
		skipWriteReadCheck: true,
	}
}

func (r *rbdCSIDriver) createCSIDriver() {
	By("deploying csi rbd driver")
	f := r.f
	cs := f.ClientSet
	config := r.config
	r.controllerServiceAccount = csiServiceAccount(cs, config, "rbd-controller", false /* setup */)
	r.nodeServiceAccount = csiServiceAccount(cs, config, "rbd-node", false /* setup */)
	csiClusterRoleBindings(cs, config, false /* setup */, r.controllerServiceAccount, r.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, false /* setup */, r.nodeServiceAccount, r.nodeClusterRoles)
	deployRbdCSIDriver(cs, config, false /* setup */, f, r.nodeServiceAccount, r.controllerServiceAccount, r)
}

func (r *rbdCSIDriver) cleanupCSIDriver() {
	By("uninstalling csi rbd driver")
	f := r.f
	cs := f.ClientSet
	config := r.config
	deployRbdCSIDriver(cs, config, true /* teardown */, f, r.nodeServiceAccount, r.controllerServiceAccount, r)
	csiClusterRoleBindings(cs, config, true /* teardown */, r.controllerServiceAccount, r.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, true /* teardown */, r.nodeServiceAccount, r.nodeClusterRoles)
	csiServiceAccount(cs, config, "rbd-controller", true /* teardown */)
	csiServiceAccount(cs, config, "rbd-node", true /* teardown */)
}

type cephfsCSIDriver struct {
	controllerClusterRoles   []string
	nodeClusterRoles         []string
	controllerServiceAccount *v1.ServiceAccount
	nodeServiceAccount       *v1.ServiceAccount
	serverPod                *v1.Pod
	secret                   *v1.Secret
	serverIP                 string

	f      *framework.Framework
	config framework.VolumeTestConfig
}

func initCSIcephfs(f *framework.Framework, config framework.VolumeTestConfig) csiTestDriver {
	return &cephfsCSIDriver{
		nodeClusterRoles: []string{
			csiDriverRegistrarClusterRoleName,
		},
		controllerClusterRoles: []string{
			csiExternalAttacherClusterRoleName,
			csiExternalProvisionerClusterRoleName,
			csiDriverSecretAccessClusterRoleName,
		},

		f:      f,
		config: config,
	}
}

func (c *cephfsCSIDriver) createStorageClassTest(node v1.Node) storageClassTest {
	return storageClassTest{
		name:        "csi-cephfsplugin",
		provisioner: "csi-cephfsplugin",
		parameters: map[string]string{
			"monitors":        c.serverIP,
			"provisionVolume": "true",
			"pool":            "cephfs_data",
			"csiProvisionerSecretName":      c.secret.Name,
			"csiProvisionerSecretNamespace": c.config.Namespace,
		},
		claimSize:          "1Gi",
		expectedSize:       "1Gi",
		nodeName:           node.Name,
		skipWriteReadCheck: true,
	}
}

func (c *cephfsCSIDriver) createCSIDriver() {
	By("deploying csi ceph driver")
	f := c.f
	cs := f.ClientSet
	config := c.config
	c.controllerServiceAccount = csiServiceAccount(cs, config, "ceph-controller", false /* setup */)
	c.nodeServiceAccount = csiServiceAccount(cs, config, "ceph-node", false /* setup */)
	csiClusterRoleBindings(cs, config, false /* setup */, c.controllerServiceAccount, c.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, false /* setup */, c.nodeServiceAccount, c.nodeClusterRoles)
	deployCephfsCSIDriver(cs, config, false /* setup */, f, c.nodeServiceAccount, c.controllerServiceAccount, c)
}

func (c *cephfsCSIDriver) cleanupCSIDriver() {
	By("uninstalling csi ceph driver")
	f := c.f
	cs := f.ClientSet
	config := c.config
	deployCephfsCSIDriver(cs, config, true /* teardown */, f, c.nodeServiceAccount, c.controllerServiceAccount, c)
	csiClusterRoleBindings(cs, config, true /* teardown */, c.controllerServiceAccount, c.controllerClusterRoles)
	csiClusterRoleBindings(cs, config, true /* teardown */, c.nodeServiceAccount, c.nodeClusterRoles)
	csiServiceAccount(cs, config, "ceph-controller", true /* teardown */)
	csiServiceAccount(cs, config, "ceph-node", true /* teardown */)
}
