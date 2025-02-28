/*
Copyright 2021 The Tekton Authors

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

package tektonconfig

import (
	"context"
	"fmt"

	"github.com/tektoncd/operator/pkg/apis/operator/v1alpha1"
	clientset "github.com/tektoncd/operator/pkg/client/clientset/versioned"
	tektonConfigreconciler "github.com/tektoncd/operator/pkg/client/injection/reconciler/operator/v1alpha1/tektonconfig"
	"github.com/tektoncd/operator/pkg/reconciler/common"
	"github.com/tektoncd/operator/pkg/reconciler/shared/tektonconfig/pipeline"
	"github.com/tektoncd/operator/pkg/reconciler/shared/tektonconfig/trigger"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"knative.dev/pkg/logging"
	pkgreconciler "knative.dev/pkg/reconciler"
)

// Reconciler implements controller.Reconciler for TektonConfig resources.
type Reconciler struct {
	// kubeClientSet allows us to talk to the k8s for core APIs
	kubeClientSet kubernetes.Interface
	// operatorClientSet allows us to configure operator objects
	operatorClientSet clientset.Interface
	// Platform-specific behavior to affect the transform
	extension common.Extension
}

// Check that our Reconciler implements controller.Reconciler
var _ tektonConfigreconciler.Interface = (*Reconciler)(nil)
var _ tektonConfigreconciler.Finalizer = (*Reconciler)(nil)

// FinalizeKind removes all resources after deletion of a TektonConfig.
func (r *Reconciler) FinalizeKind(ctx context.Context, original *v1alpha1.TektonConfig) pkgreconciler.Event {
	logger := logging.FromContext(ctx)

	if err := r.extension.Finalize(ctx, original); err != nil {
		logger.Error("Failed to finalize platform resources", err)
	}

	if original.Spec.Profile == v1alpha1.ProfileLite {
		return pipeline.TektonPipelineCRDelete(ctx, r.operatorClientSet.OperatorV1alpha1().TektonPipelines(), v1alpha1.PipelineResourceName)
	} else {
		// TektonPipeline and TektonTrigger is common for profile type basic and all
		if err := trigger.TektonTriggerCRDelete(ctx, r.operatorClientSet.OperatorV1alpha1().TektonTriggers(), v1alpha1.TriggerResourceName); err != nil {
			return err
		}
		if err := pipeline.TektonPipelineCRDelete(ctx, r.operatorClientSet.OperatorV1alpha1().TektonPipelines(), v1alpha1.PipelineResourceName); err != nil {
			return err
		}
	}

	return nil
}

// ReconcileKind compares the actual state with the desired, and attempts to
// converge the two.
func (r *Reconciler) ReconcileKind(ctx context.Context, tc *v1alpha1.TektonConfig) pkgreconciler.Event {
	logger := logging.FromContext(ctx)
	tc.Status.InitializeConditions()

	logger.Infow("Reconciling TektonConfig", "status", tc.Status)
	if tc.GetName() != v1alpha1.ConfigResourceName {
		msg := fmt.Sprintf("Resource ignored, Expected Name: %s, Got Name: %s",
			v1alpha1.ConfigResourceName,
			tc.GetName(),
		)
		logger.Error(msg)
		tc.Status.MarkNotReady(msg)
		return nil
	}

	tc.SetDefaults(ctx)

	if err := r.extension.PreReconcile(ctx, tc); err != nil {
		if err == v1alpha1.RECONCILE_AGAIN_ERR {
			return err
		}
		tc.Status.MarkPreInstallFailed(err.Error())
		return err
	}

	tc.Status.MarkPreInstallComplete()

	// Create TektonPipeline CR
	if err := pipeline.CreatePipelineCR(ctx, tc, r.operatorClientSet.OperatorV1alpha1()); err != nil {
		tc.Status.MarkComponentNotReady(fmt.Sprintf("TektonPipeline: %s", err.Error()))
		return err
	}

	// Create TektonTrigger CR if the profile is all or basic
	if tc.Spec.Profile == v1alpha1.ProfileAll || tc.Spec.Profile == v1alpha1.ProfileBasic {
		if err := trigger.CreateTriggerCR(ctx, tc, r.operatorClientSet.OperatorV1alpha1()); err != nil {
			tc.Status.MarkComponentNotReady(fmt.Sprintf("TektonTrigger: %s", err.Error()))
			return err
		}
	} else {
		if err := trigger.TektonTriggerCRDelete(ctx, r.operatorClientSet.OperatorV1alpha1().TektonTriggers(), v1alpha1.TriggerResourceName); err != nil {
			tc.Status.MarkComponentNotReady(fmt.Sprintf("TektonTrigger: %s", err.Error()))
			return err
		}
	}

	if err := common.Prune(ctx, r.kubeClientSet, tc); err != nil {
		tc.Status.MarkComponentNotReady(fmt.Sprintf("tekton-resource-pruner: %s", err.Error()))
		logger.Error(err)
	}

	tc.Status.MarkComponentsReady()

	if err := r.extension.PostReconcile(ctx, tc); err != nil {
		tc.Status.MarkPostInstallFailed(err.Error())
		return err
	}

	tc.Status.MarkPostInstallComplete()

	// Update the object for any spec changes
	if _, err := r.operatorClientSet.OperatorV1alpha1().TektonConfigs().Update(ctx, tc, v1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}
