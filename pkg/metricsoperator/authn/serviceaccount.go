package authn

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openmcp-project/extensibility-utils/pkg/tokenrefresh"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	serviceAccountVolume    = "kube-api-access"

	// TokenServiceProvider is the service provider name used to build the token annotation keys,
	// shared by the initial token write in Configure and the tokenrefresh controller.
	TokenServiceProvider = "metrics-operator"
)

// ManagedServiceAccount references the managed ServiceAccount object
type ManagedServiceAccount struct {
	types.NamespacedName
}

func (m *ManagedServiceAccount) KubeAPIAccess() string {
	return fmt.Sprintf("kube-api-access-%s", m.Name)
}

// TokenBinding returns the ServiceAccount and the token Secret in the given workload namespace for the token refresh controller.
func (m *ManagedServiceAccount) TokenBinding(workloadNamespace string) tokenrefresh.Binding {
	return tokenrefresh.Binding{
		ServiceAccount: m.NamespacedName,
		Secret:         types.NamespacedName{Name: m.KubeAPIAccess(), Namespace: workloadNamespace},
	}
}

// AddCAToHelmValues removes conflicting volumes, volumeMounts and envVars (matching by name and/or mountPath) and
// adds a volume, volumeMount and envVar on all Flux controller helm values sections to import the custom CA certificate.
// nolint:gocyclo
func AddAuthToHelmValues(values *apiextensionsv1.JSON, remoteCluster resources.ManagedCluster, saName string) (*apiextensionsv1.JSON, error) {
	authVolume := corev1.Volume{
		Name: serviceAccountVolume,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: saName,
			},
		},
	}

	authVolumeMount := corev1.VolumeMount{
		Name:      serviceAccountVolume,
		ReadOnly:  true,
		MountPath: serviceAccountMountPath,
	}

	remoteHost, remotePort := remoteCluster.GetHostAndPort()

	hostEnvVar := corev1.EnvVar{
		Name:  "KUBERNETES_SERVICE_HOST",
		Value: remoteHost,
	}

	portEnvVar := corev1.EnvVar{
		Name:  "KUBERNETES_SERVICE_PORT",
		Value: remotePort,
	}

	// namespaceEnvVar := corev1.EnvVar{
	// 	Name:  "POD_NAMESPACE",
	// 	Value: remoteCluster.GetDefaultNamespace(),
	// }

	var root = map[string]json.RawMessage{}

	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &root); err != nil {
			return nil, fmt.Errorf("failed to unmarshal helm values: %w", err)
		}
		if root == nil {
			root = make(map[string]json.RawMessage)
		}
	}
	var extraVolumes []corev1.Volume
	if err := unmarshalIfPresent(root, "extraVolumes", &extraVolumes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", ".Values.extraVolumes", err)
	}

	extraVolumes = removeConflictingVolumesAndAppend(extraVolumes, authVolume)

	extraVolumesRaw, err := json.Marshal(extraVolumes)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Vales.extraVolumes: %w", err)
	}
	root["extraVolumes"] = extraVolumesRaw

	var initValues map[string]json.RawMessage
	var initExtraVolumeMounts []corev1.VolumeMount
	var initExtraEnv []corev1.EnvVar
	if err := unmarshalIfPresent(root, "init", &initValues); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", ".Values.init", err)
	}
	if initValues == nil {
		initValues = make(map[string]json.RawMessage)
	}
	if err := unmarshalIfPresent(initValues, "extraVolumeMounts", &initExtraVolumeMounts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", ".Values.init.extraVolumeMounts", err)
	}
	if err := unmarshalIfPresent(initValues, "extraEnv", &initExtraEnv); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", ".Values.init.extraEnv", err)
	}

	initExtraVolumeMounts = removeConflictingVolumeMountsAndAppend(initExtraVolumeMounts, authVolumeMount)
	initExtraEnv = removeConflictingEnvVarsAndAppend(initExtraEnv, hostEnvVar)
	initExtraEnv = removeConflictingEnvVarsAndAppend(initExtraEnv, portEnvVar)
	// initExtraEnv = removeConflictingEnvVarsAndAppend(initExtraEnv, namespaceEnvVar)

	initExtraVolumeMountsRaw, err := json.Marshal(initExtraVolumeMounts)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal .Values.init.extraVolumeMounts: %w", err)
	}

	initExtraEnvRaw, err := json.Marshal(initExtraEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal V.Values.init.extraEnv: %w", err)
	}

	initValues["extraVolumeMounts"] = initExtraVolumeMountsRaw
	initValues["extraEnv"] = initExtraEnvRaw

	initValuesRaw, err := json.Marshal(initValues)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal .Values.init: %w", err)
	}

	root["init"] = initValuesRaw

	var managerValues map[string]json.RawMessage
	var managerExtraVolumeMounts []corev1.VolumeMount
	var managerExtraEnv []corev1.EnvVar
	if err := unmarshalIfPresent(root, "manager", &managerValues); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", ".Values.manager", err)
	}
	if managerValues == nil {
		managerValues = make(map[string]json.RawMessage)
	}
	if err := unmarshalIfPresent(managerValues, "extraVolumeMounts", &managerExtraVolumeMounts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", ".Values.manager.extraVolumeMounts", err)
	}
	if err := unmarshalIfPresent(managerValues, "extraEnv", &managerExtraEnv); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s: %w", ".Values.manager.extraEnv", err)
	}

	managerExtraVolumeMounts = removeConflictingVolumeMountsAndAppend(managerExtraVolumeMounts, authVolumeMount)
	managerExtraEnv = removeConflictingEnvVarsAndAppend(managerExtraEnv, hostEnvVar)
	managerExtraEnv = removeConflictingEnvVarsAndAppend(managerExtraEnv, portEnvVar)
	// managerExtraEnv = removeConflictingEnvVarsAndAppend(managerExtraEnv, namespaceEnvVar)

	managerExtraVolumeMountsRaw, err := json.Marshal(managerExtraVolumeMounts)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal .Values.manager.extraVolumes: %w", err)
	}
	managerExtraEnvRaw, err := json.Marshal(managerExtraEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal .Values.manager.extraEnv: %w", err)
	}

	managerValues["extraVolumeMounts"] = managerExtraVolumeMountsRaw
	managerValues["extraEnv"] = managerExtraEnvRaw

	managerValuesRaw, err := json.Marshal(managerValues)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal .Values.manager: %w", err)
	}

	root["manager"] = managerValuesRaw

	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal helm values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: out}, nil
}

func removeConflictingVolumesAndAppend(volumes []corev1.Volume, newVolume corev1.Volume) []corev1.Volume {
	updated := []corev1.Volume{}
	for _, volume := range volumes {
		if volume.Name != newVolume.Name {
			updated = append(updated, volume)
		}
	}
	updated = append(updated, newVolume)
	return updated
}

func removeConflictingVolumeMountsAndAppend(volumeMounts []corev1.VolumeMount, newVolumeMount corev1.VolumeMount) []corev1.VolumeMount {
	updated := []corev1.VolumeMount{}
	for _, volumeMount := range volumeMounts {
		if volumeMount.MountPath != newVolumeMount.MountPath && volumeMount.Name != newVolumeMount.Name {
			updated = append(updated, volumeMount)
		}
	}
	updated = append(updated, newVolumeMount)
	return updated
}

func removeConflictingEnvVarsAndAppend(envVars []corev1.EnvVar, newEnvVar corev1.EnvVar) []corev1.EnvVar {
	updated := []corev1.EnvVar{}
	for _, envVar := range envVars {
		if envVar.Name != newEnvVar.Name {
			updated = append(updated, envVar)
		}
	}
	updated = append(updated, newEnvVar)
	return updated
}

func unmarshalIfPresent(obj map[string]json.RawMessage, key string, out any) error {
	raw, ok := obj[key]
	if !ok || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("invalid %s JSON: %w", key, err)
	}
	return nil
}

// Configure adds a managed ServiceAccount object to the given CP cluster and a managed Secret object to the given workload cluster.
func (m *ManagedServiceAccount) Configure(workloadCluster, cpCluster resources.ManagedCluster) {
	// Add a namespace on the CP cluster so the service account has a place to live.
	ns := resources.NewManagedObject(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.Namespace,
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: resources.NoOp,
		StatusFunc:    resources.SimpleStatus,
	})
	cpCluster.AddObject(ns)

	// Add a service account on the CP cluster.
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
	}
	msa := resources.NewManagedObject(sa, resources.ManagedObjectContext{
		DependsOn:     []resources.ManagedObject{ns},
		ReconcileFunc: resources.NoOp,
		StatusFunc:    resources.SimpleStatus,
	})
	cpCluster.AddObject(msa)

	// Add a secret on the local cluster that contains a token for the remote service account.
	wcNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: workloadCluster.GetDefaultNamespace(),
		},
	}
	wcns := resources.NewManagedObject(wcNamespace, resources.ManagedObjectContext{
		ReconcileFunc: resources.NoOp,
		StatusFunc:    resources.SimpleStatus,
	})
	workloadCluster.AddObject(wcns)

	secret := resources.NewManagedObject(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.KubeAPIAccess(),
			Namespace: workloadCluster.GetDefaultNamespace(),
		},
	}, resources.ManagedObjectContext{
		DependsOn: []resources.ManagedObject{
			msa,
		},
		// Mint the first token here so the Secret is never applied empty and crashloop until the tokenrefresh controller catches up.
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			tokenSecret := o.(*corev1.Secret)
			// if the token is already present for this ServiceAccount, don't overwrite it, the tokenrefresh controller will handle it.
			// A token minted for a ServiceAccount in another namespace (e.g. after a namespaceOverride change) is replaced right away,
			// because the ClusterRoleBinding no longer grants it any permissions.
			if len(tokenSecret.Data[tokenrefresh.SecretKeyToken]) > 0 &&
				string(tokenSecret.Data[tokenrefresh.SecretKeyNamespace]) == m.Namespace {
				return nil
			}
			_, err := tokenrefresh.EnsureToken(ctx, cpCluster.GetCluster(), tokenSecret,
				m.TokenBinding(workloadCluster.GetDefaultNamespace()), TokenServiceProvider, tokenrefresh.DefaultTokenLifetime)
			return err
		},
		StatusFunc: resources.SimpleStatus,
	})
	workloadCluster.AddObject(secret)
}
