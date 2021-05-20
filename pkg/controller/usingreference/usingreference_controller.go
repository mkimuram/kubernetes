/*
Copyright 2021 The Kubernetes Authors.
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

package usingreference

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	klog "k8s.io/klog/v2"
)

const (
	// UsingReferenceFinalizer is a finalizer for usingReference protection
	UsingReferenceFinalizer = "kubernetes.io/using-reference-protection"
	// ObjectIndex is an indexer key for object in the form of "{APIVersion}/{Kind}/{Namespace}/{Name}"
	ObjectIndex = "object-index"
	// UsingReferenceIndex is an indexer key for usingReference
	UsingReferenceIndex = "using-reference-index"
)

// Controller represents usingReferenceController
type Controller struct {
	client         clientset.Interface
	metadataClient metadata.Interface
	dynClient      dynamic.Interface

	metaLister       corelisters.SecretLister
	metaListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	indexer cache.Indexer

	restMapper meta.RESTMapper
}

// NewUsingReferenceController returns UsingReferenceController
func NewUsingReferenceController(
	cl clientset.Interface,
	metaCl metadata.Interface,
	dynCl dynamic.Interface,
	secretInformer coreinformers.SecretInformer,
	restMapper meta.RESTMapper,
) *Controller {
	c := &Controller{
		client:         cl,
		metadataClient: metaCl,
		dynClient:      dynCl,
		queue:          workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "usingreferenceprotection"),
		restMapper:     restMapper,
	}

	c.metaLister = secretInformer.Lister()
	c.metaListerSynced = secretInformer.Informer().HasSynced
	c.indexer = secretInformer.Informer().GetIndexer()

	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		// TODO: Optimize event handlings
		AddFunc: func(obj interface{}) {
			c.queue.Add(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.queue.Add(newObj)
			// TODO: Need to handle case that reference is removed from oldObj
		},
		DeleteFunc: func(obj interface{}) {
			c.queue.Add(obj)
		},
	})

	c.indexer.AddIndexers(cache.Indexers{
		ObjectIndex:         objectIndexFunc,
		UsingReferenceIndex: usingRefIndexFunc,
	})

	return c
}

func objectIndexFunc(obj interface{}) ([]string, error) {
	klog.Errorf("objectIndexFunc called: %v", obj)
	keys := []string{}
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return keys, fmt.Errorf("cannot access obj: %v", err)
	}

	keys = append(keys, accessorToKey(accessor))

	klog.Errorf("objectIndexFunc: %s/%s: keys: %v", accessor.GetNamespace(), accessor.GetName(), keys)
	return keys, nil
}

func usingRefIndexFunc(obj interface{}) ([]string, error) {
	klog.Errorf("usingRefIndexFunc called: %v", obj)
	keys := []string{}
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return keys, fmt.Errorf("cannot access obj: %v", err)
	}

	for _, ref := range accessor.GetUsingReferences() {
		keys = append(keys, referenceToKey(ref))
	}

	klog.Errorf("usingRefIndexFunc: %s/%s: keys: %v", accessor.GetNamespace(), accessor.GetName(), keys)
	return keys, nil
}

func referenceToKey(ref metav1.UsingReference) string {
	if ref.Namespace == "" {
		return fmt.Sprintf("%s/%s/%s", ref.APIVersion, ref.Kind, ref.Name)
	}

	return fmt.Sprintf("%s/%s/%s/%s", ref.APIVersion, ref.Kind, ref.Namespace, ref.Name)
}

func accessorToKey(ac metav1.Object) string {
	if ac.GetNamespace() == "" {
		return fmt.Sprintf("%s/%s", ac.GetResourceVersion(), ac.GetName())
	}

	return fmt.Sprintf("%s/%s/%s", ac.GetResourceVersion(), ac.GetNamespace(), ac.GetName())
}

// Run runs the controller goroutines.
func (c *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	klog.InfoS("Starting usingReference protection controller")
	defer klog.InfoS("Shutting down usingReference protection controller")

	if !cache.WaitForNamedCacheSync("usingReference protection", stopCh, c.metaListerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(obj)

	ac, err := meta.Accessor(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("cannot access obj: %v", err))
		return true
	}
	klog.Errorf("processNextWorkItem: %s/%s: started", ac.GetNamespace(), ac.GetName())

	for _, ref := range ac.GetUsingReferences() {
		refObj, err := c.getRefObj(ref)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("cannot access ref %s: %v", ref, err))
			continue
		}
		refAc, err := meta.Accessor(refObj)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("cannot access obj: %v", err))
			continue
		}
		if !c.hasUsingReferenceFinalizer(refAc) {
			// Add finalizer
			if err := c.addUsingReferenceFinalizer(refAc); err != nil {
				utilruntime.HandleError(fmt.Errorf("cannot add finalizer: %v", err))
				continue
			}
		}

		// TODO: This might cause issue when reference is cyclic
		klog.Errorf("processNextWorkItem: %s/%s: adding %v", ac.GetNamespace(), ac.GetName(), refObj)
		c.queue.Add(refObj)
	}

	// Check if the object is used
	isUsed, err := c.hasUsingObject(ac)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("cannot decide if objecte is used: %v", err))
		return true
	}
	if !isUsed {
		if ac.GetDeletionTimestamp() != nil && c.hasUsingReferenceFinalizer(ac) {
			// Remove finalizer
			if err := c.removeUsingReferenceFinalizer(ac); err != nil {
				utilruntime.HandleError(fmt.Errorf("cannot remove finalizer: %v", err))
				return true
			}
		}
	} else {
		if !c.hasUsingReferenceFinalizer(ac) {
			// Add finalizer
			if err := c.addUsingReferenceFinalizer(ac); err != nil {
				utilruntime.HandleError(fmt.Errorf("cannot add finalizer: %v", err))
				return true
			}
		}
	}

	c.queue.Forget(obj)
	klog.Errorf("processNextWorkItem: %s/%s: ended", ac.GetNamespace(), ac.GetName())

	return true
}

func (c *Controller) hasUsingReferenceFinalizer(ac metav1.Object) bool {
	for _, f := range ac.GetFinalizers() {
		if f == UsingReferenceFinalizer {
			return true
		}
	}
	return false
}

func (c *Controller) hasUsingObject(ac metav1.Object) (bool, error) {
	usingObjs, err := c.indexer.ByIndex(UsingReferenceIndex, accessorToKey(ac))
	if err != nil {
		return false, fmt.Errorf("cache-based list of objects failed while processing %s: %s", accessorToKey(ac), err.Error())
	}

	for _, usingObj := range usingObjs {
		acUsingObj, err := meta.Accessor(usingObj)
		if err != nil {
			return false, fmt.Errorf("cannot access obj: %v", err)
		}
		if isAccessorUsed(acUsingObj) {
			return true, nil
		}
	}

	// TODO: Consider also checking with API server efficiently

	return false, nil
}

func isAccessorUsed(ac metav1.Object) bool {
	for _, ref := range ac.GetUsingReferences() {
		if isAccessorUsedBy(ac, ref) {
			return true
		}
	}

	return false
}

func isAccessorUsedBy(ac metav1.Object, ref metav1.UsingReference) bool {
	return ac.GetResourceVersion() == ref.APIVersion &&
		ac.GetNamespace() == ref.Namespace &&
		ac.GetName() == ref.Name
}

func (c *Controller) addUsingReferenceFinalizer(ac metav1.Object) error {
	finalizers := ac.GetFinalizers()
	finalizers = append(finalizers, UsingReferenceFinalizer)
	return c.applyFinalizerPatch(ac, finalizers)
}

func (c *Controller) removeUsingReferenceFinalizer(ac metav1.Object) error {
	newFinalizers := []string{}
	for _, f := range ac.GetFinalizers() {
		if f != UsingReferenceFinalizer {
			newFinalizers = append(newFinalizers, f)
		}
	}
	return c.applyFinalizerPatch(ac, newFinalizers)
}

func (c *Controller) applyFinalizerPatch(ac metav1.Object, finalizers []string) error {
	fJSON, err := json.Marshal(finalizers)
	if err != nil {
		return err
	}
	patchStr := fmt.Sprintf("[{\"op\": \"replace\", \"path\": \"/metadata/finalizers\", \"value\": %s}]", fJSON)

	ta, err := meta.TypeAccessor(ac)
	if err != nil {
		return err
	}

	resource, namespaced, err := c.apiResource(ta.GetAPIVersion(), ta.GetKind())
	if err != nil {
		return err
	}

	if namespaced {
		_, err := c.metadataClient.Resource(resource).Namespace(ac.GetNamespace()).Patch(context.TODO(), ac.GetName(), types.JSONPatchType, []byte(patchStr), metav1.PatchOptions{})
		return err
	}

	_, err = c.metadataClient.Resource(resource).Namespace("").Patch(context.TODO(), ac.GetName(), types.JSONPatchType, []byte(patchStr), metav1.PatchOptions{})
	return err
}

// TODO: fixme (copied and modified from operations.go)
func (c *Controller) apiResource(apiVersion, kind string) (schema.GroupVersionResource, bool, error) {
	fqKind := schema.FromAPIVersionAndKind(apiVersion, kind)
	mapping, err := c.restMapper.RESTMapping(fqKind.GroupKind(), fqKind.Version)
	if err != nil {
		return schema.GroupVersionResource{}, false, err
	}

	return mapping.Resource, mapping.Scope == meta.RESTScopeNamespace, nil
}

func (c *Controller) getRefObj(ref metav1.UsingReference) (*unstructured.Unstructured, error) {
	resource, namespaced, err := c.apiResource(ref.APIVersion, ref.Kind)
	if err != nil {
		return nil, err
	}

	klog.Errorf("getRefObj: resource:%v namespaced:%v namespace: %v name:%v", resource, namespaced, ref.Namespace, ref.Name)

	if namespaced {
		return c.dynClient.Resource(resource).Namespace(ref.Namespace).Get(context.TODO(), ref.Name, metav1.GetOptions{})
	}

	return c.dynClient.Resource(resource).Namespace("").Get(context.TODO(), ref.Name, metav1.GetOptions{})
}
