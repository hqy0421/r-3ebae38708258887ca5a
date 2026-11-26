package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	quotav1alpha1 "github.com/powerhome/pac-quota-controller/api/v1alpha1"
	"github.com/powerhome/pac-quota-controller/pkg/kubernetes/storage"
	testutils "github.com/powerhome/pac-quota-controller/test/utils"
)

var _ = Describe("Storage Class Group Quota E2E Tests", func() {
	const groupName = "shared-array"

	var (
		testNamespace      string
		testNamespaceLabel string
		storageClassA      string
		storageClassB      string
		storageClassOther  string
		crqName            string

		groupStorageKey corev1.ResourceName
		groupCountKey   corev1.ResourceName
		classAStorage   corev1.ResourceName
		classACount     corev1.ResourceName
	)

	BeforeEach(func() {
		testNamespace = fmt.Sprintf("scg-test-%s", testutils.GenerateTestSuffix())
		testNamespaceLabel = "enabled-" + testutils.GenerateTestSuffix()
		storageClassA = fmt.Sprintf("scg-a-%s", testutils.GenerateTestSuffix())
		storageClassB = fmt.Sprintf("scg-b-%s", testutils.GenerateTestSuffix())
		storageClassOther = fmt.Sprintf("scg-other-%s", testutils.GenerateTestSuffix())
		crqName = fmt.Sprintf("scg-crq-%s", testutils.GenerateTestSuffix())

		groupStorageKey = storage.StorageClassGroupStorageResource(groupName)
		groupCountKey = storage.StorageClassGroupPVCCountResource(groupName)
		classAStorage = corev1.ResourceName(fmt.Sprintf(
			"%s.storageclass.storage.k8s.io/requests.storage", storageClassA))
		classACount = corev1.ResourceName(fmt.Sprintf(
			"%s.storageclass.storage.k8s.io/persistentvolumeclaims", storageClassA))

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace,
				Labels: map[string]string{
					"storage-class-group-test": testNamespaceLabel,
				},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		for _, name := range []string{storageClassA, storageClassB, storageClassOther} {
			sc := &storagev1.StorageClass{
				ObjectMeta:  metav1.ObjectMeta{Name: name},
				Provisioner: "kubernetes.io/no-provisioner",
			}
			Expect(k8sClient.Create(ctx, sc)).To(Succeed())
		}
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})
		_ = k8sClient.Delete(ctx, &quotav1alpha1.ClusterResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: crqName},
		})
		for _, name := range []string{storageClassA, storageClassB, storageClassOther} {
			_ = k8sClient.Delete(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}})
		}
	})

	createGroupedCRQ := func(hard quotav1alpha1.ResourceList) {
		crq := &quotav1alpha1.ClusterResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: crqName},
			Spec: quotav1alpha1.ClusterResourceQuotaSpec{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
					"storage-class-group-test": testNamespaceLabel,
				}},
				StorageClassGroups: []quotav1alpha1.StorageClassGroup{
					{Name: groupName, StorageClasses: []string{storageClassA, storageClassB}},
				},
				Hard: hard,
			},
		}
		Expect(k8sClient.Create(ctx, crq)).To(Succeed())
	}

	It("aggregates the group from the stored volumes of its member classes only", func() {
		createGroupedCRQ(quotav1alpha1.ResourceList{
			classAStorage:   resource.MustParse("50Gi"),
			classACount:     resource.MustParse("10"),
			groupStorageKey: resource.MustParse("20Gi"),
			groupCountKey:   resource.MustParse("10"),
		})

		By("Creating PVCs on both member classes plus one ungrouped class")
		Expect(k8sClient.Create(ctx,
			createTestPVC("a1", testNamespace, storageClassA, "3Gi"))).To(Succeed())
		Expect(k8sClient.Create(ctx,
			createTestPVC("b1", testNamespace, storageClassB, "2Gi"))).To(Succeed())
		Expect(k8sClient.Create(ctx,
			createTestPVC("other1", testNamespace, storageClassOther, "1Gi"))).To(Succeed())

		By("Waiting for the group's storage usage to include both member classes")
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, groupStorageKey, resource.MustParse("5Gi"))).To(Succeed())
		By("Waiting for the group's PVC count to include both member classes")
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, groupCountKey, resource.MustParse("2"))).To(Succeed())
		By("Waiting for the per-class usage to stay per-class")
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, classAStorage, resource.MustParse("3Gi"))).To(Succeed())
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, classACount, resource.MustParse("1"))).To(Succeed())
	})

	It("denies a create that would exceed the group while the class still has room", func() {
		createGroupedCRQ(quotav1alpha1.ResourceList{
			// Generous per-class budgets, tight combined budget.
			classAStorage:   resource.MustParse("100Gi"),
			groupStorageKey: resource.MustParse("10Gi"),
		})

		By("Filling most of the group budget through the other member class")
		Expect(k8sClient.Create(ctx,
			createTestPVC("b1", testNamespace, storageClassB, "8Gi"))).To(Succeed())
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, groupStorageKey, resource.MustParse("8Gi"))).To(Succeed())

		By("Trying to add a PVC on the other class that only the group can't fit")
		err := testutils.EventuallyDenied(ctx, k8sClient, func() (client.Object, error) {
			pvc := createTestPVC(testutils.GenerateResourceName("a"), testNamespace, storageClassA, "3Gi")
			return pvc, k8sClient.Create(ctx, pvc)
		})
		Expect(err).To(HaveOccurred(), "group budget should block the create")
		Expect(err.Error()).To(ContainSubstring(
			"ClusterResourceQuota storage class group '" + groupName + "' storage validation failed"))
	})

	It("denies a create that would exceed the class while the group still has room", func() {
		createGroupedCRQ(quotav1alpha1.ResourceList{
			// Tight per-class budget, generous combined budget.
			classAStorage:   resource.MustParse("5Gi"),
			groupStorageKey: resource.MustParse("100Gi"),
		})

		By("Filling the class A budget")
		Expect(k8sClient.Create(ctx,
			createTestPVC("a1", testNamespace, storageClassA, "5Gi"))).To(Succeed())
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, classAStorage, resource.MustParse("5Gi"))).To(Succeed())

		By("Trying to add another class A PVC that the class budget can't fit")
		err := testutils.EventuallyDenied(ctx, k8sClient, func() (client.Object, error) {
			pvc := createTestPVC(testutils.GenerateResourceName("a"), testNamespace, storageClassA, "1Gi")
			return pvc, k8sClient.Create(ctx, pvc)
		})
		Expect(err).To(HaveOccurred(), "per-class budget should block the create")
		Expect(err.Error()).To(ContainSubstring(
			"ClusterResourceQuota storage class '" + storageClassA + "' storage validation failed"))
	})

	It("charges only the growth on resize against the group budget", func() {
		createGroupedCRQ(quotav1alpha1.ResourceList{
			classAStorage:   resource.MustParse("100Gi"),
			groupStorageKey: resource.MustParse("10Gi"),
		})

		By("Creating a 5Gi PVC on class A")
		pvc := createTestPVC("a1", testNamespace, storageClassA, "5Gi")
		Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, groupStorageKey, resource.MustParse("5Gi"))).To(Succeed())

		By("Growing the PVC to 9Gi (delta 4Gi fits) and waiting for the group to catch up")
		resize := func(pvc *corev1.PersistentVolumeClaim, size string) error {
			pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse(size)
			return k8sClient.Update(ctx, pvc)
		}
		fresh := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "a1", Namespace: testNamespace}, fresh)).
			To(Succeed())
		Expect(resize(fresh, "9Gi")).To(Succeed())
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, groupStorageKey, resource.MustParse("9Gi"))).To(Succeed())

		By("Trying to grow to 14Gi (delta 5Gi does not fit)")
		var denialErr error
		Eventually(func() bool {
			fresh = &corev1.PersistentVolumeClaim{}
			if err := k8sClient.Get(ctx,
				types.NamespacedName{Name: "a1", Namespace: testNamespace}, fresh); err != nil {
				return false
			}
			if err := resize(fresh, "14Gi"); err != nil {
				denialErr = err
				return true
			}
			return false
		}, 30*time.Second, 500*time.Millisecond).Should(BeTrue(),
			"the group budget should block the resize")
		Expect(denialErr.Error()).To(ContainSubstring(
			"ClusterResourceQuota storage class group '" + groupName + "' storage validation failed"))
	})

	It("counts group PVCs for both member classes on create", func() {
		createGroupedCRQ(quotav1alpha1.ResourceList{
			classACount:   resource.MustParse("10"),
			groupCountKey: resource.MustParse("2"),
		})

		By("Filling the group's PVC count through class A")
		Expect(k8sClient.Create(ctx,
			createTestPVC("a1", testNamespace, storageClassA, "1Gi"))).To(Succeed())
		By("Filling the group's PVC count through class B")
		Expect(k8sClient.Create(ctx,
			createTestPVC("b1", testNamespace, storageClassB, "1Gi"))).To(Succeed())
		Expect(testutils.WaitForCRQResourceUsage(
			ctx, k8sClient, crqName, groupCountKey, resource.MustParse("2"))).To(Succeed())

		By("Trying to add a third grouped PVC that the group count can't fit")
		err := testutils.EventuallyDenied(ctx, k8sClient, func() (client.Object, error) {
			pvc := createTestPVC(testutils.GenerateResourceName("a"), testNamespace, storageClassA, "1Mi")
			return pvc, k8sClient.Create(ctx, pvc)
		})
		Expect(err).To(HaveOccurred(), "group PVC count should block the create")
		Expect(err.Error()).To(ContainSubstring(
			"ClusterResourceQuota storage class group '" + groupName + "' PVC count validation failed"))
	})
})
