/*
Copyright 2016 The Kubernetes Authors.

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

package predicates

import (
	"strings"

	"github.com/golang/glog"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/scheduler/algorithm"
	schedutil "k8s.io/kubernetes/pkg/scheduler/util"
)

// FindLabelsInSet gets as many key/value pairs as possible out of a label set.
func FindLabelsInSet(labelsToKeep []string, selector labels.Set) map[string]string {
	aL := make(map[string]string)
	for _, l := range labelsToKeep {
		if selector.Has(l) {
			aL[l] = selector.Get(l)
		}
	}
	return aL
}

// AddUnsetLabelsToMap backfills missing values with values we find in a map.
func AddUnsetLabelsToMap(aL map[string]string, labelsToAdd []string, labelSet labels.Set) {
	for _, l := range labelsToAdd {
		// if the label is already there, dont overwrite it.
		if _, exists := aL[l]; exists {
			continue
		}
		// otherwise, backfill this label.
		if labelSet.Has(l) {
			aL[l] = labelSet.Get(l)
		}
	}
}

// FilterPodsByNamespace filters pods outside a namespace from the given list.
func FilterPodsByNamespace(pods []*v1.Pod, ns string) []*v1.Pod {
	filtered := []*v1.Pod{}
	for _, nsPod := range pods {
		if nsPod.Namespace == ns {
			filtered = append(filtered, nsPod)
		}
	}
	return filtered
}

// CreateSelectorFromLabels is used to define a selector that corresponds to the keys in a map.
func CreateSelectorFromLabels(aL map[string]string) labels.Selector {
	if aL == nil || len(aL) == 0 {
		return labels.Everything()
	}
	return labels.Set(aL).AsSelector()
}

// EquivalencePodGenerator is a generator of equivalence class for pod with consideration of PVC info.
type EquivalencePodGenerator struct {
	pvcInfo PersistentVolumeClaimInfo
}

// NewEquivalencePodGenerator returns a getEquivalencePod method with consideration of PVC info.
func NewEquivalencePodGenerator(pvcInfo PersistentVolumeClaimInfo) algorithm.GetEquivalencePodFunc {
	g := &EquivalencePodGenerator{
		pvcInfo: pvcInfo,
	}
	return g.getEquivalencePod
}

// GetEquivalencePod returns a EquivalencePod which contains a group of pod attributes which can be reused.
func (e *EquivalencePodGenerator) getEquivalencePod(pod *v1.Pod) interface{} {
	// For now we only consider pods:
	// 1. OwnerReferences is Controller
	// 2. with same OwnerReferences
	// 3. with same PVC claim
	// to be equivalent
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			if pvcSet, err := e.getPVCSet(pod); err == nil {
				// A pod can only belongs to one controller, so let's return.
				return &EquivalencePod{
					ControllerRef: ref,
					PVCSet:        pvcSet,
				}
			} else {
				// If error encountered, log warning and return nil (i.e. no equivalent pod found)
				glog.Warningf("[EquivalencePodGenerator] for pod: %v failed due to: %v", pod.GetName(), err)
				return nil
			}
		}
	}
	return nil
}

// getPVCSet returns a set of PVC UIDs of given pod.
func (e *EquivalencePodGenerator) getPVCSet(pod *v1.Pod) (sets.String, error) {
	result := sets.NewString()
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil {
			continue
		}
		pvcName := volume.PersistentVolumeClaim.ClaimName
		pvc, err := e.pvcInfo.GetPersistentVolumeClaimInfo(pod.GetNamespace(), pvcName)
		if err != nil {
			return nil, err
		}
		result.Insert(string(pvc.UID))
	}

	return result, nil
}

// EquivalencePod is a group of pod attributes which can be reused as equivalence to schedule other pods.
type EquivalencePod struct {
	ControllerRef metav1.OwnerReference
	PVCSet        sets.String
}

type hostPortInfo struct {
	protocol string
	hostIP   string
	hostPort string
}

// decode decodes string ("protocol/hostIP/hostPort") to *hostPortInfo object.
func decode(info string) *hostPortInfo {
	hostPortInfoSlice := strings.Split(info, "/")

	protocol := hostPortInfoSlice[0]
	hostIP := hostPortInfoSlice[1]
	hostPort := hostPortInfoSlice[2]

	return &hostPortInfo{
		protocol: protocol,
		hostIP:   hostIP,
		hostPort: hostPort,
	}
}

// specialPortConflictCheck detects whether specailHostPort(whose hostIP is 0.0.0.0) is conflict with otherHostPorts.
// return true if we have a conflict.
func specialPortConflictCheck(specialHostPort string, otherHostPorts map[string]bool) bool {
	specialHostPortInfo := decode(specialHostPort)

	if specialHostPortInfo.hostIP == schedutil.DefaultBindAllHostIP {
		// loop through all the otherHostPorts to see if there exists a conflict
		for hostPortItem := range otherHostPorts {
			hostPortInfo := decode(hostPortItem)

			// if there exists one hostPortItem which has the same hostPort and protocol with the specialHostPort, that will cause a conflict
			if specialHostPortInfo.hostPort == hostPortInfo.hostPort && specialHostPortInfo.protocol == hostPortInfo.protocol {
				return true
			}
		}

	}

	return false
}

// portsConflict check whether existingPorts and wantPorts conflict with each other
// return true if we have a conflict
func portsConflict(existingPorts, wantPorts map[string]bool) bool {

	for existingPort := range existingPorts {
		if specialPortConflictCheck(existingPort, wantPorts) {
			return true
		}
	}

	for wantPort := range wantPorts {
		if specialPortConflictCheck(wantPort, existingPorts) {
			return true
		}

		// general check hostPort conflict procedure for hostIP is not 0.0.0.0
		if existingPorts[wantPort] {
			return true
		}
	}

	return false
}
