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

// AddDefaultHelmValues sets helm values that are required for the CP deployment
func AddDefaultHelmValues(values *apiextensionsv1.JSON, cpNamespace string) (*apiextensionsv1.JSON, error) {
	root, err := unmarshalRoot(values)
	if err != nil {
		return nil, err
	}

	root["manager"], err = patchEnvVars(root["manager"], corev1.EnvVar{Name: "OPERATOR_CONFIG_NAMESPACE", Value: cpNamespace})
	if err != nil {
		return nil, fmt.Errorf("manager: %w", err)
	}
	if root["serviceAccount"], err = json.Marshal(map[string]any{
		"create":    false,
		"automount": false,
	}); err != nil {
		return nil, err
	}

	out, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return &apiextensionsv1.JSON{Raw: out}, nil
}

// AddAuthToHelmValues injects a kube-api-access volume (from the SA token Secret) and
// KUBERNETES_SERVICE_HOST/PORT env vars into the init and manager containers so the
// metrics-operator --install-crds init container connects to the CP cluster.
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
	hostEnv := corev1.EnvVar{Name: "KUBERNETES_SERVICE_HOST", Value: remoteHost}
	portEnv := corev1.EnvVar{Name: "KUBERNETES_SERVICE_PORT", Value: remotePort}

	root, err := unmarshalRoot(values)
	if err != nil {
		return nil, err
	}

	// extraVolumes (top-level)
	var extraVolumes []corev1.Volume
	if err := unmarshalKey(root, "extraVolumes", &extraVolumes); err != nil {
		return nil, fmt.Errorf("extraVolumes: %w", err)
	}
	extraVolumes = upsertVolume(extraVolumes, authVolume)
	if root["extraVolumes"], err = json.Marshal(extraVolumes); err != nil {
		return nil, err
	}

	// init container overrides
	root["init"], err = patchContainerSection(root["init"], authVolumeMount, hostEnv, portEnv)
	if err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}

	// manager container overrides
	root["manager"], err = patchContainerSection(root["manager"], authVolumeMount, hostEnv, portEnv)
	if err != nil {
		return nil, fmt.Errorf("manager: %w", err)
	}

	out, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return &apiextensionsv1.JSON{Raw: out}, nil
}

func unmarshalRoot(values *apiextensionsv1.JSON) (map[string]json.RawMessage, error) {
	root := map[string]json.RawMessage{}
	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &root); err != nil {
			return nil, fmt.Errorf("failed to unmarshal helm values: %w", err)
		}
	}
	return root, nil
}

func unmarshalKey(root map[string]json.RawMessage, key string, out any) error {
	raw, ok := root[key]
	if !ok || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func patchContainerSection(raw json.RawMessage, mount corev1.VolumeMount, envVars ...corev1.EnvVar) (json.RawMessage, error) {
	section := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &section); err != nil {
			return nil, err
		}
	}

	var mounts []corev1.VolumeMount
	if err := unmarshalKey(section, "extraVolumeMounts", &mounts); err != nil {
		return nil, err
	}
	mounts = upsertVolumeMount(mounts, mount)

	var envs []corev1.EnvVar
	if err := unmarshalKey(section, "extraEnv", &envs); err != nil {
		return nil, err
	}
	for _, e := range envVars {
		envs = upsertEnvVar(envs, e)
	}

	var err error
	if section["extraVolumeMounts"], err = json.Marshal(mounts); err != nil {
		return nil, err
	}
	if section["extraEnv"], err = json.Marshal(envs); err != nil {
		return nil, err
	}
	return json.Marshal(section)
}

func upsertVolume(list []corev1.Volume, v corev1.Volume) []corev1.Volume {
	out := make([]corev1.Volume, 0, len(list)+1)
	for _, item := range list {
		if item.Name != v.Name {
			out = append(out, item)
		}
	}
	return append(out, v)
}

func upsertVolumeMount(list []corev1.VolumeMount, vm corev1.VolumeMount) []corev1.VolumeMount {
	out := make([]corev1.VolumeMount, 0, len(list)+1)
	for _, item := range list {
		if item.Name != vm.Name && item.MountPath != vm.MountPath {
			out = append(out, item)
		}
	}
	return append(out, vm)
}

func upsertEnvVar(list []corev1.EnvVar, e corev1.EnvVar) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(list)+1)
	for _, item := range list {
		if item.Name != e.Name {
			out = append(out, item)
		}
	}
	return append(out, e)
}

func patchEnvVars(raw json.RawMessage, envVars ...corev1.EnvVar) (json.RawMessage, error) {
	section := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &section); err != nil {
			return nil, err
		}
	}
	var envs []corev1.EnvVar
	if err := unmarshalKey(section, "extraEnv", &envs); err != nil {
		return nil, err
	}
	for _, e := range envVars {
		envs = upsertEnvVar(envs, e)
	}
	var err error
	if section["extraEnv"], err = json.Marshal(envs); err != nil {
		return nil, err
	}
	return json.Marshal(section)
}
