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

package csi

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	api "k8s.io/api/core/v1"
	"k8s.io/api/storage/v1beta1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	fakeclient "k8s.io/client-go/kubernetes/fake"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/volume"
	volumetest "k8s.io/kubernetes/pkg/volume/testing"
)

func prepareBlockMapperTest(plug *csiPlugin, specVolumeName string, t *testing.T) (*csiBlockMapper, *volume.Spec, *api.PersistentVolume, error) {
	registerFakePlugin(testDriver, "endpoint", []string{"1.0.0"}, t)
	pv := makeTestPV(specVolumeName, 10, testDriver, testVol)
	spec := volume.NewSpecFromPersistentVolume(pv, pv.Spec.PersistentVolumeSource.CSI.ReadOnly)
	mapper, err := plug.NewBlockVolumeMapper(
		spec,
		&api.Pod{ObjectMeta: meta.ObjectMeta{UID: testPodUID, Namespace: testns}},
		volume.VolumeOptions{},
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Failed to make a new Mapper: %v", err)
	}
	csiMapper := mapper.(*csiBlockMapper)
	return csiMapper, spec, pv, nil
}

func prepareBlockUnmapperTest(plug *csiPlugin, specVolumeName string, t *testing.T) (*csiBlockMapper, *volume.Spec, *api.PersistentVolume, error) {
	registerFakePlugin(testDriver, "endpoint", []string{"1.0.0"}, t)
	pv := makeTestPV(specVolumeName, 10, testDriver, testVol)
	spec := volume.NewSpecFromPersistentVolume(pv, pv.Spec.PersistentVolumeSource.CSI.ReadOnly)

	// save volume data
	dir := getVolumeDeviceDataDir(pv.ObjectMeta.Name, plug.host)
	if err := os.MkdirAll(dir, 0755); err != nil && !os.IsNotExist(err) {
		t.Errorf("failed to create dir [%s]: %v", dir, err)
	}

	if err := saveVolumeData(
		dir,
		volDataFileName,
		map[string]string{
			volDataKey.specVolID:  pv.ObjectMeta.Name,
			volDataKey.driverName: testDriver,
			volDataKey.volHandle:  testVol,
		},
	); err != nil {
		t.Fatalf("failed to save volume data: %v", err)
	}

	unmapper, err := plug.NewBlockVolumeUnmapper(pv.ObjectMeta.Name, testPodUID)
	if err != nil {
		t.Fatalf("failed to make a new Unmapper: %v", err)
	}

	csiUnmapper := unmapper.(*csiBlockMapper)
	csiUnmapper.csiClient = setupClient(t, true)

	return csiUnmapper, spec, pv, nil
}

func TestBlockMapperGetGlobalMapPath(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	plug, tmpDir := newTestPlugin(t, nil)
	defer os.RemoveAll(tmpDir)

	// TODO (vladimirvivien) specName with slashes will not work
	testCases := []struct {
		name           string
		specVolumeName string
		path           string
	}{
		{
			name:           "simple specName",
			specVolumeName: "spec-0",
			path:           filepath.Join(tmpDir, fmt.Sprintf("plugins/kubernetes.io/csi/volumeDevices/%s/%s", "spec-0", "dev")),
		},
		{
			name:           "specName with dots",
			specVolumeName: "test.spec.1",
			path:           filepath.Join(tmpDir, fmt.Sprintf("plugins/kubernetes.io/csi/volumeDevices/%s/%s", "test.spec.1", "dev")),
		},
	}
	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		csiMapper, spec, _, err := prepareBlockMapperTest(plug, tc.specVolumeName, t)
		if err != nil {
			t.Fatalf("Failed to make a new Mapper: %v", err)
		}

		path, err := csiMapper.GetGlobalMapPath(spec)
		if err != nil {
			t.Errorf("mapper GetGlobalMapPath failed: %v", err)
		}

		if tc.path != path {
			t.Errorf("expecting path %s, got %s", tc.path, path)
		}
	}
}

func TestBlockMapperGetStagingPath(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	plug, tmpDir := newTestPlugin(t, nil)
	defer os.RemoveAll(tmpDir)

	testCases := []struct {
		name           string
		specVolumeName string
		path           string
	}{
		{
			name:           "simple specName",
			specVolumeName: "spec-0",
			path:           filepath.Join(tmpDir, fmt.Sprintf("plugins/kubernetes.io/csi/volumeDevices/staging/%s", "spec-0")),
		},
		{
			name:           "specName with dots",
			specVolumeName: "test.spec.1",
			path:           filepath.Join(tmpDir, fmt.Sprintf("plugins/kubernetes.io/csi/volumeDevices/staging/%s", "test.spec.1")),
		},
	}
	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		csiMapper, _, _, err := prepareBlockMapperTest(plug, tc.specVolumeName, t)
		if err != nil {
			t.Fatalf("Failed to make a new Mapper: %v", err)
		}

		path := csiMapper.getStagingPath()

		if tc.path != path {
			t.Errorf("expecting path %s, got %s", tc.path, path)
		}
	}
}

func TestBlockMapperGetPublishPath(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	plug, tmpDir := newTestPlugin(t, nil)
	defer os.RemoveAll(tmpDir)

	testCases := []struct {
		name           string
		specVolumeName string
		path           string
	}{
		{
			name:           "simple specName",
			specVolumeName: "spec-0",
			path:           filepath.Join(tmpDir, fmt.Sprintf("plugins/kubernetes.io/csi/volumeDevices/publish/%s/%s", "spec-0", testPodUID)),
		},
		{
			name:           "specName with dots",
			specVolumeName: "test.spec.1",
			path:           filepath.Join(tmpDir, fmt.Sprintf("plugins/kubernetes.io/csi/volumeDevices/publish/%s/%s", "test.spec.1", testPodUID)),
		},
	}
	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		csiMapper, _, _, err := prepareBlockMapperTest(plug, tc.specVolumeName, t)
		if err != nil {
			t.Fatalf("Failed to make a new Mapper: %v", err)
		}

		path := csiMapper.getPublishPath()

		if tc.path != path {
			t.Errorf("expecting path %s, got %s", tc.path, path)
		}
	}
}

func TestBlockMapperGetDeviceMapPath(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	plug, tmpDir := newTestPlugin(t, nil)
	defer os.RemoveAll(tmpDir)

	testCases := []struct {
		name           string
		specVolumeName string
		path           string
	}{
		{
			name:           "simple specName",
			specVolumeName: "spec-0",
			path:           filepath.Join(tmpDir, fmt.Sprintf("pods/%s/volumeDevices/kubernetes.io~csi", testPodUID)),
		},
		{
			name:           "specName with dots",
			specVolumeName: "test.spec.1",
			path:           filepath.Join(tmpDir, fmt.Sprintf("pods/%s/volumeDevices/kubernetes.io~csi", testPodUID)),
		},
	}
	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		csiMapper, _, _, err := prepareBlockMapperTest(plug, tc.specVolumeName, t)
		if err != nil {
			t.Fatalf("Failed to make a new Mapper: %v", err)
		}

		path, volName := csiMapper.GetPodDeviceMapPath()

		if tc.path != path {
			t.Errorf("expecting path %s, got %s", tc.path, path)
		}

		if tc.specVolumeName != volName {
			t.Errorf("expecting volName %s, got %s", tc.specVolumeName, volName)
		}
	}
}

func TestBlockMapperSetupDevice(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	plug, tmpDir := newTestPlugin(t, nil)
	defer os.RemoveAll(tmpDir)
	fakeClient := fakeclient.NewSimpleClientset()
	host := volumetest.NewFakeVolumeHostWithCSINodeName(
		tmpDir,
		fakeClient,
		nil,
		"fakeNode",
		nil,
	)
	plug.host = host

	csiMapper, _, pv, err := prepareBlockMapperTest(plug, "test-pv", t)
	if err != nil {
		t.Fatalf("Failed to make a new Mapper: %v", err)
	}

	pvName := pv.GetName()
	nodeName := string(plug.host.GetNodeName())

	csiMapper.csiClient = setupClient(t, true)

	attachID := getAttachmentName(csiMapper.volumeID, string(csiMapper.driverName), string(nodeName))
	attachment := makeTestAttachment(attachID, nodeName, pvName)
	attachment.Status.Attached = true
	_, err = csiMapper.k8s.StorageV1().VolumeAttachments().Create(attachment)
	if err != nil {
		t.Fatalf("failed to setup VolumeAttachment: %v", err)
	}
	t.Log("created attachement ", attachID)

	devicePath, err := csiMapper.SetUpDevice()
	if err != nil {
		t.Fatalf("mapper failed to SetupDevice: %v", err)
	}

	// Check if SetUpDevice returns the right path
	expectedDevicePath := csiMapper.getPublishPath()
	if devicePath != expectedDevicePath {
		t.Fatalf("mapper.SetupDevice returned unexpected path %s instead of %v", devicePath, expectedDevicePath)
	}

	// Check if NodeStageVolume staged the volume to the right path
	stagingPath := csiMapper.getStagingPath()
	svols := csiMapper.csiClient.(*fakeCsiDriverClient).nodeClient.GetNodeStagedVolumes()
	svol, ok := svols[csiMapper.volumeID]
	if !ok {
		t.Error("csi server may not have received NodeStageVolume call")
	}
	if svol.Path != stagingPath {
		t.Errorf("csi server expected device path %s, got %s", stagingPath, svol.Path)
	}
}

func TestBlockMapperMapPodDevice(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	plug, tmpDir := newTestPlugin(t, nil)
	defer os.RemoveAll(tmpDir)
	fakeClient := fakeclient.NewSimpleClientset()
	host := volumetest.NewFakeVolumeHostWithCSINodeName(
		tmpDir,
		fakeClient,
		nil,
		"fakeNode",
		nil,
	)
	plug.host = host

	csiMapper, _, pv, err := prepareBlockMapperTest(plug, "test-pv", t)
	if err != nil {
		t.Fatalf("Failed to make a new Mapper: %v", err)
	}

	pvName := pv.GetName()
	nodeName := string(plug.host.GetNodeName())

	csiMapper.csiClient = setupClient(t, true)

	attachID := getAttachmentName(csiMapper.volumeID, string(csiMapper.driverName), string(nodeName))
	attachment := makeTestAttachment(attachID, nodeName, pvName)
	attachment.Status.Attached = true
	_, err = csiMapper.k8s.StorageV1().VolumeAttachments().Create(attachment)
	if err != nil {
		t.Fatalf("failed to setup VolumeAttachment: %v", err)
	}
	t.Log("created attachement ", attachID)

	publishPath := csiMapper.getPublishPath()

	// Call MapPodDevice
	err = csiMapper.MapPodDevice()
	if err != nil {
		t.Fatalf("mapper failed to GetGlobalMapPath: %v", err)
	}

	// Check if NodePublishVolume published the volume to the right path
	pvols := csiMapper.csiClient.(*fakeCsiDriverClient).nodeClient.GetNodePublishedVolumes()
	pvol, ok := pvols[csiMapper.volumeID]
	if !ok {
		t.Error("csi server may not have received NodePublishVolume call")
	}
	if pvol.Path != publishPath {
		t.Errorf("csi server expected path %s, got %s", publishPath, pvol.Path)
	}
}

func TestBlockMapperMapPodDeviceNotSupportAttach(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIDriverRegistry, true)()

	fakeClient := fakeclient.NewSimpleClientset()
	attachRequired := false
	fakeDriver := &v1beta1.CSIDriver{
		ObjectMeta: meta.ObjectMeta{
			Name: testDriver,
		},
		Spec: v1beta1.CSIDriverSpec{
			AttachRequired: &attachRequired,
		},
	}
	_, err := fakeClient.StorageV1beta1().CSIDrivers().Create(fakeDriver)
	if err != nil {
		t.Fatalf("Failed to create a fakeDriver: %v", err)
	}

	// after the driver is created, create the plugin. newTestPlugin waits for the informer to sync,
	// such that csiMapper.SetUpDevice below sees the VolumeAttachment object in the lister.

	plug, tmpDir := newTestPlugin(t, fakeClient)
	defer os.RemoveAll(tmpDir)

	host := volumetest.NewFakeVolumeHostWithCSINodeName(
		tmpDir,
		fakeClient,
		nil,
		"fakeNode",
		plug.csiDriverLister,
	)
	plug.host = host
	csiMapper, _, _, err := prepareBlockMapperTest(plug, "test-pv", t)
	if err != nil {
		t.Fatalf("Failed to make a new Mapper: %v", err)
	}
	csiMapper.csiClient = setupClient(t, true)
	publishPath := csiMapper.getPublishPath()

	// Call MapPodDevice
	err = csiMapper.MapPodDevice()
	if err != nil {
		t.Fatalf("mapper failed to GetGlobalMapPath: %v", err)
	}

	// Check if NodePublishVolume published the volume to the right path
	pvols := csiMapper.csiClient.(*fakeCsiDriverClient).nodeClient.GetNodePublishedVolumes()
	pvol, ok := pvols[csiMapper.volumeID]
	if !ok {
		t.Error("csi server may not have received NodePublishVolume call")
	}
	if pvol.Path != publishPath {
		t.Errorf("csi server expected path %s, got %s", publishPath, pvol.Path)
	}
}

func TestBlockMapperTearDownDevice(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	testCases := []struct {
		name                    string
		shouldStagingPathExist  bool
		expectNodeUnstageCalled bool
	}{
		{
			name:                    "StagingPath exists and NodeUnstageVolume should be called",
			shouldStagingPathExist:  true,
			expectNodeUnstageCalled: true,
		},
		{
			name:                    "StagingPath doesn't exist and NodeUnstageVolume shouldn't be called",
			shouldStagingPathExist:  false,
			expectNodeUnstageCalled: false,
		},
	}
	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)

		plug, tmpDir := newTestPlugin(t, nil)
		defer os.RemoveAll(tmpDir)
		fakeClient := fakeclient.NewSimpleClientset()
		host := volumetest.NewFakeVolumeHostWithCSINodeName(
			tmpDir,
			fakeClient,
			nil,
			"fakeNode",
			nil,
		)
		plug.host = host

		csiUnmapper, spec, _, err := prepareBlockUnmapperTest(plug, "test-pv", t)
		if err != nil {
			t.Fatalf("Failed to make a new Mapper: %v", err)
		}

		globalMapPath, err := csiUnmapper.GetGlobalMapPath(spec)
		if err != nil {
			t.Fatalf("unmapper failed to GetGlobalMapPath: %v", err)
		}

		stagingPath := csiUnmapper.getStagingPath()
		if tc.shouldStagingPathExist {
			// Create dummy stagingPath which should be created on SetupDevice
			if err := os.MkdirAll(stagingPath, 0750); err != nil {
				t.Fatalf("unmapper failed to create dirctory for stagingPath(%s): %v", stagingPath, err)
			}
		} else {
			if _, err := os.Stat(stagingPath); err != nil {
				if !os.IsNotExist(err) {
					t.Fatalf("Checking if stagingPath %s exists failed: %v", stagingPath, err)
				}
				// Should reach this path (stagingPath not exist) for tc.shouldStagingPathExist == false case

			} else {
				t.Fatalf("StagingPath %s shouldn't exist for this test, but exists", stagingPath)
			}
		}

		// Add volume to fakeCsiDriverClient's nodeStagedVolumes to make it be able to check if node unstage is called later
		csiUnmapper.csiClient.(*fakeCsiDriverClient).nodeClient.AddNodeStagedVolume(csiUnmapper.volumeID, stagingPath, map[string]string{})

		err = csiUnmapper.TearDownDevice(globalMapPath, stagingPath)
		if err != nil {
			t.Fatal(err)
		}

		// ensure csi client call and node unstaged
		vols := csiUnmapper.csiClient.(*fakeCsiDriverClient).nodeClient.GetNodeStagedVolumes()

		// Existence of the key for vols means that node unstage is not called,
		// because fakeCsiDriverClient delete the key if it is called for the key
		_, notCalled := vols[csiUnmapper.volumeID]

		if tc.expectNodeUnstageCalled && notCalled {
			t.Error("csi server may not have received NodeUnstageVolume call")
		} else if !tc.expectNodeUnstageCalled && !notCalled {
			t.Error("csi server received NodeUnstageVolume call, but it shouldn't receive the call")
		}

	}
}

func TestBlockMapperUnmapPodDevice(t *testing.T) {
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CSIBlockVolume, true)()

	testCases := []struct {
		name                     string
		shouldPublishPathExist   bool
		expectNodeUnpblishCalled bool
	}{
		{
			name:                     "PublishPath exists and NodeUnpublishVolume should be called",
			shouldPublishPathExist:   true,
			expectNodeUnpblishCalled: true,
		},
		{
			name:                     "PublishPath doesn't exist and NodeUnpublishVolume shouldn't be called",
			shouldPublishPathExist:   false,
			expectNodeUnpblishCalled: false,
		},
	}
	for _, tc := range testCases {
		t.Logf("test case: %s", tc.name)
		plug, tmpDir := newTestPlugin(t, nil)
		defer os.RemoveAll(tmpDir)

		fakeClient := fakeclient.NewSimpleClientset()
		host := volumetest.NewFakeVolumeHostWithCSINodeName(
			tmpDir,
			fakeClient,
			nil,
			"fakeNode",
			nil,
		)
		plug.host = host

		csiUnmapper, _, _, err := prepareBlockUnmapperTest(plug, "test-pv", t)
		if err != nil {
			t.Fatalf("Failed to make a new Unmapper: %v", err)
		}

		publishPath := csiUnmapper.getPublishPath()
		if tc.shouldPublishPathExist {
			// Create dummy publishPath which should be created on MapPodDevice
			if err := os.MkdirAll(publishPath, 0750); err != nil {
				t.Fatalf("unmapper failed to create dirctory for publishPath(%s): %v", publishPath, err)
			}
		} else {
			if _, err := os.Stat(publishPath); err != nil {
				if !os.IsNotExist(err) {
					t.Fatalf("Checking if publishPath %s exists failed: %v", publishPath, err)
				}
				// Should reach this path (publishPath not exist) for tc.shouldPublishPathExist == false case

			} else {
				t.Fatalf("PublishPath %s shouldn't exist for this test, but exists", publishPath)
			}
		}

		// Add volume to fakeCsiDriverClient's nodePublishedVolumes to make it be able to check if node unpublish is called later
		csiUnmapper.csiClient.(*fakeCsiDriverClient).nodeClient.AddNodePublishedVolume(csiUnmapper.volumeID, publishPath, map[string]string{})

		// Call UnmapPodDevice
		err = csiUnmapper.UnmapPodDevice()
		if err != nil {
			t.Fatal(err)
		}

		// ensure csi client call and node unpublished
		pubs := csiUnmapper.csiClient.(*fakeCsiDriverClient).nodeClient.GetNodePublishedVolumes()

		// Existence of the key for pubs means that node unpublish is not called,
		// because fakeCsiDriverClient delete the key if it is called for the key
		_, notCalled := pubs[csiUnmapper.volumeID]

		if tc.expectNodeUnpblishCalled && notCalled {
			t.Error("csi server may not have received NodeUnpublishVolume call")
		} else if !tc.expectNodeUnpblishCalled && !notCalled {
			t.Error("csi server received NodeUnpublishVolume call, but it shouldn't receive the call")
		}
	}
}
