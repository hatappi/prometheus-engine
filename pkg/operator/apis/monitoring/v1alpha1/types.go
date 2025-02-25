// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/prometheus/common/config"
	prommodel "github.com/prometheus/common/model"
	promconfig "github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/discovery"
	discoverykube "github.com/prometheus/prometheus/discovery/kubernetes"
	"github.com/prometheus/prometheus/pkg/relabel"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/GoogleCloudPlatform/prometheus-engine/pkg/export"
)

// OperatorConfig defines configuration of the gmp-operator.
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type OperatorConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Rules specifies how the operator configures and deployes rule-evaluator.
	Rules RuleEvaluatorSpec `json:"rules,omitempty"`
	// Collection specifies how the operator configures collection.
	Collection CollectionSpec `json:"collection,omitempty"`
}

// OperatorConfigList is a list of OperatorConfigs.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type OperatorConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OperatorConfig `json:"items"`
}

// RuleEvaluatorSpec defines configuration for deploying rule-evaluator.
type RuleEvaluatorSpec struct {
	// ExternalLabels specifies external labels that are attached to any rule
	// results and alerts produced by rules. The precedence behavior matches that
	// of Prometheus.
	ExternalLabels map[string]string `json:"externalLabels,omitempty"`
	// QueryProjectID is the GCP project ID to evaluate rules against.
	// If left blank, the rule-evaluator will try attempt to infer the Project ID
	// from the environment.
	QueryProjectID string `json:"queryProjectID,omitempty"`
	// Alerting contains how the rule-evaluator configures alerting.
	Alerting AlertingSpec `json:"alerting,omitempty"`
	// A reference to GCP service account credentials with which the rule
	// evaluator container is run. It needs to have metric read permissions
	// against queryProjectId and metric write permissions against all projects
	// to which rule results are written.
	// Within GKE, this can typically be left empty if the compute default
	// service account has the required permissions.
	Credentials *v1.SecretKeySelector `json:"credentials,omitempty"`
}

// CollectionSpec specifies how the operator configures collection of metric data.
type CollectionSpec struct {
	// ExternalLabels specifies external labels that are attached to all scraped
	// data before being written to Cloud Monitoring. The precedence behavior matches that
	// of Prometheus.
	ExternalLabels map[string]string `json:"externalLabels,omitempty"`
	// Filter limits which metric data is sent to Cloud Monitoring.
	Filter ExportFilters `json:"filter,omitempty"`
	// A reference to GCP service account credentials with which Prometheus collectors
	// are run. It needs to have metric write permissions for all project IDs to which
	// data is written.
	// Within GKE, this can typically be left empty if the compute default
	// service account has the required permissions.
	Credentials *v1.SecretKeySelector `json:"credentials,omitempty"`
}

// ExportFilters provides mechanisms to filter the scraped data that's sent to GMP.
type ExportFilters struct {
	// A list Prometheus time series matchers. Every time series must match at least one
	// of the matchers to be exported. This field can be used equivalently to the match[]
	// parameter of the Prometheus federation endpoint to selectively export data.
	//
	// Example: `["{job='prometheus'}", "{__name__=~'job:.*'}"]`
	MatchOneOf []string `json:"matchOneOf,omitempty"`
}

// AlertingSpec defines alerting configuration.
type AlertingSpec struct {
	// Alertmanagers contains endpoint configuration for designated Alertmanagers.
	Alertmanagers []AlertmanagerEndpoints `json:"alertmanagers,omitempty"`
}

// AlertmanagerEndpoints defines a selection of a single Endpoints object
// containing alertmanager IPs to fire alerts against.
type AlertmanagerEndpoints struct {
	// Namespace of Endpoints object.
	Namespace string `json:"namespace"`
	// Name of Endpoints object in Namespace.
	Name string `json:"name"`
	// Port the Alertmanager API is exposed on.
	Port intstr.IntOrString `json:"port"`
	// Scheme to use when firing alerts.
	Scheme string `json:"scheme,omitempty"`
	// Prefix for the HTTP path alerts are pushed to.
	PathPrefix string `json:"pathPrefix,omitempty"`
	// TLS Config to use for alertmanager connection.
	TLS *TLSConfig `json:"tls,omitempty"`
	// Authorization section for this alertmanager endpoint
	Authorization *Authorization `json:"authorization,omitempty"`
	// Version of the Alertmanager API that rule-evaluator uses to send alerts. It
	// can be "v1" or "v2".
	APIVersion string `json:"apiVersion,omitempty"`
	// Timeout is a per-target Alertmanager timeout when pushing alerts.
	Timeout string `json:"timeout,omitempty"`
}

// Authorization specifies a subset of the Authorization struct, that is
// safe for use in Endpoints (no CredentialsFile field).
type Authorization struct {
	// Set the authentication type. Defaults to Bearer, Basic will cause an
	// error
	Type string `json:"type,omitempty"`
	// The secret's key that contains the credentials of the request
	Credentials *v1.SecretKeySelector `json:"credentials,omitempty"`
}

// SafeTLSConfig specifies TLS configuration parameters from Kubernetes resources.
type TLSConfig struct {
	// Struct containing the CA cert to use for the targets.
	CA *SecretOrConfigMap `json:"ca,omitempty"`
	// Struct containing the client cert file for the targets.
	Cert *SecretOrConfigMap `json:"cert,omitempty"`
	// Secret containing the client key file for the targets.
	KeySecret *v1.SecretKeySelector `json:"keySecret,omitempty"`
	// Used to verify the hostname for the targets.
	ServerName string `json:"serverName,omitempty"`
	// Disable target certificate validation.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// SecretOrConfigMap allows to specify data as a Secret or ConfigMap. Fields are mutually exclusive.
// Taking inspiration from prometheus-operator: https://github.com/prometheus-operator/prometheus-operator/blob/2c81b0cf6a5673e08057499a08ddce396b19dda4/Documentation/api.md#secretorconfigmap
type SecretOrConfigMap struct {
	// Secret containing data to use for the targets.
	Secret *v1.SecretKeySelector `json:"secret,omitempty"`
	// ConfigMap containing data to use for the targets.
	ConfigMap *v1.ConfigMapKeySelector `json:"configMap,omitempty"`
}

// PodMonitoring defines monitoring for a set of pods.
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PodMonitoring struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Specification of desired Pod selection for target discovery by
	// Prometheus.
	Spec PodMonitoringSpec `json:"spec"`
	// Most recently observed status of the resource.
	// +optional
	Status PodMonitoringStatus `json:"status"`
}

// PodMonitoringList is a list of PodMonitorings.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PodMonitoringList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodMonitoring `json:"items"`
}

func (pm *PodMonitoring) ValidateCreate() error {
	if len(pm.Spec.Endpoints) == 0 {
		return errors.New("at least one endpoint is required")
	}
	_, err := pm.ScrapeConfigs()
	return err
}

func (pm *PodMonitoring) ValidateUpdate(old runtime.Object) error {
	// Validity does not depend on state changes.
	return pm.ValidateCreate()
}

func (pm *PodMonitoring) ValidateDelete() error {
	// Deletions are always valid.
	return nil
}

func (pm *PodMonitoring) ScrapeConfigs() (res []*promconfig.ScrapeConfig, err error) {
	for i := range pm.Spec.Endpoints {
		c, err := pm.endpontScrapeConfig(i)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid definition for endpoint with index %d", i)
		}
		res = append(res, c)
	}
	return res, nil
}

// Environment variable for the current node that needs to be interpolated in generated
// scrape configurations for a PodMonitoring resource.
const EnvVarNodeName = "NODE_NAME"

func (pm *PodMonitoring) endpontScrapeConfig(index int) (*promconfig.ScrapeConfig, error) {
	// Configure how Prometheus talks to the Kubernetes API server to discover targets.
	// This configuration is the same for all scrape jobs (esp. selectors).
	// This ensures that Prometheus can reuse the underlying client and caches, which reduces
	// load on the Kubernetes API server.
	discoveryCfgs := discovery.Configs{
		&discoverykube.SDConfig{
			HTTPClientConfig: config.DefaultHTTPClientConfig,
			Role:             discoverykube.RolePod,
			// Drop all potential targets not the same node as the collector. The $(NODE_NAME) variable
			// is interpolated by the config reloader sidecar before the config reaches the Prometheus collector.
			// Doing it through selectors rather than relabeling should substantially reduce the client and
			// server side load.
			Selectors: []discoverykube.SelectorConfig{
				{
					Role:  discoverykube.RolePod,
					Field: fmt.Sprintf("spec.nodeName=$(%s)", EnvVarNodeName),
				},
			},
		},
	}

	ep := pm.Spec.Endpoints[index]

	// TODO(freinartz): validate all generated regular expressions.
	relabelCfgs := []*relabel.Config{
		// Filter targets by namespace of the PodMonitoring configuration.
		{
			Action:       relabel.Keep,
			SourceLabels: prommodel.LabelNames{"__meta_kubernetes_namespace"},
			Regex:        relabel.MustNewRegexp(pm.Namespace),
		},
	}

	// Filter targets that belong to selected services.

	// Simple equal matchers. Sort by keys first to ensure that generated configs are reproducible.
	// (Go map iteration is non-deterministic.)
	var selectorKeys []string
	for k := range pm.Spec.Selector.MatchLabels {
		selectorKeys = append(selectorKeys, k)
	}
	sort.Strings(selectorKeys)

	for _, k := range selectorKeys {
		relabelCfgs = append(relabelCfgs, &relabel.Config{
			Action:       relabel.Keep,
			SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_label_" + sanitizeLabelName(k)},
			Regex:        relabel.MustNewRegexp(pm.Spec.Selector.MatchLabels[k]),
		})
	}
	// Expression matchers are mapped to relabeling rules with the same behavior.
	for _, exp := range pm.Spec.Selector.MatchExpressions {
		switch exp.Operator {
		case metav1.LabelSelectorOpIn:
			relabelCfgs = append(relabelCfgs, &relabel.Config{
				Action:       relabel.Keep,
				SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_label_" + sanitizeLabelName(exp.Key)},
				Regex:        relabel.MustNewRegexp(strings.Join(exp.Values, "|")),
			})
		case metav1.LabelSelectorOpNotIn:
			relabelCfgs = append(relabelCfgs, &relabel.Config{
				Action:       relabel.Drop,
				SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_label_" + sanitizeLabelName(exp.Key)},
				Regex:        relabel.MustNewRegexp(strings.Join(exp.Values, "|")),
			})
		case metav1.LabelSelectorOpExists:
			relabelCfgs = append(relabelCfgs, &relabel.Config{
				Action:       relabel.Keep,
				SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_labelpresent_" + sanitizeLabelName(exp.Key)},
				Regex:        relabel.MustNewRegexp("true"),
			})
		case metav1.LabelSelectorOpDoesNotExist:
			relabelCfgs = append(relabelCfgs, &relabel.Config{
				Action:       relabel.Drop,
				SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_labelpresent_" + sanitizeLabelName(exp.Key)},
				Regex:        relabel.MustNewRegexp("true"),
			})
		}
	}

	// Set a clean namespace, job, and instance label that provide sufficient uniqueness.
	relabelCfgs = append(relabelCfgs, &relabel.Config{
		Action:       relabel.Replace,
		SourceLabels: prommodel.LabelNames{"__meta_kubernetes_namespace"},
		TargetLabel:  "namespace",
	})
	relabelCfgs = append(relabelCfgs, &relabel.Config{
		Action:      relabel.Replace,
		Replacement: pm.Name,
		TargetLabel: "job",
	})

	// Filter targets by the configured port.
	//
	// If a port number is configured we don't filter by the __meta_kubernetes_pod_container_port_number
	// label but instead hardcode it into the config and rely on Prometheus' target deduplication to
	// ensure only one final target is produced.
	// We do this because a user may not be able to ensure that all Pods sufficiently declare ports
	// but should still be able to enforce a specific port being scraped.
	//
	// This approach works in our case for two reasons:
	//   1. if a Pod specifies no ports at all as Prometheus will still generate a single substitute target
	//   2. we generate a scrape config for each specified endpoint and can thus safely force all candidates
	//      to have a static port number without preventing other ports from being scraped.
	if ep.Port.StrVal != "" {
		portValue, err := relabel.NewRegexp(ep.Port.StrVal)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid port name %q", ep.Port)
		}
		relabelCfgs = append(relabelCfgs, &relabel.Config{
			Action:       relabel.Keep,
			SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_container_port_name"},
			Regex:        portValue,
		})
		// The instance label being the pod name would be ideal UX-wise. But we cannot be certain
		// that multiple metrics endpoints on a pod don't expose metrics with the same name. Thus
		// we have to disambiguate along the port as well.
		relabelCfgs = append(relabelCfgs, &relabel.Config{
			Action:       relabel.Replace,
			SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_name", "__meta_kubernetes_pod_container_port_name"},
			Regex:        relabel.MustNewRegexp("(.+);(.+)"),
			Replacement:  "$1:$2",
			TargetLabel:  "instance",
		})
	} else if ep.Port.IntVal != 0 {
		relabelCfgs = append(relabelCfgs, &relabel.Config{
			Action:       relabel.Replace,
			SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_name"},
			Replacement:  fmt.Sprintf("$1:%d", ep.Port.IntVal),
			TargetLabel:  "instance",
		})
		// If the input target was produced by a port declared in the Pod, this port number
		// is pre-filled in the __address__ label that's actually used for scraping. Thus
		// we need to explicitly override it.
		relabelCfgs = append(relabelCfgs, &relabel.Config{
			Action:       relabel.Replace,
			SourceLabels: prommodel.LabelNames{"__meta_kubernetes_pod_ip"},
			Replacement:  fmt.Sprintf("$1:%d", ep.Port.IntVal),
			TargetLabel:  "__address__",
		})
	} else {
		return nil, errors.New("port must be set for PodMonitoring")
	}

	// Incorporate k8s label remappings from CRD.
	if pCfgs, err := labelMappingRelabelConfigs(pm.Spec.TargetLabels.FromPod, "__meta_kubernetes_pod_label_"); err != nil {
		return nil, errors.Wrap(err, "invalid PodMonitoring target labels")
	} else {
		relabelCfgs = append(relabelCfgs, pCfgs...)
	}

	interval, err := prommodel.ParseDuration(ep.Interval)
	if err != nil {
		return nil, errors.Wrap(err, "invalid scrape interval")
	}
	timeout := interval
	if ep.Timeout != "" {
		timeout, err = prommodel.ParseDuration(ep.Timeout)
		if err != nil {
			return nil, errors.Wrap(err, "invalid scrape timeout")
		}
		if timeout > interval {
			return nil, errors.Errorf("scrape timeout %v must not be greater than scrape interval %v", timeout, interval)
		}
	}

	metricsPath := "/metrics"
	if ep.Path != "" {
		metricsPath = ep.Path
	}

	return &promconfig.ScrapeConfig{
		// Generate a job name to make it easy to track what generated the scrape configuration.
		// The actual job label attached to its metrics is overwritten via relabeling.
		JobName:                 fmt.Sprintf("PodMonitoring/%s/%s/%s", pm.Namespace, pm.Name, &ep.Port),
		ServiceDiscoveryConfigs: discoveryCfgs,
		MetricsPath:             metricsPath,
		ScrapeInterval:          interval,
		ScrapeTimeout:           timeout,
		RelabelConfigs:          relabelCfgs,
	}, nil
}

// labelMappingRelabelConfigs generates relabel configs using a provided mapping and resource prefix.
func labelMappingRelabelConfigs(mappings []LabelMapping, prefix prommodel.LabelName) ([]*relabel.Config, error) {
	var relabelCfgs []*relabel.Config
	for _, m := range mappings {
		if collision := isPrometheusTargetLabel(m.To); collision {
			return nil, fmt.Errorf("relabel %q to %q conflicts with GMP target schema", m.From, m.To)
		}
		// `To` can be unset, default to `From`.
		if m.To == "" {
			m.To = m.From
		}
		relabelCfgs = append(relabelCfgs, &relabel.Config{
			Action:       relabel.Replace,
			SourceLabels: prommodel.LabelNames{prefix + sanitizeLabelName(m.From)},
			TargetLabel:  m.To,
		})
	}
	return relabelCfgs, nil
}

// PodMonitoringSpec contains specification parameters for PodMonitoring.
type PodMonitoringSpec struct {
	Selector     metav1.LabelSelector `json:"selector"`
	Endpoints    []ScrapeEndpoint     `json:"endpoints"`
	TargetLabels TargetLabels         `json:"targetLabels,omitempty"`
}

// ScrapeEndpoint specifies a Prometheus metrics endpoint to scrape.
type ScrapeEndpoint struct {
	// Name or number of the port to scrape.
	Port intstr.IntOrString `json:"port,omitempty"`
	// HTTP path to scrape metrics from. Defaults to "/metrics".
	Path string `json:"path,omitempty"`
	// Interval at which to scrape metrics. Must be a valid Prometheus duration.
	Interval string `json:"interval,omitempty"`
	// Timeout for metrics scrapes. Must be a valid Prometheus duration.
	// Must not be larger then the scrape interval.
	Timeout string `json:"timeout,omitempty"`
}

// TargetLabels groups label mappings by Kubernetes resource.
type TargetLabels struct {
	// Labels to transfer from the Kubernetes Pod to Prometheus target labels.
	// In the case of a label mapping conflict:
	// - Mappings at the end of the array take precedence.
	FromPod []LabelMapping `json:"fromPod,omitempty"`
}

// LabelMapping specifies how to transfer a label from a Kubernetes resource
// onto a Prometheus target.
type LabelMapping struct {
	// Kubenetes resource label to remap.
	From string `json:"from"`
	// Remapped Prometheus target label.
	// Defaults to the same name as `From`.
	To string `json:"to,omitempty"`
}

// PodMonitoringStatus holds status information of a PodMonitoring resource.
type PodMonitoringStatus struct {
	// The generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration"`
	// Represents the latest available observations of a podmonitor's current state.
	Conditions []MonitoringCondition `json:"conditions,omitempty"`
}

// MonitoringConditionType is the type of MonitoringCondition.
type MonitoringConditionType string

const (
	// ConfigurationCreateSuccess indicates that the config generated from the
	// monitoring resource was created successfully.
	ConfigurationCreateSuccess MonitoringConditionType = "ConfigurationCreateSuccess"
)

// MonitoringCondition describes a condition of a PodMonitoring.
type MonitoringCondition struct {
	Type MonitoringConditionType `json:"type"`
	// Status of the condition, one of True, False, Unknown.
	Status corev1.ConditionStatus `json:"status"`
	// The last time this condition was updated.
	// +optional
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty"`
	// Last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// The reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`
	// A human-readable message indicating details about the transition.
	// +optional
	Message string `json:"message,omitempty"`
}

// NewDefaultConditions returns a list of default conditions for at the given
// time for a `PodMonitoringStatus` if never explicitly set.
func NewDefaultConditions(now metav1.Time) []MonitoringCondition {
	return []MonitoringCondition{
		{
			Type:               ConfigurationCreateSuccess,
			Status:             corev1.ConditionUnknown,
			LastUpdateTime:     now,
			LastTransitionTime: now,
		},
	}
}

// Rules defines Prometheus alerting and recording rules that are scoped
// to the namespace of the resource. Only metric data from this namespace is processed
// and all rule results have their project_id, cluster, and namespace label preserved
// for query processing.
// The location, if not preserved by the rule, is set to the cluster's location.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type Rules struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Specification of desired Pod selection for target discovery by
	// Prometheus.
	Spec RulesSpec `json:"spec"`
	// Most recently observed status of the resource.
	// +optional
	Status RulesStatus `json:"status"`
}

// RulesList is a list of Rules.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type RulesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Rules `json:"items"`
}

// ClusterRules defines Prometheus alerting and recording rules that are scoped
// to the current cluster. Only metric data from the current cluster is processed
// and all rule results have their project_id and cluster label preserved
// for query processing.
// The location, if not preserved by the rule, is set to the cluster's location
//
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterRules struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// Specification of desired Pod selection for target discovery by
	// Prometheus.
	Spec RulesSpec `json:"spec"`
	// Most recently observed status of the resource.
	// +optional
	Status RulesStatus `json:"status"`
}

// ClusterRulesList is a list of ClusterRules.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterRulesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRules `json:"items"`
}

// RulesSpec contains specification parameters for a Rules resource.
type RulesSpec struct {
	// A list of Prometheus rule groups.
	Groups []RuleGroup `json:"groups"`
}

// RuleGroup declares rules in the Prometheus format:
// https://prometheus.io/docs/prometheus/latest/configuration/recording_rules/
type RuleGroup struct {
	// The name of the rule group.
	Name string `json:"name"`
	// The interval at which to evaluate the rules. Must be a valid Prometheus duration.
	Interval string `json:"interval"`
	// A list of rules that are executed sequentially as part of this group.
	Rules []Rule `json:"rules"`
}

// Rule is a single rule in the Prometheus format:
// https://prometheus.io/docs/prometheus/latest/configuration/recording_rules/
type Rule struct {
	// Record the result of the expression to this metric name.
	// Only one of `record` and `alert` must be set.
	Record string `json:"record,omitempty"`
	// Name of the alert to evaluate the expression as.
	// Only one of `record` and `alert` must be set.
	Alert string `json:"alert,omitempty"`
	// The PromQL expression to evaluate.
	Expr string `json:"expr"`
	// The duration to wait before a firing alert produced by this rule is sent to Alertmanager.
	// Only valid if `alert` is set.
	For string `json:"for,omitempty"`
	// A set of labels to attach to the result of the query expression.
	Labels map[string]string `json:"labels,omitempty"`
	// A set of annotations to attach to alerts produced by the query expression.
	// Only valid if `alert` is set.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// RulesStatus contains status information for a Rules resource.
type RulesStatus struct {
	// TODO: add status information.
}

// isPrometheusTargetLabel returns true if the label argument is in use by the Prometheus target schema.
func isPrometheusTargetLabel(label string) bool {
	switch label {
	case export.KeyProjectID, export.KeyLocation, export.KeyCluster, export.KeyNamespace, export.KeyJob, export.KeyInstance:
		return true
	default:
		return false
	}
}

var invalidLabelCharRE = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// sanitizeLabelName reproduces the label name cleanup Prometheus's service discovery applies.
func sanitizeLabelName(name string) prommodel.LabelName {
	return prommodel.LabelName(invalidLabelCharRE.ReplaceAllString(name, "_"))
}
