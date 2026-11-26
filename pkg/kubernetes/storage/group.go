package storage

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	quotav1alpha1 "github.com/powerhome/pac-quota-controller/api/v1alpha1"
)

// StorageClassGroupResourceDomain is the resource-name domain used for
// group-level limits, mirroring the per-class
// '<class>.storageclass.storage.k8s.io/...' convention.
const StorageClassGroupResourceDomain = "storageclassgroup.storage.k8s.io"

const (
	groupStorageSuffix = "." + StorageClassGroupResourceDomain + "/requests.storage"
	groupCountSuffix   = "." + StorageClassGroupResourceDomain + "/persistentvolumeclaims"

	// groupBucketPrefix keeps group buckets in their own namespace inside the
	// per-class bucket map. ':' cannot appear in a storage class name, so a
	// group bucket can never shadow a real class.
	groupBucketPrefix = "group:"
)

// StorageClassGroupStorageResource returns the Hard key holding a group's
// combined storage limit.
func StorageClassGroupStorageResource(group string) corev1.ResourceName {
	return corev1.ResourceName(group + groupStorageSuffix)
}

// StorageClassGroupPVCCountResource returns the Hard key holding a group's
// combined PVC count limit.
func StorageClassGroupPVCCountResource(group string) corev1.ResourceName {
	return corev1.ResourceName(group + groupCountSuffix)
}

// ParseStorageClassGroupResource splits a group-level resource name into its
// group name and the base resource it limits (requests.storage or
// persistentvolumeclaims). ok is false for any other resource name.
func ParseStorageClassGroupResource(
	resourceName corev1.ResourceName,
) (group string, base corev1.ResourceName, ok bool) {
	resourceStr := string(resourceName)
	if name, found := strings.CutSuffix(resourceStr, groupStorageSuffix); found && name != "" {
		return name, corev1.ResourceRequestsStorage, true
	}
	if name, found := strings.CutSuffix(resourceStr, groupCountSuffix); found && name != "" {
		return name, corev1.ResourcePersistentVolumeClaims, true
	}
	return "", "", false
}

// IsStorageClassGroupResource reports whether the resource name is a
// group-level limit.
func IsStorageClassGroupResource(resourceName corev1.ResourceName) bool {
	_, _, ok := ParseStorageClassGroupResource(resourceName)
	return ok
}

// StorageClassGroupBucketKey returns the per-class bucket key that holds a
// group's PVCs. It is only meaningful together with AddStorageClassGroupBuckets.
func StorageClassGroupBucketKey(group string) string {
	return groupBucketPrefix + group
}

// StorageClassGroupIndex maps each storage class to the group that owns it.
// Classes that appear in no group, and all classes when no groups are declared,
// are absent from the map and keep the classic per-class-only behavior. A class
// listed by more than one group resolves to the first group, though CRQ
// admission rejects that configuration.
func StorageClassGroupIndex(groups []quotav1alpha1.StorageClassGroup) map[string]string {
	if len(groups) == 0 {
		return nil
	}
	index := make(map[string]string, len(groups))
	for _, group := range groups {
		for _, class := range group.StorageClasses {
			if class == "" {
				continue
			}
			if _, seen := index[class]; seen {
				continue
			}
			index[class] = group.Name
		}
	}
	return index
}

// StorageClassGroupForClass returns the group that owns the storage class.
func StorageClassGroupForClass(groups []quotav1alpha1.StorageClassGroup, class string) (string, bool) {
	if class == "" {
		return "", false
	}
	for _, group := range groups {
		for _, member := range group.StorageClasses {
			if member == class {
				return group.Name, true
			}
		}
	}
	return "", false
}

// AddStorageClassGroupBuckets indexes the PVCs already bucketed by class a
// second time under each group's bucket key, so group aggregation can reuse
// the per-class buckets instead of re-scanning the PVC list. It resolves each
// class through StorageClassGroupIndex, so a class repeated within a group (or
// listed by several groups, which CRQ admission rejects) is counted exactly
// once in exactly one group's bucket.
func AddStorageClassGroupBuckets(
	buckets map[string][]corev1.PersistentVolumeClaim,
	groups []quotav1alpha1.StorageClassGroup,
) {
	if len(buckets) == 0 || len(groups) == 0 {
		return
	}
	for class, group := range StorageClassGroupIndex(groups) {
		classPVCs := buckets[class]
		if len(classPVCs) == 0 {
			continue
		}
		bucketKey := StorageClassGroupBucketKey(group)
		buckets[bucketKey] = append(buckets[bucketKey], classPVCs...)
	}
}
