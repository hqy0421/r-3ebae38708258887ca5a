package quota

import (
	"fmt"
	"regexp"

	quotav1alpha1 "github.com/powerhome/pac-quota-controller/api/v1alpha1"
	"github.com/powerhome/pac-quota-controller/pkg/kubernetes/storage"
)

// maxStorageClassGroupNameLength bounds a group name so the derived resource
// name ('<group>.storageclassgroup.storage.k8s.io/persistentvolumeclaims')
// stays within Kubernetes' 253-character resource-name limit.
const maxStorageClassGroupNameLength = 200

// validStorageClassGroupName matches the group names that can safely prefix a
// group-level resource key. The name becomes the leading component of a
// resource name, so it must be a lowercase DNS-1123 subdomain.
var validStorageClassGroupName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// ValidateStorageClassGroups validates spec.storageClassGroups: every group
// needs a well-formed, unique name and at least one storage class; every
// storage class may belong to at most one group within the quota; and any
// group-level limit in spec.hard must name a declared group.
func ValidateStorageClassGroups(crq *quotav1alpha1.ClusterResourceQuota) error {
	groups := crq.Spec.StorageClassGroups

	groupNames := make(map[string]bool, len(groups))
	classOwner := make(map[string]string)
	for i, group := range groups {
		if group.Name == "" {
			return fmt.Errorf("spec.storageClassGroups[%d]: name is required", i)
		}
		if !validStorageClassGroupName.MatchString(group.Name) || len(group.Name) > maxStorageClassGroupNameLength {
			return fmt.Errorf(
				"spec.storageClassGroups[%d]: group name %q must be a lowercase DNS-1123 subdomain of at most %d characters",
				i, group.Name, maxStorageClassGroupNameLength)
		}
		if groupNames[group.Name] {
			return fmt.Errorf("spec.storageClassGroups: duplicate group name %q", group.Name)
		}
		groupNames[group.Name] = true

		if len(group.StorageClasses) == 0 {
			return fmt.Errorf("spec.storageClassGroups[%d] (%s): storageClasses must not be empty", i, group.Name)
		}
		for _, class := range group.StorageClasses {
			if class == "" {
				return fmt.Errorf("spec.storageClassGroups[%d] (%s): storage class name must not be empty", i, group.Name)
			}
			if owner, seen := classOwner[class]; seen {
				if owner == group.Name {
					return fmt.Errorf(
						"spec.storageClassGroups: storage class %q is listed more than once in group %q",
						class, group.Name)
				}
				return fmt.Errorf(
					"spec.storageClassGroups: storage class %q belongs to multiple groups (%q and %q)",
					class, owner, group.Name)
			}
			classOwner[class] = group.Name
		}
	}

	for resourceName := range crq.Spec.Hard {
		group, _, ok := storage.ParseStorageClassGroupResource(resourceName)
		if ok && !groupNames[group] {
			return fmt.Errorf(
				"spec.hard: %q references storage class group %q, which is not declared in spec.storageClassGroups",
				resourceName, group)
		}
	}

	return nil
}
