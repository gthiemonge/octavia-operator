/*

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

package octavia

import (
	"fmt"

	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	octaviav1 "github.com/openstack-k8s-operators/octavia-operator/api/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ImageUploadDetails struct {
	ContainerImage string
	VolumeMounts   []corev1.VolumeMount
}

const (
	// ServiceCommand -
	ServiceCommand = "/usr/local/bin/container-scripts/image_upload_run.sh"
)

func getVolumes(name string) []corev1.Volume {
	var scriptsVolumeDefaultMode int32 = 0755
	var config0640AccessMode int32 = 0644

	return []corev1.Volume{
		{
			Name: "amphora-image",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "scripts",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					DefaultMode: &scriptsVolumeDefaultMode,
					SecretName:  name + "-scripts",
				},
			},
		},
		{
			Name: "config-data",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					DefaultMode: &config0640AccessMode,
					SecretName:  name + "-config-data",
				},
			},
		},
		{
			Name: "config-data-merged",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{Medium: ""},
			},
		},
	}
}

func getInitVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      "amphora-image",
			MountPath: "/www",
		},
		{
			Name:      "scripts",
			MountPath: "/usr/local/bin/container-scripts",
			ReadOnly:  true,
		},
		{
			Name:      "config-data-merged",
			MountPath: "/var/lib/config-data/merged",
			ReadOnly:  false,
		},
	}
}

// GetVolumeMounts - general VolumeMounts
func getVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      "amphora-image",
			MountPath: "/www",
			ReadOnly:  true,
		},
		{
			Name:      "scripts",
			MountPath: "/usr/local/bin/container-scripts",
			ReadOnly:  true,
		},
		{
			Name:      "config-data",
			MountPath: "/var/lib/config-data/default",
			ReadOnly:  true,
		},
		{
			Name:      "config-data-merged",
			MountPath: "/var/lib/config-data/merged",
			ReadOnly:  true,
		},
	}
}

// Deployment func
func ImageUploadDeployment(
	instance *octaviav1.Octavia,
	labels map[string]string,
) *appsv1.Deployment {
	args := []string{"-c", ServiceCommand}

	serviceName := fmt.Sprintf("%s-image-upload", ServiceName)

	// create Volume and VolumeMounts
	volumes := getVolumes(instance.Name)
	volumeMounts := getVolumeMounts()
	initVolumeMounts := getInitVolumeMounts()

	// add CA cert if defined
	if instance.Spec.OctaviaAPI.TLS.CaBundleSecretName != "" {
		volumes = append(volumes, instance.Spec.OctaviaAPI.TLS.CreateVolume())
		volumeMounts = append(volumeMounts, instance.Spec.OctaviaAPI.TLS.CreateVolumeMounts(nil)...)
		initVolumeMounts = append(initVolumeMounts, instance.Spec.OctaviaAPI.TLS.CreateVolumeMounts(nil)...)
	}

	if instance.Spec.OctaviaAPI.TLS.API.Enabled(service.EndpointInternal) {
		tlsEndptCfg := instance.Spec.OctaviaAPI.TLS.API.Internal

		svc, err := tlsEndptCfg.ToService()
		if err != nil {
			return nil //, err
		}
		volumes = append(volumes, svc.CreateVolume(string(service.EndpointInternal)))
		volumeMounts = append(volumeMounts, svc.CreateVolumeMounts(string(service.EndpointInternal))...)
		initVolumeMounts = append(initVolumeMounts, svc.CreateVolumeMounts(string(service.EndpointInternal))...)
	}

	depl := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: instance.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: instance.RbacResourceName(),
					Containers: []corev1.Container{
						{
							Name: "octavia-amphora-httpd",
							Command: []string{
								"/bin/bash",
							},
							Args:         args,
							Image:        instance.Spec.ApacheContainerImage,
							VolumeMounts: volumeMounts,
							Resources:    instance.Spec.Resources,
							// TODO(gthiemonge) do we need probes?
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	initContainerDetails := ImageUploadDetails{
		ContainerImage: instance.Spec.AmphoraImageContainerImage,
		VolumeMounts:   initVolumeMounts,
	}
	depl.Spec.Template.Spec.InitContainers = initContainer(instance, initContainerDetails)

	return depl
}

func initContainer(
	instance *octaviav1.Octavia,
	init ImageUploadDetails,
) []corev1.Container {
	runAsUser := int64(0)
	envs := []corev1.EnvVar{
		{
			Name:  "DEST_DIR",
			Value: "/www",
		},
	}

	return []corev1.Container{
		{
			Name: "init",
			Command: []string{
				"/bin/bash",
			},
			Args: []string{
				"-c",
				"/usr/local/bin/container-scripts/image_upload_init.sh",
			},
			Image: instance.Spec.ApacheContainerImage,
			SecurityContext: &corev1.SecurityContext{
				RunAsUser: &runAsUser,
			},
			Env:          envs,
			VolumeMounts: init.VolumeMounts,
		},
		{
			Name:  "init-image",
			Image: init.ContainerImage,
			SecurityContext: &corev1.SecurityContext{
				RunAsUser: &runAsUser,
			},
			Env:          envs,
			VolumeMounts: init.VolumeMounts,
		},
	}
}
