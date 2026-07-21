// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and Open Control Plane contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package setup contains local e2e test extensions.
package setup

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	certManagerURL = "https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml"
	k0smotronURL   = "https://docs.k0smotron.io/stable/install.yaml"
	openmcpNS      = "openmcp-system"
)

// K0smotronSetup installs cert-manager and k0smotron, then ensures the onboarding
// Cluster object uses the k0smotron profile and stamps the platform Cluster Ready.
type K0smotronSetup struct {
	// PlatformKubeconfig is captured during Install for use by K0smotronClusterProvider.
	PlatformKubeconfig []byte
	// StopWatchdog cancels the AR watchdog goroutine.
	StopWatchdog context.CancelFunc
}

func (k *K0smotronSetup) Name() string { return "k0smotron-setup" }

func (k *K0smotronSetup) RegisterSchemes(_ context.Context, _ *runtime.Scheme) error { return nil }

func (k *K0smotronSetup) Install(ctx context.Context, cfg *envconf.Config) error {
	// Capture management cluster kubeconfig before anything changes.
	raw, err := restConfigToKubeconfig(cfg.Client().RESTConfig())
	if err != nil {
		return fmt.Errorf("capture management kubeconfig: %w", err)
	}
	k.PlatformKubeconfig = raw

	// Start AR watchdog immediately using a detached context so it survives
	// past the Bootstrap install phase. Watchdog re-triggers Pending ARs when
	// their kubeconfig secret appears (cluster-provider-k0smotron doesn't watch secrets).
	watchdogCtx, watchdogCancel := context.WithTimeout(context.Background(), 20*time.Minute)
	k.StopWatchdog = watchdogCancel
	StartARWatchdog(watchdogCtx, cfg.Client().RESTConfig())

	r := cfg.Client().Resources()
	if err := applyURL(ctx, r, certManagerURL); err != nil {
		return fmt.Errorf("install cert-manager: %w", err)
	}
	if err := k.waitDeployments(ctx, cfg, "cert-manager", 3, 2*time.Minute); err != nil {
		return fmt.Errorf("wait cert-manager: %w", err)
	}
	if err := applyURL(ctx, r, k0smotronURL); err != nil {
		return fmt.Errorf("install k0smotron: %w", err)
	}
	if err := k.waitDeployments(ctx, cfg, "k0smotron", 1, 90*time.Second); err != nil {
		return fmt.Errorf("wait k0smotron: %w", err)
	}
	if err := ensureCluster(ctx, cfg, "onboarding", "onboarding"); err != nil {
		return err
	}
	// Wait for the onboarding kubeconfig secret to exist before returning.
	// The cluster-provider-k0smotron AR controller doesn't watch secrets, so if the
	// secret appears after the AR is first reconciled it gets stuck in backoff.
	// Waiting here ensures the secret is present when ARs are first created.
	klog.Info("waiting for onboarding kubeconfig secret...")
	if err := wait.For(func(ctx context.Context) (bool, error) {
		secretList := &corev1.SecretList{}
		if err := cfg.Client().Resources("k0smotron").List(ctx, secretList); err != nil {
			return false, nil
		}
		for _, s := range secretList.Items {
			if strings.HasPrefix(s.Name, "onboarding-") && strings.HasSuffix(s.Name, "-kubeconfig") {
				return true, nil
			}
		}
		return false, nil
	}, wait.WithTimeout(3*time.Minute)); err != nil {
		return fmt.Errorf("wait onboarding kubeconfig secret: %w", err)
	}
	// platform Cluster CR (profile=kind) has no kind ClusterProvider to reconcile it.
	// Stamp it Ready so verifyEnvironment's ClustersReady wait passes.
	if err := markClusterReady(ctx, cfg, "platform"); err != nil {
		return fmt.Errorf("mark platform cluster ready: %w", err)
	}
	return nil
}

// applyURL fetches a YAML manifest URL and creates each object, ignoring already-exists.
func applyURL(ctx context.Context, r *resources.Resources, url string) error {
	klog.Infof("applying %s", url)
	return decoder.DecodeURL(ctx, url, func(ctx context.Context, obj k8s.Object) error {
		if err := r.Create(ctx, obj); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				return nil
			}
			// Ignore immutable field errors on re-runs
			if strings.Contains(err.Error(), "field is immutable") {
				return nil
			}
			return err
		}
		return nil
	})
}

func (k *K0smotronSetup) waitDeployments(ctx context.Context, cfg *envconf.Config, ns string, count int, timeout time.Duration) error {
	klog.Infof("waiting for %d deployment(s) in %s...", count, ns)
	depList := &unstructured.UnstructuredList{}
	depList.SetAPIVersion("apps/v1")
	depList.SetKind("DeploymentList")
	return wait.For(
		func(ctx context.Context) (bool, error) {
			if err := cfg.Client().Resources(ns).List(ctx, depList); err != nil {
				return false, nil
			}
			ready := 0
			for i := range depList.Items {
				avail, _, _ := unstructured.NestedInt64(depList.Items[i].Object, "status", "availableReplicas")
				if avail > 0 {
					ready++
				}
			}
			return ready >= count, nil
		},
		wait.WithTimeout(timeout),
	)
}

func ensureCluster(ctx context.Context, cfg *envconf.Config, name, purpose string) error {
	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "clusters.openmcp.cloud/v1alpha1",
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": openmcpNS,
			},
			"spec": map[string]interface{}{
				"kubernetes": map[string]interface{}{},
				"profile":    "k0smotron",
				"purposes":   []interface{}{purpose},
				"tenancy":    "Shared",
			},
		},
	}
	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("clusters.openmcp.cloud/v1alpha1")
	existing.SetKind("Cluster")
	if err := cfg.Client().Resources(openmcpNS).Get(ctx, name, openmcpNS, existing); err == nil {
		profile, _, _ := unstructured.NestedString(existing.Object, "spec", "profile")
		if profile == "k0smotron" {
			klog.Infof("cluster %s already has k0smotron profile", name)
			return nil
		}
		// spec.profile is immutable — must delete and recreate.
		klog.Infof("cluster %s has profile %q — deleting and recreating with k0smotron", name, profile)
		existing.SetFinalizers(nil)
		if err := cfg.Client().Resources(openmcpNS).Update(ctx, existing); err != nil {
			klog.Warningf("strip finalizers on %s: %v", name, err)
		}
		if err := cfg.Client().Resources(openmcpNS).Delete(ctx, existing); err != nil && !strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("delete cluster %s: %w", name, err)
		}
		if err := wait.For(func(ctx context.Context) (bool, error) {
			tmp := &unstructured.Unstructured{}
			tmp.SetAPIVersion("clusters.openmcp.cloud/v1alpha1")
			tmp.SetKind("Cluster")
			err := cfg.Client().Resources(openmcpNS).Get(ctx, name, openmcpNS, tmp)
			return err != nil, nil
		}, wait.WithTimeout(30*time.Second)); err != nil {
			klog.Warningf("cluster %s not fully deleted after 30s, proceeding anyway", name)
		}
	}
	if err := cfg.Client().Resources(openmcpNS).Create(ctx, desired); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("create cluster %s: %w", name, err)
	}
	klog.Infof("cluster %s created with k0smotron profile", name)
	return nil
}

func markClusterReady(ctx context.Context, cfg *envconf.Config, name string) error {
	klog.Infof("stamping cluster %s status Ready", name)
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("clusters.openmcp.cloud/v1alpha1")
	obj.SetKind("Cluster")
	obj.SetName(name)
	obj.SetNamespace(openmcpNS)
	_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "observedGeneration")
	_ = unstructured.SetNestedField(obj.Object, "Ready", "status", "phase")
	_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
		map[string]interface{}{
			"type":               "Ready",
			"status":             string(corev1.ConditionTrue),
			"reason":             "ManuallyMarked",
			"message":            "management kind cluster",
			"lastTransitionTime": "2026-07-21T00:00:00Z",
		},
	}, "status", "conditions")
	patch, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		return fmt.Errorf("encode status patch: %w", err)
	}
	return cfg.Client().Resources(openmcpNS).PatchStatus(ctx, obj, k8s.Patch{
		PatchType: k8stypes.MergePatchType,
		Data:      patch,
	})
}

// restConfigToKubeconfig serialises a *rest.Config to raw kubeconfig YAML bytes.
// StartARWatchdog runs a background goroutine that re-triggers Pending AccessRequests
// whenever their kubeconfig secret appears in k0smotron namespace.
// The cluster-provider-k0smotron controller doesn't watch secrets, so ARs get stuck
// in exponential backoff after a secret appears post-reconcile.
// Call this once after Bootstrap; it exits when ctx is cancelled.
func StartARWatchdog(ctx context.Context, restCfg *rest.Config) {
	go func() {
		r, err := resources.New(restCfg)
		if err != nil {
			klog.Warningf("AR watchdog: build client: %v", err)
			return
		}
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				retriggerPendingARs(ctx, r)
			}
		}
	}()
}

func retriggerPendingARs(ctx context.Context, r *resources.Resources) {
	// List all kubeconfig secrets across k0smotron namespace.
	secretList := &corev1.SecretList{}
	if err := r.WithNamespace("k0smotron").List(ctx, secretList); err != nil {
		return
	}
	availableSecrets := map[string]bool{}
	for _, s := range secretList.Items {
		if strings.HasSuffix(s.Name, "-kubeconfig") {
			availableSecrets[s.Name] = true
		}
	}

	// List all AccessRequests in all namespaces.
	arList := &unstructured.UnstructuredList{}
	arList.SetAPIVersion("clusters.openmcp.cloud/v1alpha1")
	arList.SetKind("AccessRequestList")
	if err := r.List(ctx, arList); err != nil {
		return
	}
	for i := range arList.Items {
		ar := &arList.Items[i]
		phase, _, _ := unstructured.NestedString(ar.Object, "status", "phase")
		if phase != "Pending" {
			continue
		}
		// Check if any condition mentions a missing kubeconfig secret.
		conditions, _, _ := unstructured.NestedSlice(ar.Object, "status", "conditions")
		msg := ""
		for _, c := range conditions {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if m, ok := cm["message"].(string); ok {
				msg = m
				break
			}
		}
		if !strings.Contains(msg, "kubeconfig") || !strings.Contains(msg, "not found") {
			continue
		}
		// Extract the secret name from the error message.
		// Format: "failed to get kubeconfig secret k0smotron/<name>: ..."
		for secretName := range availableSecrets {
			if strings.Contains(msg, secretName) {
				// Secret now exists — re-trigger by bumping an annotation.
				annotations := ar.GetAnnotations()
				if annotations == nil {
					annotations = map[string]string{}
				}
				annotations["k0smotron.io/retrigger"] = fmt.Sprintf("%d", time.Now().UnixNano())
				ar.SetAnnotations(annotations)
				if err := r.WithNamespace(ar.GetNamespace()).Update(ctx, ar); err == nil {
					klog.Infof("AR watchdog: re-triggered %s/%s (secret %s now available)",
						ar.GetNamespace(), ar.GetName(), secretName)
				}
				break
			}
		}
	}
}

func restConfigToKubeconfig(cfg *rest.Config) ([]byte, error) {
	apiCfg := clientcmdapi.NewConfig()
	apiCfg.Clusters["platform"] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
		InsecureSkipTLSVerify:    cfg.Insecure,
	}
	apiCfg.AuthInfos["platform"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
		Token:                 cfg.BearerToken,
	}
	apiCfg.Contexts["platform"] = &clientcmdapi.Context{
		Cluster:  "platform",
		AuthInfo: "platform",
	}
	apiCfg.CurrentContext = "platform"
	return runtime.Encode(clientcmdlatest.Codec, apiCfg)
}

// K0smotronClusterProvider implements clusterutils.ClusterProvider for k0smotron clusters.
// "platform" returns the management kind cluster kubeconfig; others come from k0smotron secrets.
type K0smotronClusterProvider struct {
	PlatformKubeconfig []byte
	RestConfig         *rest.Config
}

func (p *K0smotronClusterProvider) List() ([]string, error) {
	r, err := resources.New(p.RestConfig)
	if err != nil {
		return nil, fmt.Errorf("build resources client: %w", err)
	}
	secretList := &corev1.SecretList{}
	if err := r.WithNamespace("k0smotron").List(context.Background(), secretList); err != nil {
		return nil, fmt.Errorf("list k0smotron secrets: %w", err)
	}
	names := []string{"platform"}
	seen := map[string]bool{"platform": true}
	for _, s := range secretList.Items {
		if !strings.HasSuffix(s.Name, "-kubeconfig") {
			continue
		}
		parts := strings.SplitN(s.Name, "-", 2)
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		if !seen[parts[0]] {
			seen[parts[0]] = true
			names = append(names, parts[0])
		}
	}
	return names, nil
}

func (p *K0smotronClusterProvider) KubeConfig(name string, _ bool) (string, error) {
	if name == "platform" {
		return string(p.PlatformKubeconfig), nil
	}
	r, err := resources.New(p.RestConfig)
	if err != nil {
		return "", fmt.Errorf("build resources client: %w", err)
	}
	secretList := &corev1.SecretList{}
	if err := r.WithNamespace("k0smotron").List(context.Background(), secretList); err != nil {
		return "", fmt.Errorf("list k0smotron secrets: %w", err)
	}
	for _, s := range secretList.Items {
		if !strings.HasPrefix(s.Name, name+"-") || !strings.HasSuffix(s.Name, "-kubeconfig") {
			continue
		}
		data, ok := s.Data["value"]
		if !ok || len(data) == 0 {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return string(data), nil
		}
		return string(decoded), nil
	}
	return "", fmt.Errorf("no kubeconfig secret for cluster %s in k0smotron namespace", name)
}
