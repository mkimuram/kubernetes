/*
Copyright 2019 The Kubernetes Authors.

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

package proxy

import (
	"fmt"
	"net"
	"reflect"
	"sync"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	utilproxy "k8s.io/kubernetes/pkg/proxy/util"
)

// BaseEgressInfo contains base information that defines an egress.
// This could be used directly by proxier while processing egress,
// or can be used for constructing a more specific EgressInfo struct
// defined by the proxier if needed.
type BaseEgressInfo struct {
	IP net.IP
}

var _ EgressIP = &BaseEgressInfo{}

// String is part of EgressIP interface.
func (info *BaseEgressInfo) String() string {
	return fmt.Sprintf("%s", info.IP)
}

// IP is part of EgressIP interface.
func (info *BaseEgressInfo) IP() net.IP {
	return info.IP
}

func (ect *EgressChangeTracker) newBaseEgressInfo(egress *v1.Egress) *BaseEgressInfo {
	info := &BaseServiceInfo{
		IP: net.ParseIP(egress.Spec.IP),
	}

	return info
}

// egressChange contains all changes to egresses that happened since proxy rules were synced.  For a single object,
// changes are accumulated, i.e. previous is state from before applying the changes,
// current is state after applying all of the changes.
type egressChange struct {
	previous EgressMap
	current  EgressMap
}

// EgressChangeTracker carries state about uncommitted changes to an arbitrary number of
// Egresses, keyed by their namespace and name.
type EgressChangeTracker struct {
	// lock protects items.
	lock sync.Mutex
	// items maps an egress to its egressChange.
	items map[types.NamespacedName]*egressChange
}

// NewEgressChangeTracker initializes an EgressChangeTracker
func NewEgressChangeTracker() *EgressChangeTracker {
	return &EgressChangeTracker{
		items: make(map[types.NamespacedName]*egressChange),
	}
}

// Update updates given egress' change map based on the <previous, current> egress pair.  It returns true if items changed,
// otherwise return false.  Update can be used to add/update/delete items of EgressChangeMap.  For example,
// Add item
//   - pass <nil, egress> as the <previous, current> pair.
// Update item
//   - pass <oldEgress, egress> as the <previous, current> pair.
// Delete item
//   - pass <egress, nil> as the <previous, current> pair.
func (ect *EgressChangeTracker) Update(previous, current *v1.Egress) bool {
	egrs := current
	if egrs == nil {
		egrs = previous
	}
	// previous == nil && current == nil is unexpected, we should return false directly.
	if egrs == nil {
		return false
	}
	namespacedName := types.NamespacedName{Namespace: egrs.Namespace, Name: egrs.Name}

	ect.lock.Lock()
	defer ect.lock.Unlock()

	change, exists := ect.items[namespacedName]
	if !exists {
		change = &egressChange{}
		change.previous = ect.egressToEgressMap(previous)
		ect.items[namespacedName] = change
	}
	change.current = ect.egressToEgressMap(current)
	// if change.previous equal to change.current, it means no change
	if reflect.DeepEqual(change.previous, change.current) {
		delete(ect.items, namespacedName)
	}
	return len(ect.items) > 0
}

// UpdateEgressMapResult is the updated results after applying egress changes.
type UpdateEgressMapResult struct {
	// StaleIP holds stale (no longer assigned to an egress) Egress IPs.
	StaleIP sets.String
}

// UpdateEgressMap updates EgressMap based on the given changes.
func UpdateEgressMap(egressMap EgressMap, changes *EgressChangeTracker) (result UpdateEgressMapResult) {
	result.StaleIP = sets.NewString()
	egressMap.apply(changes, result.StaleIP)

	return result
}

// EgressMap maps an egress' namespaced name to IP.
type EgressMap map[string]string

// egressToEgressMap translates a single Egress object to an EgressMap.
//
// NOTE: egress object should NOT be modified.
func (ect *EgressChangeTracker) egressToEgressMap(egress *v1.Egress) EgressMap {
	if egress == nil {
		return nil
	}
	egressName := types.NamespacedName{Namespace: egress.Namespace, Name: egress.Name}
	if utilproxy.ShouldSkipEgress(egressName, egress) {
		return nil
	}

	if len(egress.Spec.IP) != 0 {
		return nil
	}

	egressMap := make(EgressMap)
	egressMap[egressName] = egress.Spec.IP

	return egressMap
}
