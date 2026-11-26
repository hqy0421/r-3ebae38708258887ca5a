package quota

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	quotav1alpha1 "github.com/powerhome/pac-quota-controller/api/v1alpha1"
	"github.com/powerhome/pac-quota-controller/pkg/kubernetes/storage"
)

func groupedCRQ(
	groups []quotav1alpha1.StorageClassGroup,
	hard quotav1alpha1.ResourceList,
) *quotav1alpha1.ClusterResourceQuota {
	return &quotav1alpha1.ClusterResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "crq"},
		Spec: quotav1alpha1.ClusterResourceQuotaSpec{
			Hard:               hard,
			StorageClassGroups: groups,
		},
	}
}

var _ = Describe("ValidateStorageClassGroups", func() {
	groupHard := func(group string) quotav1alpha1.ResourceList {
		return quotav1alpha1.ResourceList{
			storage.StorageClassGroupStorageResource(group):  resource.MustParse("100Gi"),
			storage.StorageClassGroupPVCCountResource(group): resource.MustParse("10"),
		}
	}

	It("accepts an empty spec", func() {
		Expect(ValidateStorageClassGroups(groupedCRQ(nil, nil))).To(Succeed())
	})

	It("accepts disjoint groups with matching hard keys", func() {
		crq := groupedCRQ([]quotav1alpha1.StorageClassGroup{
			{Name: "shared-fast", StorageClasses: []string{"fast-ssd", "fast-nvme"}},
			{Name: "shared-slow", StorageClasses: []string{"slow-hdd"}},
		}, groupHard("shared-fast"))
		Expect(ValidateStorageClassGroups(crq)).To(Succeed())
	})

	DescribeTable("rejections",
		func(crq *quotav1alpha1.ClusterResourceQuota, substr string) {
			err := ValidateStorageClassGroups(crq)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(substr))
		},
		Entry("class in two groups",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: []string{"fast-ssd"}},
				{Name: "b", StorageClasses: []string{"fast-ssd"}},
			}, nil),
			"belongs to multiple groups"),
		Entry("class listed twice in one group",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: []string{"fast-ssd", "fast-ssd"}},
			}, nil),
			"listed more than once"),
		Entry("duplicate group name",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: []string{"fast-ssd"}},
				{Name: "a", StorageClasses: []string{"slow-hdd"}},
			}, nil),
			"duplicate group name"),
		Entry("empty group name",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "", StorageClasses: []string{"fast-ssd"}},
			}, nil),
			"name is required"),
		Entry("invalid group name",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "Shared_Fast", StorageClasses: []string{"fast-ssd"}},
			}, nil),
			"lowercase DNS-1123 subdomain"),
		Entry("empty storageClasses",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: nil},
			}, nil),
			"storageClasses must not be empty"),
		Entry("empty storage class name",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: []string{""}},
			}, nil),
			"storage class name must not be empty"),
		Entry("hard references an undeclared group",
			groupedCRQ([]quotav1alpha1.StorageClassGroup{
				{Name: "a", StorageClasses: []string{"fast-ssd"}},
			}, quotav1alpha1.ResourceList{
				storage.StorageClassGroupStorageResource("ghost"): resource.MustParse("1Gi"),
			}),
			"not declared"),
	)

	It("treats group keys as standard (non-pod) resources for scopes", func() {
		crq := groupedCRQ([]quotav1alpha1.StorageClassGroup{
			{Name: "shared-fast", StorageClasses: []string{"fast-ssd"}},
		}, quotav1alpha1.ResourceList{
			storage.StorageClassGroupStorageResource("shared-fast"): resource.MustParse("100Gi"),
		})
		crq.Spec.Scopes = []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeBestEffort}
		_, err := ValidateScopeSpec(crq)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unsupported scope"))
	})
})
