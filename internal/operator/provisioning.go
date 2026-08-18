package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hkjang/AgentHub/internal/runtimeenv"
)

// provisionedVolume is the volume name the generated files are mounted from.
const provisionedVolume = "provisioned"

// provisionedConfigMapName keeps the provisioned files in their own ConfigMap
// rather than the platform's: the platform's is mounted whole at /etc/agenthub,
// and an administrator's pip.conf has no business appearing there.
func provisionedConfigMapName(runtimeName string) string { return runtimeName + "-files" }

// provisionedFiles and provisionedEnv are what actually reaches the Pod. The
// control plane already filtered this set, so anything dropped here arrived by
// editing the AgentRuntime object directly.
func provisionedFiles(value spec) []runtimeenv.File {
	files, _ := runtimeenv.Settings{Files: value.Provisioning.Files}.Effective()
	return files
}

func provisionedEnv(value spec) []corev1.EnvVar {
	_, variables := runtimeenv.Settings{Variables: value.Provisioning.Env}.Effective()
	env := make([]corev1.EnvVar, 0, len(variables))
	for _, variable := range variables {
		env = append(env, corev1.EnvVar{Name: variable.Name, Value: variable.Value})
	}
	return env
}

// provisionedData renders the ConfigMap payload, keyed so two files can never
// collide on a key the way their file names can.
func provisionedData(files []runtimeenv.File) map[string]string {
	data := make(map[string]string, len(files))
	for _, file := range files {
		data[runtimeenv.ConfigKey(file.Path)] = file.Content
	}
	return data
}

// ensureProvisionedConfigMap creates, updates or removes the ConfigMap carrying
// the administrator's files. Removing it matters: emptying the setting has to
// take the files away, and an owner reference only cleans up on delete.
func (c *Controller) ensureProvisionedConfigMap(ctx context.Context, ns, name string, value spec, owner *unstructured.Unstructured) error {
	target := provisionedConfigMapName(name)
	files := provisionedFiles(value)
	if len(files) == 0 {
		err := c.client.CoreV1().ConfigMaps(ns).Delete(ctx, target, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: target, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Data: provisionedData(files)}
	existing, err := c.client.CoreV1().ConfigMaps(ns).Get(ctx, target, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.CoreV1().ConfigMaps(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	_, err = c.client.CoreV1().ConfigMaps(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

// applyProvisioning gives every container in the Pod the same files and the same
// variables. Uniformity is the point: an init container that installs packages
// needs /etc/pip.conf as much as the agent does, and a proxy that has to reach a
// mirror needs the same HTTPS_PROXY.
//
// Variables are prepended so that anything the platform sets later in the list
// wins on a name collision, and the mount is read-only so an agent cannot edit
// what an administrator declared.
func applyProvisioning(pod *corev1.PodSpec, runtimeName string, value spec) {
	env := provisionedEnv(value)
	files := provisionedFiles(value)
	if len(env) == 0 && len(files) == 0 {
		return
	}
	mounts := make([]corev1.VolumeMount, 0, len(files))
	items := make([]corev1.KeyToPath, 0, len(files))
	for _, file := range files {
		key := runtimeenv.ConfigKey(file.Path)
		mode, err := file.FileMode()
		if err != nil {
			continue
		}
		items = append(items, corev1.KeyToPath{Key: key, Path: key, Mode: ptr(mode)})
		mounts = append(mounts, corev1.VolumeMount{Name: provisionedVolume, MountPath: file.Path, SubPath: key, ReadOnly: true})
	}
	apply := func(containers []corev1.Container) {
		for i := range containers {
			if len(env) > 0 {
				containers[i].Env = append(append([]corev1.EnvVar{}, env...), containers[i].Env...)
			}
			containers[i].VolumeMounts = append(containers[i].VolumeMounts, mounts...)
		}
	}
	apply(pod.InitContainers)
	apply(pod.Containers)
	if len(items) == 0 {
		return
	}
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: provisionedVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: provisionedConfigMapName(runtimeName)}, Items: items}}})
}

// provisioningHash fingerprints the provisioned environment so the Pod template
// changes when an administrator edits a file. Without it the ConfigMap would be
// updated under a running Pod that mounts it through subPath — which never sees
// the new content.
func provisioningHash(value spec) string {
	files := provisionedFiles(value)
	env := provisionedEnv(value)
	parts := make([]string, 0, len(files)+len(env))
	for _, file := range files {
		parts = append(parts, "file\x00"+file.Path+"\x00"+file.Mode+"\x00"+file.Content)
	}
	for _, variable := range env {
		parts = append(parts, "env\x00"+variable.Name+"\x00"+variable.Value)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}
