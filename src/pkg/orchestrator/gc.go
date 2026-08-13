package orchestrator

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// GarbageCollectOrphanedWorkers deletes orchestrator-spawned worker Deployments
// (and their HorizontalPodAutoscalers) whose connection no longer exists in the
// control-plane DB. It is a safety net for connection deletes whose orchestrator
// teardown failed — or predate the teardown fix (#175/#176): without it, an
// orphaned worker crash-loops forever (observed in prod: 2 connections leaked 8
// objects that crash-looped ~3 days).
//
// Every orchestrator worker carries app=vrsky + pipeline=<connectionID> labels.
// `exists` reports whether a connection ID is still present in the DB; on a
// lookup error it MUST return true, so a transient DB blip never deletes live
// workers. Returns the number of connections whose resources were removed.
func GarbageCollectOrphanedWorkers(ctx context.Context, k8s kubernetes.Interface, namespace string, exists func(connectionID string) bool) (int, error) {
	deployClient := k8s.AppsV1().Deployments(namespace)
	hpaClient := k8s.AutoscalingV2().HorizontalPodAutoscalers(namespace)

	// All orchestrator-spawned workers carry app=vrsky (LabelAppValue); the
	// standing services use app=vrsky-<name>, so this selector is exact.
	sel := BuildLabelSelector(map[string]string{LabelApp: LabelAppValue})
	deployments, err := deployClient.List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return 0, fmt.Errorf("list worker deployments: %w", err)
	}

	// Unique connection IDs across the worker deployments (each connection has a
	// src + dst worker, so dedupe before checking the DB).
	connIDs := make(map[string]struct{})
	for i := range deployments.Items {
		if cid := deployments.Items[i].Labels[LabelPipeline]; cid != "" {
			connIDs[cid] = struct{}{}
		}
	}

	removed := 0
	for cid := range connIDs {
		if exists(cid) {
			continue
		}
		// Orphan: delete every Deployment + HPA for this connection.
		connSel := BuildLabelSelector(GetDeploymentLabelsForConnection(cid))
		if ds, lerr := deployClient.List(ctx, metav1.ListOptions{LabelSelector: connSel}); lerr == nil {
			for i := range ds.Items {
				_ = deployClient.Delete(ctx, ds.Items[i].Name, metav1.DeleteOptions{})
			}
		}
		if hs, lerr := hpaClient.List(ctx, metav1.ListOptions{LabelSelector: connSel}); lerr == nil {
			for i := range hs.Items {
				_ = hpaClient.Delete(ctx, hs.Items[i].Name, metav1.DeleteOptions{})
			}
		}
		removed++
	}
	return removed, nil
}
