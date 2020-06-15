// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package beat

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"

	beatv1beta1 "github.com/elastic/cloud-on-k8s/pkg/apis/beat/v1beta1"
	commonv1 "github.com/elastic/cloud-on-k8s/pkg/apis/common/v1"
	beatcommon "github.com/elastic/cloud-on-k8s/pkg/controller/beat/common"
	"github.com/elastic/cloud-on-k8s/pkg/controller/beat/metricbeat"
	"github.com/elastic/cloud-on-k8s/pkg/controller/common/version"
	"github.com/elastic/cloud-on-k8s/pkg/controller/elasticsearch/client"
	"github.com/elastic/cloud-on-k8s/test/e2e/cmd/run"
	"github.com/elastic/cloud-on-k8s/test/e2e/test"
)

const (
	PSPClusterRoleName          = "elastic-beat-restricted"
	AutodiscoverClusterRoleName = "elastic-beat-autodiscover"
	MetricbeatClusterRoleName   = "elastic-beat-metricbeat"
)

// Builder to create a Beat
type Builder struct {
	Beat        beatv1beta1.Beat
	Validations []ValidationFunc
	RBACObjects []runtime.Object

	// PodTemplate points to the PodTemplate in spec.DaemonSet or spec.Deployment
	PodTemplate *corev1.PodTemplateSpec
}

func (b Builder) SkipTest() bool {
	ver := version.MustParse(b.Beat.Spec.Version)
	return version.SupportedBeatVersions.WithinRange(ver) != nil

}

func NewBuilderWithoutSuffix(name string) Builder {
	return newBuilder(name, "")
}

func NewBuilder(name string) Builder {
	return newBuilder(name, rand.String(4))
}

func newBuilder(name string, suffix string) Builder {
	meta := metav1.ObjectMeta{
		Name:      name,
		Namespace: test.Ctx().ManagedNamespace(0),
		Labels:    map[string]string{run.TestNameLabel: name},
	}

	return Builder{
		Beat: beatv1beta1.Beat{
			ObjectMeta: meta,
			Spec: beatv1beta1.BeatSpec{
				Version: test.Ctx().ElasticStackVersion,
			},
		},
	}.
		WithSuffix(suffix).
		WithLabel(run.TestNameLabel, name).
		WithDaemonSet().
		WithRBAC().
		WithPSP()
}

type ValidationFunc func(client.Client) error

func (b Builder) WithType(typ beatcommon.Type) Builder {
	b.Beat.Spec.Type = string(typ)
	return b
}

func (b Builder) WithDaemonSet() Builder {
	b.Beat.Spec.DaemonSet = &beatv1beta1.DaemonSetSpec{}

	// if it exists, move PodTemplate from Deployment to DaemonSet
	if b.Beat.Spec.Deployment != nil {
		b.Beat.Spec.DaemonSet.PodTemplate = b.Beat.Spec.Deployment.PodTemplate
		b.Beat.Spec.Deployment = nil
	}

	b.PodTemplate = &b.Beat.Spec.DaemonSet.PodTemplate

	return b
}

func (b Builder) WithDeployment() Builder {
	b.Beat.Spec.Deployment = &beatv1beta1.DeploymentSpec{}

	// if it exists, move PodTemplate from DaemonSet to Deployment
	if b.Beat.Spec.DaemonSet != nil {
		b.Beat.Spec.Deployment.PodTemplate = b.Beat.Spec.DaemonSet.PodTemplate
		b.Beat.Spec.DaemonSet = nil
	}
	b.PodTemplate = &b.Beat.Spec.Deployment.PodTemplate

	return b
}

func (b Builder) WithESValidations(validations ...ValidationFunc) Builder {
	b.Validations = append(b.Validations, validations...)

	return b
}

func (b Builder) WithElasticsearchRef(ref commonv1.ObjectSelector) Builder {
	b.Beat.Spec.ElasticsearchRef = ref
	return b
}

func (b Builder) WithConfig(config *commonv1.Config) Builder {
	b.Beat.Spec.Config = config
	return b
}

func (b Builder) WithImage(image string) Builder {
	b.Beat.Spec.Image = image
	return b
}

func (b Builder) WithSuffix(suffix string) Builder {
	if suffix != "" {
		b.Beat.ObjectMeta.Name = b.Beat.ObjectMeta.Name + "-" + suffix
	}
	return b
}

func (b Builder) WithNamespace(namespace string) Builder {
	b.Beat.ObjectMeta.Namespace = namespace
	return b
}

func (b Builder) WithRestrictedSecurityContext() Builder {
	b.PodTemplate.Spec.SecurityContext = test.DefaultSecurityContext()

	return b
}

func (b Builder) WithContainerSecurityContext(securityContext corev1.SecurityContext) Builder {
	for i := range b.PodTemplate.Spec.Containers {
		b.PodTemplate.Spec.Containers[i].SecurityContext = &securityContext
	}

	return b
}

func (b Builder) WithLabel(key, value string) Builder {
	if b.Beat.Labels == nil {
		b.Beat.Labels = make(map[string]string)
	}
	b.Beat.Labels[key] = value

	return b
}

func (b Builder) WithPodLabel(key, value string) Builder {
	if b.PodTemplate.Labels == nil {
		b.PodTemplate.Labels = make(map[string]string)
	}
	b.PodTemplate.Labels[key] = value

	return b
}

func (b Builder) WithPodTemplateServiceAccount(name string) Builder {
	b.PodTemplate.Spec.ServiceAccountName = name

	return b
}

func (b Builder) WithRBAC() Builder {
	clusterRoleName := AutodiscoverClusterRoleName
	if b.Beat.Spec.Type == string(metricbeat.Type) {
		clusterRoleName = MetricbeatClusterRoleName
	}
	return bind(b, clusterRoleName)
}

func (b Builder) WithPSP() Builder {
	return bind(b, PSPClusterRoleName)
}

func bind(b Builder, clusterRoleName string) Builder {
	saName := fmt.Sprintf("%s-sa", b.Beat.Name)

	if b.PodTemplate.Spec.ServiceAccountName != saName {
		b = b.WithPodTemplateServiceAccount(saName)
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: b.Beat.Namespace,
			},
		}
		b.RBACObjects = append(b.RBACObjects, sa)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%s-%s-binding", clusterRoleName, b.Beat.Namespace, b.Beat.Name),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: b.Beat.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
	}

	b.RBACObjects = append(b.RBACObjects, crb)

	return b
}

func (b Builder) RuntimeObjects() []runtime.Object {
	return append(b.RBACObjects, &b.Beat)
}

var _ test.Builder = Builder{}
