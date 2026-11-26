package storage

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	quotav1alpha1 "github.com/powerhome/pac-quota-controller/api/v1alpha1"
)

var _ = Describe("Storage class group helpers", func() {
	groups := []quotav1alpha1.StorageClassGroup{
		{Name: "shared-fast", StorageClasses: []string{"fast-ssd", "fast-nvme"}},
		{Name: "shared-slow", StorageClasses: []string{"slow-hdd"}},
	}

	Describe("group resource names", func() {
		It("builds the storage and count keys", func() {
			Expect(string(StorageClassGroupStorageResource("shared-fast"))).
				To(Equal("shared-fast.storageclassgroup.storage.k8s.io/requests.storage"))
			Expect(string(StorageClassGroupPVCCountResource("shared-fast"))).
				To(Equal("shared-fast.storageclassgroup.storage.k8s.io/persistentvolumeclaims"))
		})

		It("round-trips storage and count keys", func() {
			group, base, ok := ParseStorageClassGroupResource(
				StorageClassGroupStorageResource("shared-fast"))
			Expect(ok).To(BeTrue())
			Expect(group).To(Equal("shared-fast"))
			Expect(base).To(Equal(corev1.ResourceRequestsStorage))

			group, base, ok = ParseStorageClassGroupResource(
				StorageClassGroupPVCCountResource("shared-fast"))
			Expect(ok).To(BeTrue())
			Expect(group).To(Equal("shared-fast"))
			Expect(base).To(Equal(corev1.ResourcePersistentVolumeClaims))
		})

		It("does not mistake per-class or plain keys for group keys", func() {
			for _, name := range []corev1.ResourceName{
				"fast-ssd.storageclass.storage.k8s.io/requests.storage",
				"fast-ssd.storageclass.storage.k8s.io/persistentvolumeclaims",
				corev1.ResourceRequestsStorage,
				corev1.ResourcePersistentVolumeClaims,
				".storageclassgroup.storage.k8s.io/requests.storage",
			} {
				Expect(IsStorageClassGroupResource(name)).To(BeFalse(), string(name))
			}
			// A group literally named "group" still parses correctly.
			group, _, ok := ParseStorageClassGroupResource(
				"group.storageclassgroup.storage.k8s.io/requests.storage")
			Expect(ok).To(BeTrue())
			Expect(group).To(Equal("group"))
		})
	})

	Describe("membership", func() {
		It("indexes each class to its group", func() {
			index := StorageClassGroupIndex(groups)
			Expect(index).To(Equal(map[string]string{
				"fast-ssd":  "shared-fast",
				"fast-nvme": "shared-fast",
				"slow-hdd":  "shared-slow",
			}))
		})

		It("returns no index when no groups are declared", func() {
			Expect(StorageClassGroupIndex(nil)).To(BeNil())
		})

		It("keeps the first group when a class is listed twice", func() {
			dupes := []quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: []string{"fast-ssd"}},
				{Name: "b", StorageClasses: []string{"fast-ssd"}},
			}
			Expect(StorageClassGroupIndex(dupes)["fast-ssd"]).To(Equal("a"))
		})

		It("looks up a class's group", func() {
			group, ok := StorageClassGroupForClass(groups, "fast-nvme")
			Expect(ok).To(BeTrue())
			Expect(group).To(Equal("shared-fast"))

			_, ok = StorageClassGroupForClass(groups, "unlisted")
			Expect(ok).To(BeFalse())

			_, ok = StorageClassGroupForClass(groups, "")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("AddStorageClassGroupBuckets", func() {
		fastSSD := "fast-ssd"
		fastNVMe := "fast-nvme"
		unlisted := "unlisted"

		It("copies each member class's PVCs into the group bucket", func() {
			pvcs := []corev1.PersistentVolumeClaim{
				{Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &fastSSD,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				}},
				{Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &fastNVMe,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
					},
				}},
				{Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &unlisted,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")},
					},
				}},
			}

			buckets := map[string][]corev1.PersistentVolumeClaim{}
			for i := range pvcs {
				class := PVCStorageClass(&pvcs[i])
				buckets[class] = append(buckets[class], pvcs[i])
			}

			AddStorageClassGroupBuckets(buckets, groups)

			groupPVCs := buckets[StorageClassGroupBucketKey("shared-fast")]
			Expect(groupPVCs).To(HaveLen(2))
			groupUsage := CalculateStorageUsageFromPVCs(groupPVCs, corev1.ResourceRequestsStorage)
			Expect(groupUsage.String()).To(Equal("3Gi"))
			// The unlisted class is not pulled into any group.
			Expect(buckets[StorageClassGroupBucketKey("shared-slow")]).To(BeEmpty())
		})

		It("counts a class listed twice exactly once", func() {
			dupes := []quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: []string{"fast-ssd", "fast-ssd"}},
				{Name: "b", StorageClasses: []string{"fast-ssd"}},
			}
			buckets := map[string][]corev1.PersistentVolumeClaim{
				"fast-ssd": {{
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
						},
					},
				}},
			}

			AddStorageClassGroupBuckets(buckets, dupes)

			// First group wins, and the class is counted once.
			Expect(buckets[StorageClassGroupBucketKey("a")]).To(HaveLen(1))
			Expect(buckets[StorageClassGroupBucketKey("b")]).To(BeEmpty())
		})

		It("is a no-op when there are no PVCs or no groups", func() {
			buckets := map[string][]corev1.PersistentVolumeClaim{}
			AddStorageClassGroupBuckets(buckets, groups)
			Expect(buckets).To(BeEmpty())

			AddStorageClassGroupBuckets(nil, groups)
			AddStorageClassGroupBuckets(buckets, nil)
		})
	})
})
