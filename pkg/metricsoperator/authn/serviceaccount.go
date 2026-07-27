package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	serviceAccountVolume    = "kube-api-access"

	annotationTokenExpirationTime = "velero.services.openmcp.cloud/token-expiration-time"
)

var (
	errSANameOrNamespaceEmpty = errors.New("name or namespace in service account reference must not be empty")
	errRestConfigNil          = errors.New("rest config must not be nil")
	errExpirationInvalid      = errors.New("must not specify a duration less than 10 minutes")
)

type serviceAccountToken struct {
	CAData      []byte
	Token       string
	TokenExpiry time.Time
}

// TokenApplyFunc injects the token to any container of the given PodSpec.
type TokenApplyFunc func(ps *corev1.PodSpec)

// generateToken generates a token for the given ServiceAccount. If successful, it returns the token and the actual lifetime of it, which might deviate from the desired lifetime.
func generateToken(ctx context.Context, mcp *clusters.Cluster, cfg *rest.Config, svcAccRef types.NamespacedName, expiration time.Duration) (*serviceAccountToken, error) {
	if svcAccRef.Name == "" || svcAccRef.Namespace == "" {
		return nil, errSANameOrNamespaceEmpty
	}
	if cfg == nil {
		return nil, errRestConfigNil
	}
	if expiration < 10*time.Minute {
		return nil, errExpirationInvalid
	}

	sa := &corev1.ServiceAccount{}
	if err := mcp.Client().Get(ctx, types.NamespacedName{Name: svcAccRef.Name, Namespace: svcAccRef.Namespace}, sa); err != nil {
		return nil, err
	}

	req := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: ptr.To(int64(expiration.Seconds())),
		},
	}
	if err := mcp.Client().SubResource("token").Create(ctx, sa, req); err != nil {
		return nil, err
	}

	rc := &serviceAccountToken{
		Token:       req.Status.Token,
		TokenExpiry: req.Status.ExpirationTimestamp.Time,
		CAData:      cfg.CAData,
	}

	if cfg.CAFile != "" {
		caBytes, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		rc.CAData = caBytes
	}

	return rc, nil
}

// ManagedServiceAccount references the managed ServiceAccount object
type ManagedServiceAccount struct {
	types.NamespacedName
}

func (m *ManagedServiceAccount) KubeAPIAccess() string {
	return fmt.Sprintf("kube-api-access-%s", m.Name)
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

// Configure adds a managed ServiceAccount object to the given MCP cluster and a managed Secret object to the given workload cluster.
func (m *ManagedServiceAccount) Configure(workloadCluster, mcpCluster resources.ManagedCluster, values *apiextensionsv1.JSON, pollInterval time.Duration) {
	// Add namespace on the remote cluster.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.Namespace,
		},
	}
	nsa := resources.NewManagedObject(ns, resources.ManagedObjectContext{
		ReconcileFunc: resources.NoOp,
		StatusFunc:    resources.SimpleStatus,
	})
	mcpCluster.AddObject(nsa)

	// Add a service account on the remote cluster.
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
	}
	msa := resources.NewManagedObject(sa, resources.ManagedObjectContext{
		ReconcileFunc: resources.NoOp,
		StatusFunc:    resources.SimpleStatus,
	})
	mcpCluster.AddObject(msa)

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
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oSecret := o.(*corev1.Secret)

			// prevent token generation on every reconcile but at the same time consider the
			// provider config poll interval and an additional safeguard of a minute (pod volume refresh delay)
			// to make sure that the token is always refreshed in time.
			// e.g. a user might not the update velero onboarding resource after its initial creation but
			// the service still needs to refresh its token at least once per hour or more frequently
			// in case the token is issued with an expiration time of less than an hour
			nextReconcile := time.Now().Add(pollInterval).Add(time.Minute)
			expirationTime, err := getTokenExpirationTime(oSecret)
			if err != nil || expirationTime.Before(nextReconcile) {
				rc, err := generateToken(ctx, mcpCluster.GetCluster(), mcpCluster.GetConfig(), m.NamespacedName, 1*time.Hour)
				if err != nil {
					return err
				}
				oSecret.Data = map[string][]byte{
					"token":     []byte(rc.Token),
					"namespace": []byte(mcpCluster.GetDefaultNamespace()),
					"ca.crt":    rc.CAData,
				}
				setTokenExpirationTime(oSecret, rc.TokenExpiry)
			}

			return nil
		},
		StatusFunc: resources.SimpleStatus,
	})
	workloadCluster.AddObject(secret)
}

func addOrReplaceVolume(ps *corev1.PodSpec, vol corev1.Volume) {
	for i := range ps.Volumes {
		if ps.Volumes[i].Name == vol.Name {
			ps.Volumes[i] = vol
			return
		}
	}

	ps.Volumes = append(ps.Volumes, vol)
}

func addOrReplaceEnv(c *corev1.Container, env corev1.EnvVar) {
	for i := range c.Env {
		if c.Env[i].Name == env.Name {
			c.Env[i] = env
			return
		}
	}

	c.Env = append(c.Env, env)
}

func addOrReplaceVolumeMount(c *corev1.Container, vm corev1.VolumeMount) {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == vm.Name {
			c.VolumeMounts[i] = vm
			return
		}
	}

	c.VolumeMounts = append(c.VolumeMounts, vm)
}

func applyToContainer(c *corev1.Container, remoteCluster resources.ManagedCluster) {
	remoteHost, remotePort := remoteCluster.GetHostAndPort()

	addOrReplaceVolumeMount(c, corev1.VolumeMount{
		Name:      serviceAccountVolume,
		MountPath: serviceAccountMountPath,
		ReadOnly:  true,
	})
	addOrReplaceEnv(c, corev1.EnvVar{
		Name:  "KUBERNETES_SERVICE_HOST",
		Value: remoteHost,
	})
	addOrReplaceEnv(c, corev1.EnvVar{
		Name:  "KUBERNETES_SERVICE_PORT",
		Value: remotePort,
	})
}

func getTokenExpirationTime(obj *corev1.Secret) (time.Time, error) {
	if obj.Annotations == nil {
		return time.Time{}, errors.New("no expiration time set")
	}
	expirationTime := obj.Annotations[annotationTokenExpirationTime]
	return time.Parse(time.RFC3339, expirationTime)
}

func setTokenExpirationTime(obj *corev1.Secret, expTime time.Time) {
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations[annotationTokenExpirationTime] = expTime.Format(time.RFC3339)
}
