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

package drivers

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"

	"k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"

	. "github.com/onsi/ginkgo"

	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	csicrd "k8s.io/csi-api/pkg/crd"
)

var (
	csiImageVersion  = flag.String("storage.csi.image.version", "", "overrides the default tag used for hostpathplugin/csi-attacher/csi-provisioner/driver-registrar images")
	csiImageRegistry = flag.String("storage.csi.image.registry", "quay.io/k8scsi", "overrides the default repository used for hostpathplugin/csi-attacher/csi-provisioner/driver-registrar images")
	csiImageVersions = map[string]string{
		"hostpathplugin":   "v0.4.0",
		"csi-attacher":     "v0.4.0",
		"csi-provisioner":  "v0.4.0",
		"driver-registrar": "v0.4.0",
	}
)

func csiContainerImage(image string) string {
	var fullName string
	fullName += *csiImageRegistry + "/" + image + ":"
	if *csiImageVersion != "" {
		fullName += *csiImageVersion
	} else {
		fullName += csiImageVersions[image]
	}
	return fullName
}

func CreateCSICRDs(c apiextensionsclient.Interface) {
	By("Creating CSI CRDs")
	crds := []*apiextensionsv1beta1.CustomResourceDefinition{
		csicrd.CSIDriverCRD(),
		csicrd.CSINodeInfoCRD(),
	}

	for _, crd := range crds {
		_, err := c.ApiextensionsV1beta1().CustomResourceDefinitions().Get(crd.Name, metav1.GetOptions{})
		if err == nil {
			continue
		} else if !apierrs.IsNotFound(err) {
			framework.ExpectNoError(err, "Failed to check for existing of CSI CRD %q: %v", crd.Name, err)
		}
		_, err = c.ApiextensionsV1beta1().CustomResourceDefinitions().Create(crd)
		framework.ExpectNoError(err, "Failed to create CSI CRD %q: %v", crd.Name, err)
	}
}

func shredFile(filePath string) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		framework.Logf("File %v was not found, skipping shredding", filePath)
		return
	}
	framework.Logf("Shredding file %v", filePath)
	_, _, err := framework.RunCmd("shred", "--remove", filePath)
	if err != nil {
		framework.Logf("Failed to shred file %v: %v", filePath, err)
	}
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		framework.Logf("File %v successfully shredded", filePath)
		return
	}
	// Shred failed Try to remove the file for good meausure
	err = os.Remove(filePath)
	framework.ExpectNoError(err, "Failed to remove service account file %s", filePath)

}

// createGCESecrets downloads the GCP IAM Key for the default compute service account
// and puts it in a secret for the GCE PD CSI Driver to consume
func createGCESecrets(client clientset.Interface, config framework.VolumeTestConfig) {
	saEnv := "E2E_GOOGLE_APPLICATION_CREDENTIALS"
	saFile := fmt.Sprintf("/tmp/%s/cloud-sa.json", string(uuid.NewUUID()))

	os.MkdirAll(path.Dir(saFile), 0750)
	defer os.Remove(path.Dir(saFile))

	premadeSAFile, ok := os.LookupEnv(saEnv)
	if !ok {
		framework.Logf("Could not find env var %v, please either create cloud-sa"+
			" secret manually or rerun test after setting %v to the filepath of"+
			" the GCP Service Account to give to the GCE Persistent Disk CSI Driver", saEnv, saEnv)
		return
	}

	framework.Logf("Found CI service account key at %v", premadeSAFile)
	// Need to copy it saFile
	stdout, stderr, err := framework.RunCmd("cp", premadeSAFile, saFile)
	framework.ExpectNoError(err, "error copying service account key: %s\nstdout: %s\nstderr: %s", err, stdout, stderr)
	defer shredFile(saFile)
	// Create Secret with this Service Account
	fileBytes, err := ioutil.ReadFile(saFile)
	framework.ExpectNoError(err, "Failed to read file %v", saFile)

	s := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloud-sa",
			Namespace: config.Namespace,
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			filepath.Base(saFile): fileBytes,
		},
	}

	_, err = client.CoreV1().Secrets(config.Namespace).Create(s)
	framework.ExpectNoError(err, "Failed to create Secret %v", s.GetName())
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
