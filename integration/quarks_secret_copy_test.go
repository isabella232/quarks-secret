package integration_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	qsv1a1 "code.cloudfoundry.org/quarks-secret/pkg/kube/apis/quarkssecret/v1alpha1"
	"code.cloudfoundry.org/quarks-utils/testing/machine"
)

var _ = Describe("QuarksCopies", func() {
	var (
		qsec          qsv1a1.QuarksSecret
		tearDowns     []machine.TearDownFunc
		copyNamespace string
	)
	const (
		qsecName = "test.qsec"
	)

	AfterEach(func() {
		Eventually(func() error {
			return env.TearDownAll(tearDowns)
		}, 20*time.Second).Should(BeNil())
	})

	checkStatus := func() {
		Eventually(func() bool {
			qsec, err := env.GetQuarksSecret(env.Namespace, qsecName)
			Expect(err).NotTo(HaveOccurred())
			if qsec.Status.Generated != nil {
				return *qsec.Status.Generated
			}
			return false
		}, 5*time.Second).Should(Equal(true))
	}

	createQuarksSecretWithCopies := func(copyNamespace string) {
		qsec = env.DefaultQuarksSecretWithCopy(qsecName, copyNamespace)
		_, tearDown, err := env.CreateQuarksSecret(env.Namespace, qsec)
		Expect(err).NotTo(HaveOccurred())
		tearDowns = append(tearDowns, tearDown)
	}

	BeforeEach(func() {
		copyNamespace = fmt.Sprintf("%s-%s", env.Namespace, "copy")

		By("Creating copy namespace", func() {
			tearDown, err := env.CreateNamespace(copyNamespace)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)
		})
	})

	Context("the secret is generated by operator and qsec has copies spec", func() {
		BeforeEach(func() {
			copyNamespace = fmt.Sprintf("%s-%s", env.Namespace, "copy")

			By("Creating quarkssecret with copies")
			createQuarksSecretWithCopies(copyNamespace)
		})

		It("should not copy the generated secret to the copy namespace if no qsec or secret is found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking if the copy secret is empty")
			secret, err := env.GetSecret(copyNamespace, "generated-secret-copy")
			Expect(err).To(HaveOccurred())
			Expect(secret).To(BeNil())
		})
	})

	Context("the secret is generated by operator for qsec copies", func() {
		BeforeEach(func() {
			quarksCopySecret := &qsv1a1.QuarksSecret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      qsecName,
					Namespace: copyNamespace,
					Labels: map[string]string{
						"quarks.cloudfoundry.org/secret-kind": "generated",
					},
					Annotations: map[string]string{
						"quarks.cloudfoundry.org/secret-copy-of": env.Namespace + "/" + qsecName,
					},
				},
				Spec: qsv1a1.QuarksSecretSpec{
					Type:       "copy",
					SecretName: "generated-secret-copy",
				},
			}

			By("Creating copy quarks secret in copy namespace")
			_, tearDown, err := env.CreateQuarksSecret(copyNamespace, *quarksCopySecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Creating quarkssecret with copies")
			createQuarksSecretWithCopies(copyNamespace)
		})

		It("should copy the generated secret to the copy namespace if qsec is found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the secret in source namespace")
			secret, err := env.GetSecret(env.Namespace, "generated-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			sourceSecretData := secret.StringData["password"]
			Expect(sourceSecretData).NotTo(BeNil())

			By("Checking the secret in target namespace")
			secret, err = env.GetSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			targetSecretData := secret.StringData["password"]
			Expect(targetSecretData).NotTo(BeNil())
			Expect(targetSecretData).To(Equal(sourceSecretData))
		})

		It("should copy the rotated generated secret to the copy namespace if qsec is found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			secret, err := env.CollectSecret(env.Namespace, "generated-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			oldSecretData := string(secret.Data["password"])

			By("Rotating the quarkssecret")
			rotationConfig := env.RotationConfig(qsecName)
			tearDown, err := env.CreateConfigMap(env.Namespace, rotationConfig)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			err = env.WaitForConfigMap(env.Namespace, "rotation-config1")
			Expect(err).NotTo(HaveOccurred())

			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the secret data in source namespace")
			secret, err = env.CollectSecret(env.Namespace, "generated-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			Expect(secret.Data["password"]).NotTo(BeNil())
			Expect(oldSecretData).NotTo(Equal(string(secret.Data["password"])))

			By("Checking the copied secret data")
			secret, err = env.CollectSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			Expect(secret.StringData["password"]).NotTo(BeNil())
		})
	})

	Context("the secret is generated by operator for qsec copies", func() {
		BeforeEach(func() {
			copyNamespace = fmt.Sprintf("%s-%s", env.Namespace, "copy")

			passwordCopySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generated-secret-copy",
					Namespace: copyNamespace,
					Labels: map[string]string{
						"quarks.cloudfoundry.org/secret-kind": "generated",
					},
					Annotations: map[string]string{
						"quarks.cloudfoundry.org/secret-copy-of": env.Namespace + "/" + qsecName,
					},
				},
			}

			By("Creating copy empty password secret in copy namespace")
			tearDown, err := env.CreateSecret(copyNamespace, *passwordCopySecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Creating quarkssecret with copies")
			createQuarksSecretWithCopies(copyNamespace)
		})

		It("should copy into other namespaces if copy secret if found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the secret in source namespace")
			secret, err := env.GetSecret(env.Namespace, "generated-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			sourceSecretData := secret.StringData["password"]
			Expect(sourceSecretData).NotTo(BeNil())

			By("Checking the secret in target namespace")
			secret, err = env.GetSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			targetSecretData := secret.StringData["password"]
			Expect(targetSecretData).NotTo(BeNil())
			Expect(targetSecretData).To(Equal(sourceSecretData))
		})

		It("should copy the rotated generated secret to the copy namespace if copy secret is found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			secret, err := env.CollectSecret(env.Namespace, "generated-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			oldSecretData := string(secret.Data["password"])

			By("Rotating the quarkssecret")
			rotationConfig := env.RotationConfig(qsecName)
			tearDown, err := env.CreateConfigMap(env.Namespace, rotationConfig)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			err = env.WaitForConfigMap(env.Namespace, "rotation-config1")
			Expect(err).NotTo(HaveOccurred())

			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the secret data in source namespace")
			secret, err = env.CollectSecret(env.Namespace, "generated-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			Expect(secret.Data["password"]).NotTo(BeNil())
			Expect(oldSecretData).NotTo(Equal(string(secret.Data["password"])))

			By("Checking the copied secret data")
			secret, err = env.CollectSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(Equal(map[string]string{
				"quarks.cloudfoundry.org/secret-kind": "generated",
			}))
			Expect(secret.StringData["password"]).NotTo(BeNil())
		})
	})

	Context("the user provides the password secret", func() {
		BeforeEach(func() {
			passwordSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generated-secret",
					Namespace: env.Namespace,
				},
				StringData: map[string]string{
					"password": "securepassword",
				},
			}

			By("Creating user password secret")
			tearDown, err := env.CreateSecret(env.Namespace, *passwordSecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Creating quarkssecret")
			qsec = env.DefaultQuarksSecret(qsecName)
			_, tearDown, err = env.CreateQuarksSecret(env.Namespace, qsec)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)
		})

		It("should not generate the password secret", func() {
			By("Checking the quarkssecret status")
			Eventually(func() bool {
				qsec, err := env.GetQuarksSecret(env.Namespace, qsecName)
				Expect(err).NotTo(HaveOccurred())
				if qsec.Status.Generated != nil {
					return *qsec.Status.Generated
				}
				return false
			}).Should(Equal(true))

			By("Checking if it is the user created secret")
			secret, err := env.CollectSecret(env.Namespace, "generated-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(len(secret.Labels)).To(BeZero())
			Expect(string(secret.Data["password"])).To(Equal("securepassword"))
		})
	})

	Context("the user wants copies of the user password secret", func() {
		var passwordSecret *corev1.Secret

		BeforeEach(func() {
			copyNamespace = fmt.Sprintf("%s-%s", env.Namespace, "copy")

			passwordSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generated-secret",
					Namespace: env.Namespace,
				},
				StringData: map[string]string{
					"password": "securepassword",
				},
			}

			passwordCopySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generated-secret-copy",
					Namespace: copyNamespace,
					Labels: map[string]string{
						"quarks.cloudfoundry.org/secret-kind": "generated",
					},
					Annotations: map[string]string{
						"quarks.cloudfoundry.org/secret-copy-of": env.Namespace + "/" + qsecName,
					},
				},
			}

			By("Creating user password secret")
			tearDown, err := env.CreateSecret(env.Namespace, *passwordSecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Creating copy empty password secret in copy namespace")
			tearDown, err = env.CreateSecret(copyNamespace, *passwordCopySecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Creating quarkssecret with copies")
			createQuarksSecretWithCopies(copyNamespace)
		})

		It("should copy into other namespaces if copy empty secret if found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the copied secret data")
			secret, err := env.CollectSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(len(secret.Labels)).To(Equal(1))
			Expect(string(secret.Data["password"])).To(Equal("securepassword"))
		})

		It("should update the copies in other namespaces if copy empty secret is found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the copied secret data")
			secret, err := env.CollectSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(len(secret.Labels)).To(Equal(1))
			Expect(len(secret.Annotations)).To(Equal(2))
			Expect(string(secret.Data["password"])).To(Equal("securepassword"))

			By("Updating the user password secret")
			passwordSecret.StringData["password"] = "supersecurepassword"
			_, tearDown, err := env.UpdateSecret(env.Namespace, *passwordSecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Checking the quarkssecret status")
			Eventually(func() bool {
				qsec, err := env.GetQuarksSecret(env.Namespace, qsecName)
				Expect(err).NotTo(HaveOccurred())
				if qsec.Status.Generated != nil {
					return *qsec.Status.Generated
				}
				return false
			}).Should(Equal(false))

			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the copied secret data")
			secret, err = env.CollectSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(len(secret.Labels)).To(Equal(1))
			Expect(string(secret.Data["password"])).To(Equal("supersecurepassword"))
		})
	})

	Context("qsec wants copies of the user password secret", func() {
		var passwordSecret *corev1.Secret

		BeforeEach(func() {
			copyNamespace = fmt.Sprintf("%s-%s", env.Namespace, "copy")

			passwordSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "generated-secret",
					Namespace: env.Namespace,
				},
				StringData: map[string]string{
					"password": "securepassword",
				},
			}

			quarksCopySecret := &qsv1a1.QuarksSecret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      qsecName,
					Namespace: copyNamespace,
					Labels: map[string]string{
						"quarks.cloudfoundry.org/secret-kind": "generated",
					},
					Annotations: map[string]string{
						"quarks.cloudfoundry.org/secret-copy-of": env.Namespace + "/" + qsecName,
					},
				},
				Spec: qsv1a1.QuarksSecretSpec{
					Type:       "copy",
					SecretName: "generated-secret-copy",
				},
			}

			By("Creating user password secret")
			tearDown, err := env.CreateSecret(env.Namespace, *passwordSecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Creating copy quarks secret in copy namespace")
			_, tearDown, err = env.CreateQuarksSecret(copyNamespace, *quarksCopySecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Creating quarkssecret with copies")
			createQuarksSecretWithCopies(copyNamespace)
		})

		It("should copy into other namespaces if copy quarks secret if found", func() {
			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the copied secret data")
			secret, err := env.CollectSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(BeZero())
			Expect(string(secret.Data["password"])).To(Equal("securepassword"))
		})

		It("should update the copies in other namespaces if copy quarks secret if found", func() {
			By("Updating the user password secret")
			passwordSecret.StringData["password"] = "supersecurepassword"
			_, tearDown, err := env.UpdateSecret(env.Namespace, *passwordSecret)
			Expect(err).NotTo(HaveOccurred())
			tearDowns = append(tearDowns, tearDown)

			By("Checking the quarkssecret status")
			checkStatus()

			By("Checking the copied secret data")
			secret, err := env.CollectSecret(copyNamespace, "generated-secret-copy")
			Expect(err).NotTo(HaveOccurred())
			Expect(len(secret.Labels)).To(BeZero())
			Expect(string(secret.Data["password"])).To(Equal("supersecurepassword"))
		})
	})
})