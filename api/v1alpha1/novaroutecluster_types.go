package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterPhase represents the lifecycle phase of a NovaRouteCluster.
type ClusterPhase string

// ClusterPhase constants define the possible lifecycle phases for a NovaRouteCluster.
const (
	ClusterPhasePending      ClusterPhase = "Pending"
	ClusterPhaseInitializing ClusterPhase = "Initializing"
	ClusterPhaseRunning      ClusterPhase = "Running"
	ClusterPhaseUpgrading    ClusterPhase = "Upgrading"
	ClusterPhaseDegraded     ClusterPhase = "Degraded"
	ClusterPhaseFailed       ClusterPhase = "Failed"
)

// NovaRouteClusterSpec defines the desired state of NovaRouteCluster
type NovaRouteClusterSpec struct {
	// Version is the version of NovaRoute components to deploy.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^v?[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+)?$`
	Version string `json:"version"`

	// ImageRepository is the container image repository.
	// +kubebuilder:default="ghcr.io/piwi3910/novaroute"
	// +optional
	ImageRepository string `json:"imageRepository,omitempty"`

	// ImagePullPolicy for all containers.
	// +kubebuilder:default="IfNotPresent"
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets for pulling images.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Agent defines the NovaRoute agent configuration.
	// +kubebuilder:validation:Required
	Agent NovaRouteAgentSpec `json:"agent"`

	// FRR defines the FRR sidecar configuration.
	// +kubebuilder:validation:Required
	FRR FRRSpec `json:"frr"`

	// UpdateStrategy for the DaemonSet.
	// +optional
	UpdateStrategy *UpdateStrategySpec `json:"updateStrategy,omitempty"`
}

// NovaRouteAgentSpec defines the agent container configuration.
type NovaRouteAgentSpec struct {
	// Image overrides the default agent image.
	// +optional
	Image string `json:"image,omitempty"`

	// Resources for the agent container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector for scheduling.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for scheduling.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// SocketPath is the Unix socket path for the gRPC API.
	// +kubebuilder:default="/run/novaroute/novaroute.sock"
	// +optional
	SocketPath string `json:"socketPath,omitempty"`

	// MetricsPort for Prometheus metrics and health endpoint.
	// +kubebuilder:default=9102
	// +optional
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// LogLevel for the agent.
	// +kubebuilder:default="info"
	// +kubebuilder:validation:Enum=debug;info;warn;error
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// Owners defines the gRPC client authentication and policy configuration.
	// +optional
	Owners []OwnerSpec `json:"owners,omitempty"`

	// ExtraArgs are additional command-line arguments.
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`

	// ExtraEnv are additional environment variables.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`
}

// OwnerSpec defines a gRPC client owner with auth and prefix policy.
type OwnerSpec struct {
	// Name is the owner identifier.
	Name string `json:"name"`
	// Token is the authentication token.
	Token string `json:"token"`
	// PrefixType controls which prefixes this owner can advertise.
	// +kubebuilder:validation:Enum=host_only;subnet;any
	PrefixType string `json:"prefixType"`
	// AllowedCIDRs restricts which CIDRs the owner can use.
	// +optional
	AllowedCIDRs []string `json:"allowedCIDRs,omitempty"`
}

// FRRSpec defines the FRR sidecar configuration.
type FRRSpec struct {
	// Image for the FRR sidecar.
	// +kubebuilder:default="ghcr.io/piwi3910/novaroute/novaroute-frr:10.5.1"
	// +optional
	Image string `json:"image,omitempty"`

	// Resources for the FRR container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Daemons controls which FRR daemons are enabled.
	// +optional
	Daemons *FRRDaemons `json:"daemons,omitempty"`
}

// FRRDaemons controls which FRR routing daemons are enabled.
type FRRDaemons struct {
	// +kubebuilder:default=true
	// +optional
	BGPD *bool `json:"bgpd,omitempty"`
	// +kubebuilder:default=true
	// +optional
	OSPFD *bool `json:"ospfd,omitempty"`
	// +kubebuilder:default=true
	// +optional
	BFDD *bool `json:"bfdd,omitempty"`
}

// UpdateStrategySpec defines the DaemonSet update strategy.
type UpdateStrategySpec struct {
	// Type is the update strategy type (RollingUpdate or OnDelete).
	// +kubebuilder:default="RollingUpdate"
	// +kubebuilder:validation:Enum=RollingUpdate;OnDelete
	Type string `json:"type,omitempty"`
	// MaxUnavailable for RollingUpdate.
	// +kubebuilder:default=1
	// +optional
	MaxUnavailable *int32 `json:"maxUnavailable,omitempty"`
}

// ComponentStatus holds the status of a managed component.
type ComponentStatus struct {
	Ready        bool  `json:"ready"`
	DesiredNodes int32 `json:"desiredNodes"`
	ReadyNodes   int32 `json:"readyNodes"`
	UpdatedNodes int32 `json:"updatedNodes"`
}

// NovaRouteClusterStatus defines the observed state of NovaRouteCluster
type NovaRouteClusterStatus struct {
	// Phase is the current lifecycle phase.
	Phase ClusterPhase `json:"phase,omitempty"`

	// Agent is the agent DaemonSet status.
	// +optional
	Agent *ComponentStatus `json:"agent,omitempty"`

	// Version is the observed deployed version.
	Version string `json:"version,omitempty"`

	// ObservedGeneration is the last observed generation.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nrc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.agent.readyNodes`
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.status.agent.desiredNodes`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NovaRouteCluster is the Schema for the novarouteclusters API
type NovaRouteCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NovaRouteClusterSpec   `json:"spec,omitempty"`
	Status NovaRouteClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NovaRouteClusterList contains a list of NovaRouteCluster
type NovaRouteClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NovaRouteCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NovaRouteCluster{}, &NovaRouteClusterList{})
}
