package quarkssecret

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"code.cloudfoundry.org/quarks-secret/pkg/credsgen"
	qsv1a1 "code.cloudfoundry.org/quarks-secret/pkg/kube/apis/quarkssecret/v1alpha1"
	"code.cloudfoundry.org/quarks-utils/pkg/config"
	"code.cloudfoundry.org/quarks-utils/pkg/ctxlog"
	"code.cloudfoundry.org/quarks-utils/pkg/pointers"
)

// NewCopyReconciler returns a new ReconcileCopy
func NewCopyReconciler(ctx context.Context, config *config.Config, mgr manager.Manager, generator credsgen.Generator, srf setReferenceFunc) reconcile.Reconciler {
	return &ReconcileCopy{
		ctx:          ctx,
		config:       config,
		client:       mgr.GetClient(),
		scheme:       mgr.GetScheme(),
		generator:    generator,
		setReference: srf,
	}
}

// ReconcileCopy reconciles an QuarksSecret object
type ReconcileCopy struct {
	ctx          context.Context
	client       client.Client
	generator    credsgen.Generator
	scheme       *runtime.Scheme
	setReference setReferenceFunc
	config       *config.Config
}

// Reconcile reads sets the copied field in status spec to false and copies the secrets from source namespace
// to the target namespaces.
func (r *ReconcileCopy) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	qsec := &qsv1a1.QuarksSecret{}

	// Set the ctx to be Background, as the top-level context for incoming requests.
	ctx, cancel := context.WithTimeout(r.ctx, r.config.CtxTimeOut)
	defer cancel()

	ctxlog.Infof(ctx, "Reconciling QuarksSecret %s", request.NamespacedName)
	err := r.client.Get(ctx, request.NamespacedName, qsec)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			ctxlog.Info(ctx, "Skip reconcile: quarks secret not found")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		ctxlog.Info(ctx, "Error reading the object")
		return reconcile.Result{}, errors.Wrap(err, "Error reading quarksSecret")
	}

	r.updateCopyStatus(ctx, qsec, false)

	err = r.handleQuarksSecretCopies(ctx, qsec)
	if err != nil {
		ctxlog.Errorf(ctx, "Error handling quarks secret copies '%s'", qsec.Name, err.Error())
	}

	r.updateCopyStatus(ctx, qsec, true)
	return reconcile.Result{}, nil
}

func (r *ReconcileCopy) updateCopyStatus(ctx context.Context, qsec *qsv1a1.QuarksSecret, copyStatus bool) {
	qsec.Status.Copied = pointers.Bool(copyStatus)
	ctxlog.Infof(ctx, "1a")

	err := r.client.Status().Update(ctx, qsec)
	if err != nil {
		ctxlog.Errorf(ctx, "could not create or update QuarksSecret status '%s': %v", qsec.GetNamespacedName(), err)
	}
	ctxlog.Infof(ctx, "1b")

}

func (r *ReconcileCopy) handleQuarksSecretCopies(ctx context.Context, sourceQuarksSecret *qsv1a1.QuarksSecret) error {
	for _, copy := range sourceQuarksSecret.Spec.Copies {

		ctxlog.Infof(ctx, "4")

		sourceSecret, err := r.getSourceSecret(ctx, sourceQuarksSecret)
		if err != nil {
			return err
		}

		ctxlog.Infof(ctx, "5")

		ok, targetQuarksSecret, err := r.validateTargetNamespace(ctx, sourceQuarksSecret, copy, sourceSecret)
		if err != nil {
			return errors.Wrapf(err, "could not validate")
		}

		ctxlog.Infof(ctx, "6")

		targetSecret := &corev1.Secret{}
		if ok {
			targetSecret.Name = copy.Name
			targetSecret.Namespace = copy.Namespace
			targetSecret.Data = sourceSecret.Data
			targetSecret.Annotations = sourceSecret.Annotations
			targetSecret.Labels = sourceSecret.Labels

			annotations := targetSecret.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[qsv1a1.AnnotationCopyOf] = sourceQuarksSecret.GetNamespacedName()
			targetSecret.SetAnnotations(annotations)

			if targetQuarksSecret == nil {
				if err := r.updateCopySecret(ctx, targetSecret); err != nil {
					return err
				}
				ctxlog.WithEvent(sourceQuarksSecret, "CopyReconcile").Infof(ctx, "Copy secret '%s' has been updated in namespace '%s'", copy.Name, copy.Namespace)
			} else {
				if err := r.createUpdateCopySecret(ctx, targetSecret, targetQuarksSecret); err != nil {
					return err
				}
			}
		} else {
			ctxlog.WithEvent(sourceQuarksSecret, "CopyReconcile").Infof(ctx, "Skip copy creation: Secret/QSecret '%s' must exist and have the appropriate annotation to receive a copy", copy.String())
		}
	}

	return nil
}

func (r *ReconcileCopy) getSourceSecret(ctx context.Context, qsec *qsv1a1.QuarksSecret) (*corev1.Secret, error) {
	secretName := qsec.Spec.SecretName
	sourceSecret := &corev1.Secret{}
	err := r.client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: qsec.GetNamespace()}, sourceSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "could not fetch source secret")
	}

	return sourceSecret, nil
}

// validateTargetNamespace checks if there is a valid secret or valid quarks secret in the target namespace.
func (r *ReconcileCopy) validateTargetNamespace(ctx context.Context, sourceQuarksSecret *qsv1a1.QuarksSecret, copy qsv1a1.Copy, sourceSecret *corev1.Secret) (bool, *qsv1a1.QuarksSecret, error) {
	notFoundQuarksSecret := false
	notFoundSecret := false

	targetQuarksSecret := &qsv1a1.QuarksSecret{}
	err := r.client.Get(ctx, types.NamespacedName{Name: sourceQuarksSecret.Name, Namespace: copy.Namespace}, targetQuarksSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			notFoundQuarksSecret = true
		} else {
			return false, nil, errors.Wrapf(err, "could not get target quarks secret")
		}
	}

	targetSecret := &corev1.Secret{}
	err = r.client.Get(ctx, types.NamespacedName{Name: copy.Name, Namespace: copy.Namespace}, targetSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			notFoundSecret = true
		} else {
			return false, nil, errors.Wrapf(err, "could not get target secret")
		}
	}

	// If both are absent, we will skip the copying process
	if notFoundSecret && notFoundQuarksSecret {
		ctxlog.WithEvent(sourceQuarksSecret, "ValidateTargetNamespace").Infof(ctx, "No Valid Quarks Secret or Secret found in the target namespace '%s'", copy.Namespace)
		return false, nil, nil
	}

	// If both of them are found, give preference to quarks secret
	if !notFoundSecret && !notFoundQuarksSecret {
		ctxlog.WithEvent(sourceQuarksSecret, "ValidateTargetNamespace").Infof(ctx, "Both Quarks Secret & Secret found. Giving preference to Quarks Secret")
		notFoundSecret = true
	}

	if notFoundSecret {
		ctxlog.WithEvent(sourceQuarksSecret, "ValidateTargetNamespace").Infof(ctx, "Valid QuarksSecret found")

		annotations := targetQuarksSecret.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}

		if targetQuarksSecret.Spec.Type != qsv1a1.SecretCopy {
			ctxlog.WithEvent(sourceQuarksSecret, "ValidateTargetNamespace").Infof(ctx, "Invalid type for Quarks Secret. It must be 'copy' type.")
			return false, nil, nil
		}

		ctxlog.Infof(ctx, "taget qsec %v", annotations, sourceQuarksSecret.GetNamespacedName())

		return validateAnnotation(ctx, sourceQuarksSecret, annotations, sourceQuarksSecret.GetNamespacedName()), targetQuarksSecret, nil
	} else if notFoundQuarksSecret {
		ctxlog.WithEvent(sourceQuarksSecret, "ValidateTargetNamespace").Infof(ctx, "Valid Secret found")

		annotations := targetSecret.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}

		return validateAnnotation(ctx, sourceQuarksSecret, annotations, sourceQuarksSecret.GetNamespacedName()), nil, nil
	}

	return false, nil, nil
}

func validateAnnotation(ctx context.Context, sourceQsec *qsv1a1.QuarksSecret, secretAnnotations map[string]string, copyOf string) bool {
	valid := true
	if secretAnnotations[qsv1a1.AnnotationCopyOf] != copyOf {
		ctxlog.WithEvent(sourceQsec, "ValidateTargetNamespace").Infof(ctx, "doesn't have the corresponding annotation %s vs %s", secretAnnotations[qsv1a1.AnnotationCopyOf], copyOf)
		valid = false
	}

	return valid
}

func (r *ReconcileCopy) createUpdateCopySecret(ctx context.Context, targetSecret *corev1.Secret, targetQuarksSecret *qsv1a1.QuarksSecret) error {
	ctxlog.Infof(ctx, "8")

	if targetQuarksSecret != nil {
		if err := r.setReference(targetQuarksSecret, targetSecret, r.scheme); err != nil {
			return errors.Wrapf(err, "error setting owner for secret '%s' to QuarksSecret '%s'", targetSecret.GetName(), targetQuarksSecret.GetNamespacedName())
		}
	}

	ctxlog.Infof(ctx, "9")
	ctxlog.Infof(ctx, "data 2", targetSecret.Data)

	op, err := controllerutil.CreateOrUpdate(ctx, r.client, targetSecret, func() error { return nil })
	if err != nil {
		return errors.Wrapf(err, "could not create or update target secret '%s/%s'", targetSecret.Namespace, targetSecret.GetName())
	}
	if op != "unchanged" {
		ctxlog.Debugf(ctx, "Target Secret '%s' has been %s", targetSecret.Name, op)
	}

	return nil
}

// updateCopySecret updates a copied destination Secret
func (r *ReconcileCopy) updateCopySecret(ctx context.Context, secret *corev1.Secret) error {
	// If this is a copy (lives in a different namespace), we only do an update,
	// since we're not allowed to create, and we don't set a reference, because
	// cross namespace references are not supported
	uncachedSecret := &unstructured.Unstructured{}
	uncachedSecret.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Kind:    "Secret",
		Version: "v1",
	})
	uncachedSecret.SetName(secret.Name)
	uncachedSecret.SetNamespace(secret.Namespace)
	uncachedSecret.SetLabels(secret.Labels)
	uncachedSecret.SetAnnotations(secret.Annotations)
	uncachedSecret.Object["data"] = secret.Data
	err := r.client.Update(ctx, uncachedSecret)

	if err != nil {
		return errors.Wrapf(err, "could not update secret '%s/%s'", secret.Namespace, secret.GetName())
	}

	return nil
}
