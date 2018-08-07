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

// This file is used to deploy the CSI hostPath plugin
// More Information: https://github.com/kubernetes-csi/drivers/tree/master/pkg/hostpath

package storage

import (
	"fmt"
	"time"

	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/manifest"
	imageutils "k8s.io/kubernetes/test/utils/image"

	. "github.com/onsi/ginkgo"
)

var csiImageVersions = map[string]string{
	"hostpathplugin":   "v0.2.0",
	"csi-attacher":     "v0.2.0",
	"csi-provisioner":  "v0.2.1",
	"driver-registrar": "v0.2.0",
}

func csiContainerImage(image string) string {
	var fullName string
	fullName += framework.TestContext.CSIImageRegistry + "/" + image + ":"
	if framework.TestContext.CSIImageVersion != "" {
		fullName += framework.TestContext.CSIImageVersion
	} else {
		fullName += csiImageVersions[image]
	}
	return fullName
}

func createClusterRole(
	role *rbacv1.ClusterRole,
) *rbacv1.ClusterRole {
	// TODO(Issue: #62237) Remove impersonation workaround and cluster role when issue resolved
	By("Creating an impersonating superuser kubernetes clientset to define cluster role")
	rc, err := framework.LoadConfig()
	framework.ExpectNoError(err)
	rc.Impersonate = restclient.ImpersonationConfig{
		UserName: "superuser",
		Groups:   []string{"system:masters"},
	}
	superuserClientset, err := clientset.NewForConfig(rc)
	framework.ExpectNoError(err, "Failed to create superuser clientset: %v", err)
	By(fmt.Sprintf("Creating the %s cluster role", role.ObjectMeta.Name))
	clusterRoleClient := superuserClientset.RbacV1().ClusterRoles()

	ret, err := clusterRoleClient.Create(role)
	if err != nil {
		if apierrs.IsAlreadyExists(err) {
			return ret
		}
		framework.ExpectNoError(err, "Failed to create %s cluster role: %v", role.GetName(), err)
	}

	return ret
}

// Create the driver registrar cluster role if it doesn't exist, no teardown so that tests
// are parallelizable. This role will be shared with many of the CSI tests.
func csiDriverRegistrarClusterRole(
	config framework.VolumeTestConfig,
) *rbacv1.ClusterRole {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiDriverRegistrarClusterRoleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}

	return createClusterRole(role)
}

// Create additional cluster role for provisioners which need to access secrets
func csiDriverSecretAccessClusterRole(
	config framework.VolumeTestConfig,
) *rbacv1.ClusterRole {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiDriverSecretAccessClusterRoleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list"},
			},
		},
	}

	return createClusterRole(role)
}

func csiServiceAccount(
	client clientset.Interface,
	config framework.VolumeTestConfig,
	componentName string,
	teardown bool,
) *v1.ServiceAccount {
	creatingString := "Creating"
	if teardown {
		creatingString = "Deleting"
	}
	By(fmt.Sprintf("%v a CSI service account for %v", creatingString, componentName))
	serviceAccountName := config.Prefix + "-" + componentName + "-service-account"
	serviceAccountClient := client.CoreV1().ServiceAccounts(config.Namespace)
	sa := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: serviceAccountName,
		},
	}

	serviceAccountClient.Delete(sa.GetName(), &metav1.DeleteOptions{})
	err := wait.Poll(2*time.Second, 10*time.Minute, func() (bool, error) {
		_, err := serviceAccountClient.Get(sa.GetName(), metav1.GetOptions{})
		return apierrs.IsNotFound(err), nil
	})
	framework.ExpectNoError(err, "Timed out waiting for deletion: %v", err)

	if teardown {
		return nil
	}

	ret, err := serviceAccountClient.Create(sa)
	if err != nil {
		framework.ExpectNoError(err, "Failed to create %s service account: %v", sa.GetName(), err)
	}

	return ret
}

func csiClusterRoleBindings(
	client clientset.Interface,
	config framework.VolumeTestConfig,
	teardown bool,
	sa *v1.ServiceAccount,
	clusterRolesNames []string,
) {
	bindingString := "Binding"
	if teardown {
		bindingString = "Unbinding"
	}
	By(fmt.Sprintf("%v cluster roles %v to the CSI service account %v", bindingString, clusterRolesNames, sa.GetName()))
	clusterRoleBindingClient := client.RbacV1().ClusterRoleBindings()
	for _, clusterRoleName := range clusterRolesNames {

		binding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: config.Prefix + "-" + clusterRoleName + "-" + config.Namespace + "-role-binding",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      sa.GetName(),
					Namespace: sa.GetNamespace(),
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     clusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		}

		clusterRoleBindingClient.Delete(binding.GetName(), &metav1.DeleteOptions{})
		err := wait.Poll(2*time.Second, 10*time.Minute, func() (bool, error) {
			_, err := clusterRoleBindingClient.Get(binding.GetName(), metav1.GetOptions{})
			return apierrs.IsNotFound(err), nil
		})
		framework.ExpectNoError(err, "Timed out waiting for deletion: %v", err)

		if teardown {
			return
		}

		_, err = clusterRoleBindingClient.Create(binding)
		if err != nil {
			framework.ExpectNoError(err, "Failed to create %s role binding: %v", binding.GetName(), err)
		}
	}
}

func csiHostPathPod(
	client clientset.Interface,
	config framework.VolumeTestConfig,
	teardown bool,
	f *framework.Framework,
	sa *v1.ServiceAccount,
) *v1.Pod {
	podClient := client.CoreV1().Pods(config.Namespace)

	priv := true
	mountPropagation := v1.MountPropagationBidirectional
	hostPathType := v1.HostPathDirectoryOrCreate
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.Prefix + "-pod",
			Namespace: config.Namespace,
			Labels: map[string]string{
				"app": "hostpath-driver",
			},
		},
		Spec: v1.PodSpec{
			ServiceAccountName: sa.GetName(),
			NodeName:           config.ServerNodeName,
			RestartPolicy:      v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:            "external-provisioner",
					Image:           csiContainerImage("csi-provisioner"),
					ImagePullPolicy: v1.PullAlways,
					Args: []string{
						"--v=5",
						"--provisioner=csi-hostpath",
						"--csi-address=/csi/csi.sock",
					},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "socket-dir",
							MountPath: "/csi",
						},
					},
				},
				{
					Name:            "driver-registrar",
					Image:           csiContainerImage("driver-registrar"),
					ImagePullPolicy: v1.PullAlways,
					Args: []string{
						"--v=5",
						"--csi-address=/csi/csi.sock",
					},
					Env: []v1.EnvVar{
						{
							Name: "KUBE_NODE_NAME",
							ValueFrom: &v1.EnvVarSource{
								FieldRef: &v1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
					},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "socket-dir",
							MountPath: "/csi",
						},
					},
				},
				{
					Name:            "external-attacher",
					Image:           csiContainerImage("csi-attacher"),
					ImagePullPolicy: v1.PullAlways,
					Args: []string{
						"--v=5",
						"--csi-address=$(ADDRESS)",
					},
					Env: []v1.EnvVar{
						{
							Name:  "ADDRESS",
							Value: "/csi/csi.sock",
						},
					},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "socket-dir",
							MountPath: "/csi"},
					},
				},
				{
					Name:            "hostpath-driver",
					Image:           csiContainerImage("hostpathplugin"),
					ImagePullPolicy: v1.PullAlways,
					SecurityContext: &v1.SecurityContext{
						Privileged: &priv,
					},
					Args: []string{
						"--v=5",
						"--endpoint=$(CSI_ENDPOINT)",
						"--nodeid=$(KUBE_NODE_NAME)",
					},
					Env: []v1.EnvVar{
						{
							Name:  "CSI_ENDPOINT",
							Value: "unix://" + "/csi/csi.sock",
						},
						{
							Name: "KUBE_NODE_NAME",
							ValueFrom: &v1.EnvVarSource{
								FieldRef: &v1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
					},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "socket-dir",
							MountPath: "/csi",
						},
						{
							Name:             "mountpoint-dir",
							MountPath:        "/var/lib/kubelet/pods",
							MountPropagation: &mountPropagation,
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "socket-dir",
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: "/var/lib/kubelet/plugins/csi-hostpath",
							Type: &hostPathType,
						},
					},
				},
				{
					Name: "mountpoint-dir",
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: "/var/lib/kubelet/pods",
							Type: &hostPathType,
						},
					},
				},
			},
		},
	}

	err := framework.DeletePodWithWait(f, client, pod)
	framework.ExpectNoError(err, "Failed to delete pod %s/%s: %v",
		pod.GetNamespace(), pod.GetName(), err)

	if teardown {
		return nil
	}

	ret, err := podClient.Create(pod)
	if err != nil {
		framework.ExpectNoError(err, "Failed to create %q pod: %v", pod.GetName(), err)
	}

	// Wait for pod to come up
	framework.ExpectNoError(framework.WaitForPodRunningInNamespace(client, ret))
	return ret
}

func deployCSIDriver(
	client clientset.Interface,
	teardown bool,
	namespace string,
	nodeSA *v1.ServiceAccount,
	controllerSA *v1.ServiceAccount,
	daemonSetManifest string,
	statefulSetManifest string,
	serviceManifset string,
) {
	// Get API Objects from manifests
	nodeds, err := manifest.DaemonSetFromManifest(daemonSetManifest, namespace)
	framework.ExpectNoError(err, "Failed to create DaemonSet from manifest")
	nodeds.Spec.Template.Spec.ServiceAccountName = nodeSA.GetName()

	controllerss, err := manifest.StatefulSetFromManifest(statefulSetManifest, namespace)
	framework.ExpectNoError(err, "Failed to create StatefulSet from manifest")
	controllerss.Spec.Template.Spec.ServiceAccountName = controllerSA.GetName()

	controllerservice, err := manifest.SvcFromManifest(serviceManifset)
	framework.ExpectNoError(err, "Failed to create Service from manifest")

	// Got all objects from manifests now try to delete objects
	err = client.CoreV1().Services(namespace).Delete(controllerservice.GetName(), nil)
	if err != nil {
		if !apierrs.IsNotFound(err) {
			framework.ExpectNoError(err, "Failed to delete Service: %v", controllerservice.GetName())
		}
	}

	err = client.AppsV1().StatefulSets(namespace).Delete(controllerss.Name, nil)
	if err != nil {
		if !apierrs.IsNotFound(err) {
			framework.ExpectNoError(err, "Failed to delete StatefulSet: %v", controllerss.GetName())
		}
	}
	err = client.AppsV1().DaemonSets(namespace).Delete(nodeds.Name, nil)
	if err != nil {
		if !apierrs.IsNotFound(err) {
			framework.ExpectNoError(err, "Failed to delete DaemonSet: %v", nodeds.GetName())
		}
	}
	if teardown {
		return
	}

	// Create new API Objects through client
	_, err = client.CoreV1().Services(namespace).Create(controllerservice)
	framework.ExpectNoError(err, "Failed to create Service: %v", controllerservice.Name)

	_, err = client.AppsV1().StatefulSets(namespace).Create(controllerss)
	framework.ExpectNoError(err, "Failed to create StatefulSet: %v", controllerss.Name)

	_, err = client.AppsV1().DaemonSets(namespace).Create(nodeds)
	framework.ExpectNoError(err, "Failed to create DaemonSet: %v", nodeds.Name)
}

func deployGCEPDCSIDriver(
	client clientset.Interface,
	config framework.VolumeTestConfig,
	teardown bool,
	f *framework.Framework,
	nodeSA *v1.ServiceAccount,
	controllerSA *v1.ServiceAccount,
) {
	deployCSIDriver(client, teardown, config.Namespace, nodeSA, controllerSA,
		"test/e2e/testing-manifests/storage-csi/gce-pd/node_ds.yaml",
		"test/e2e/testing-manifests/storage-csi/gce-pd/controller_ss.yaml",
		"test/e2e/testing-manifests/storage-csi/gce-pd/controller_service.yaml")
}

func deployCephServer(
	client clientset.Interface,
	f *framework.Framework,
	namespace string,
) (*v1.Pod, string) {
	cephConfig := framework.VolumeTestConfig{
		Namespace:   namespace,
		Prefix:      "ceph",
		ServerImage: imageutils.GetE2EImage(imageutils.VolumeRBDServer),
		ServerPorts: []int{6789},
		ServerVolumes: map[string]string{
			"/lib/modules": "/lib/modules",
		},
		ServerReadyMessage: "Ceph is ready",
	}
	serverPod, serverIP := framework.CreateStorageServer(client, cephConfig)
	return serverPod, serverIP
}

func deleteCephEnvironment(
	client clientset.Interface,
	f *framework.Framework,
	namespace string,
	secretName string,
	serverPod *v1.Pod,
) {
	secErr := client.CoreV1().Secrets(namespace).Delete(secretName, &metav1.DeleteOptions{})
	err := framework.DeletePodWithWait(f, client, serverPod)
	if secErr != nil || err != nil {
		if secErr != nil {
			framework.Logf("Ceph server secret delete failed: %v", secErr)
		}
		if err != nil {
			framework.Logf("Ceph server pod delete failed: %v", err)
		}
		framework.Failf("Ceph server cleanup failed")
	}
}

func deploySecret(
	client clientset.Interface,
	namespace string, secretName string,
	data map[string][]byte,
) *v1.Secret {
	secret := &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName,
		},
		Data: data,
	}

	secret, err := client.CoreV1().Secrets(namespace).Create(secret)
	if err != nil {
		framework.Logf("Failed to create secret %s: %v", secretName, err)
	}

	return secret
}

func deployRbdSecret(client clientset.Interface, namespace string) *v1.Secret {
	secretName := "rbd-secret"
	data := map[string][]byte{
		// from test/images/volumes-tester/rbd/keyring
		"admin": []byte("AQDRrKNVbEevChAAEmRC+pW/KBVHxa0w/POILA=="),
	}
	return deploySecret(client, namespace, secretName, data)
}

func deployCephfsSecret(client clientset.Interface, namespace string) *v1.Secret {
	secretName := "cephfs-secret"
	data := map[string][]byte{
		"adminID": []byte("admin"),
		// from test/images/volumes-tester/rbd/keyring
		"adminKey": []byte("AQDRrKNVbEevChAAEmRC+pW/KBVHxa0w/POILA=="),
	}
	return deploySecret(client, namespace, secretName, data)
}

func deployRbdCSIDriver(
	client clientset.Interface,
	config framework.VolumeTestConfig,
	teardown bool,
	f *framework.Framework,
	nodeSA *v1.ServiceAccount,
	controllerSA *v1.ServiceAccount,
	r *rbdCSIDriver,
) {
	deployCSIDriver(client, teardown, config.Namespace, nodeSA, controllerSA,
		"test/e2e/testing-manifests/storage-csi/rbd/node_ds.yaml",
		"test/e2e/testing-manifests/storage-csi/rbd/controller_ss.yaml",
		"test/e2e/testing-manifests/storage-csi/rbd/controller_service.yaml")

	if teardown {
		deleteCephEnvironment(client, f, config.Namespace, r.secret.Name, r.serverPod)
	} else {
		r.serverPod, r.serverIP = deployCephServer(client, f, config.Namespace)
		r.secret = deployRbdSecret(client, config.Namespace)
	}
}

func deployCephfsCSIDriver(
	client clientset.Interface,
	config framework.VolumeTestConfig,
	teardown bool,
	f *framework.Framework,
	nodeSA *v1.ServiceAccount,
	controllerSA *v1.ServiceAccount,
	c *cephfsCSIDriver,
) {
	deployCSIDriver(client, teardown, config.Namespace, nodeSA, controllerSA,
		"test/e2e/testing-manifests/storage-csi/cephfs/node_ds.yaml",
		"test/e2e/testing-manifests/storage-csi/cephfs/controller_ss.yaml",
		"test/e2e/testing-manifests/storage-csi/cephfs/controller_service.yaml")

	if teardown {
		deleteCephEnvironment(client, f, config.Namespace, c.secret.Name, c.serverPod)
	} else {
		c.serverPod, c.serverIP = deployCephServer(client, f, config.Namespace)
		c.secret = deployCephfsSecret(client, config.Namespace)
	}
}
