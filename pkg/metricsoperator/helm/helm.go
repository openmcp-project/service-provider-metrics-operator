package helm

import (
	"encoding/json"
	"fmt"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	serviceAccountVolume    = "kube-api-access"
)

// HelmValues define the helm values that are explicitly processed during reconciliation
type HelmValues struct {
	NamespaceOverride string `json:"namespaceOverride,omitempty"`
	Global            Global `json:"global,omitempty"`
}

// Global define the global settings that are explicitly process during reconciliation
type Global struct {
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// ExtractHelmValues extract helm values required for processing
func ExtractHelmValues(values *apiextensionsv1.JSON) (*HelmValues, error) {
	if values == nil || len(values.Raw) == 0 {
		return &HelmValues{}, nil
	}

	vals := &HelmValues{}
	if err := json.Unmarshal(values.Raw, vals); err != nil {
		return nil, err
	}

	return vals, nil
}

func AddDefaultHelmValues(values *apiextensionsv1.JSON, mcpNamespace string) (*apiextensionsv1.JSON, error) {
	var root = map[string]json.RawMessage{}
	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &root); err != nil {
			return nil, fmt.Errorf("failed to unmarshal helm values: %w", err)
		}
		if root == nil {
			root = make(map[string]json.RawMessage)
		}
	}

	configNamespaceRaw, err := json.Marshal(mcpNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal configNamespace: %w", err)
	}
	root["configNamespace"] = configNamespaceRaw

	saRaw, err := json.Marshal(map[string]any{
		"create":    false,
		"automount": false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal serviceAccount: %w", err)
	}
	root["serviceAccount"] = saRaw

	webhooksRaw, err := json.Marshal(map[string]any{
		"manage":  false,
		"service": map[string]any{"enabled": false},
		"listen":  false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal webhooks: %w", err)
	}
	root["webhooks"] = webhooksRaw

	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal helm values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: out}, nil
}

// AddAuthToHelmValues
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
