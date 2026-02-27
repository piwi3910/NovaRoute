// Package controller implements the NovaRoute operator controller that manages
// the lifecycle of NovaRouteCluster resources via Kubernetes reconciliation loops.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	novaroutev1alpha1 "github.com/piwi3910/NovaRoute/api/v1alpha1"
)

const (
	novaRouteClusterFinalizer = "novaroute.io/finalizer"

	// ConditionTypeReady indicates the overall readiness of the cluster.
	ConditionTypeReady = "Ready"
	// ConditionTypeAgentOK indicates the agent DaemonSet is ready.
	ConditionTypeAgentOK = "AgentReady"
	// ConditionTypeDegraded indicates the cluster is in a degraded state.
	ConditionTypeDegraded = "Degraded"
)

// NovaRouteClusterReconciler reconciles a NovaRouteCluster object.
type NovaRouteClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=novaroute.io,resources=novarouteclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=novaroute.io,resources=novarouteclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=novaroute.io,resources=novarouteclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services;serviceaccounts;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main Kubernetes reconciliation loop.
func (r *NovaRouteClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NovaRouteCluster", "name", req.Name, "namespace", req.Namespace)

	// Fetch the NovaRouteCluster instance
	cluster := &novaroutev1alpha1.NovaRouteCluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("NovaRouteCluster resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get NovaRouteCluster")
		return ctrl.Result{}, err
	}

	// Handle finalizer
	if cluster.DeletionTimestamp.IsZero() {
		// Add finalizer if not present
		if !controllerutil.ContainsFinalizer(cluster, novaRouteClusterFinalizer) {
			controllerutil.AddFinalizer(cluster, novaRouteClusterFinalizer)
			if err := r.Update(ctx, cluster); err != nil {
				return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
			}
		}
	} else {
		// Handle deletion
		if controllerutil.ContainsFinalizer(cluster, novaRouteClusterFinalizer) {
			if err := r.cleanupResources(ctx, cluster); err != nil {
				return ctrl.Result{}, fmt.Errorf("cleaning up resources: %w", err)
			}
			controllerutil.RemoveFinalizer(cluster, novaRouteClusterFinalizer)
			if err := r.Update(ctx, cluster); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Initialize status if needed
	if cluster.Status.Phase == "" {
		cluster.Status.Phase = novaroutev1alpha1.ClusterPhasePending
		if err := r.Status().Update(ctx, cluster); err != nil {
			return ctrl.Result{}, fmt.Errorf("initializing status: %w", err)
		}
	}

	// Reconcile all components
	var reconcileErrors []error

	// 1. Reconcile RBAC
	if err := r.reconcileRBAC(ctx, cluster); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("RBAC: %w", err))
	}

	// 2. Reconcile ConfigMaps
	if err := r.reconcileConfigMaps(ctx, cluster); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("configmaps: %w", err))
	}

	// 3. Reconcile DaemonSet
	if err := r.reconcileDaemonSet(ctx, cluster); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("daemonset: %w", err))
	}

	// 4. Reconcile metrics Service
	if err := r.reconcileService(ctx, cluster); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("service: %w", err))
	}

	// Update status
	if err := r.updateStatus(ctx, cluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	if len(reconcileErrors) > 0 {
		logger.Error(reconcileErrors[0], "reconciliation errors", "count", len(reconcileErrors))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	logger.Info("NovaRouteCluster reconciled successfully")
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// cleanupResources removes cluster-scoped resources that cannot be garbage-collected
// via owner references (ClusterRole, ClusterRoleBinding).
func (r *NovaRouteClusterReconciler) cleanupResources(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	logger := log.FromContext(ctx)
	logger.Info("Cleaning up resources for NovaRouteCluster", "name", cluster.Name)

	// Delete ClusterRoleBinding
	crb := &rbacv1.ClusterRoleBinding{}
	crbName := fmt.Sprintf("%s-%s-agent", cluster.Namespace, cluster.Name)
	if err := r.Get(ctx, types.NamespacedName{Name: crbName}, crb); err == nil {
		if err := r.Delete(ctx, crb); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting ClusterRoleBinding %s: %w", crbName, err)
		}
	}

	// Delete ClusterRole
	cr := &rbacv1.ClusterRole{}
	crName := fmt.Sprintf("%s-%s-agent", cluster.Namespace, cluster.Name)
	if err := r.Get(ctx, types.NamespacedName{Name: crName}, cr); err == nil {
		if err := r.Delete(ctx, cr); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting ClusterRole %s: %w", crName, err)
		}
	}

	return nil
}

// ----------------------------------------------------------------------------
// RBAC
// ----------------------------------------------------------------------------

func (r *NovaRouteClusterReconciler) reconcileRBAC(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	if err := r.reconcileServiceAccount(ctx, cluster); err != nil {
		return fmt.Errorf("ServiceAccount: %w", err)
	}
	if err := r.reconcileClusterRole(ctx, cluster); err != nil {
		return fmt.Errorf("ClusterRole: %w", err)
	}
	if err := r.reconcileClusterRoleBinding(ctx, cluster); err != nil {
		return fmt.Errorf("ClusterRoleBinding: %w", err)
	}
	return nil
}

func (r *NovaRouteClusterReconciler) reconcileServiceAccount(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.getServiceAccountName(cluster),
			Namespace: cluster.Namespace,
			Labels:    r.getLabels(cluster, "agent"),
		},
	}

	if err := controllerutil.SetControllerReference(cluster, sa, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	return r.createOrUpdate(ctx, sa)
}

func (r *NovaRouteClusterReconciler) reconcileClusterRole(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-%s-agent", cluster.Namespace, cluster.Name),
			Labels: r.getLabels(cluster, "agent"),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"create", "patch"},
			},
		},
	}

	return r.createOrUpdate(ctx, cr)
}

func (r *NovaRouteClusterReconciler) reconcileClusterRoleBinding(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-%s-agent", cluster.Namespace, cluster.Name),
			Labels: r.getLabels(cluster, "agent"),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     fmt.Sprintf("%s-%s-agent", cluster.Namespace, cluster.Name),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      r.getServiceAccountName(cluster),
				Namespace: cluster.Namespace,
			},
		},
	}

	return r.createOrUpdate(ctx, crb)
}

// ----------------------------------------------------------------------------
// ConfigMaps
// ----------------------------------------------------------------------------

// agentConfig represents the JSON config the NovaRoute agent reads.
type agentConfig struct {
	ListenSocket          string                     `json:"listen_socket"`
	FRR                   agentFRRConfig             `json:"frr"`
	Owners                map[string]agentOwnerEntry `json:"owners"`
	LogLevel              string                     `json:"log_level"`
	MetricsAddress        string                     `json:"metrics_address"`
	DisconnectGracePeriod int                        `json:"disconnect_grace_period"`
}

type agentFRRConfig struct {
	SocketDir      string `json:"socket_dir"`
	ConnectTimeout int    `json:"connect_timeout"`
	RetryInterval  int    `json:"retry_interval"`
}

type agentOwnerEntry struct {
	Token           string               `json:"token"`
	AllowedPrefixes agentAllowedPrefixes `json:"allowed_prefixes"`
}

type agentAllowedPrefixes struct {
	Type         string   `json:"type"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
}

func (r *NovaRouteClusterReconciler) reconcileConfigMaps(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	if err := r.reconcileAgentConfigMap(ctx, cluster); err != nil {
		return fmt.Errorf("agent config: %w", err)
	}
	if err := r.reconcileFRRBootstrapConfigMap(ctx, cluster); err != nil {
		return fmt.Errorf("FRR bootstrap: %w", err)
	}
	return nil
}

func (r *NovaRouteClusterReconciler) reconcileAgentConfigMap(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	metricsPort := int32(9102)
	if cluster.Spec.Agent.MetricsPort != nil {
		metricsPort = *cluster.Spec.Agent.MetricsPort
	}

	socketPath := "/run/novaroute/novaroute.sock"
	if cluster.Spec.Agent.SocketPath != "" {
		socketPath = cluster.Spec.Agent.SocketPath
	}

	logLevel := "info"
	if cluster.Spec.Agent.LogLevel != "" {
		logLevel = cluster.Spec.Agent.LogLevel
	}

	// Build the owners map
	owners := make(map[string]agentOwnerEntry)
	for _, o := range cluster.Spec.Agent.Owners {
		cidrs := o.AllowedCIDRs
		if cidrs == nil {
			cidrs = []string{}
		}
		owners[o.Name] = agentOwnerEntry{
			Token: o.Token,
			AllowedPrefixes: agentAllowedPrefixes{
				Type:         o.PrefixType,
				AllowedCIDRs: cidrs,
			},
		}
	}

	cfg := agentConfig{
		ListenSocket: socketPath,
		FRR: agentFRRConfig{
			SocketDir:      "/run/frr",
			ConnectTimeout: 10,
			RetryInterval:  5,
		},
		Owners:                owners,
		LogLevel:              logLevel,
		MetricsAddress:        fmt.Sprintf(":%d", metricsPort),
		DisconnectGracePeriod: 30,
	}

	cfgJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent config JSON: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-config", cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    r.getLabels(cluster, "config"),
		},
		Data: map[string]string{
			"config.json": string(cfgJSON),
		},
	}

	if err := controllerutil.SetControllerReference(cluster, cm, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	return r.createOrUpdate(ctx, cm)
}

func (r *NovaRouteClusterReconciler) reconcileFRRBootstrapConfigMap(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	// Build the daemons config from spec
	bgpd := true
	ospfd := true
	bfdd := true
	if cluster.Spec.FRR.Daemons != nil {
		if cluster.Spec.FRR.Daemons.BGPD != nil {
			bgpd = *cluster.Spec.FRR.Daemons.BGPD
		}
		if cluster.Spec.FRR.Daemons.OSPFD != nil {
			ospfd = *cluster.Spec.FRR.Daemons.OSPFD
		}
		if cluster.Spec.FRR.Daemons.BFDD != nil {
			bfdd = *cluster.Spec.FRR.Daemons.BFDD
		}
	}

	daemonsContent := r.renderDaemonsFile(bgpd, ospfd, bfdd)

	frrConf := `! FRR bootstrap configuration.
! NovaRoute manages runtime configuration via the VTY socket interface.
! This file provides minimal bootstrap settings.
!
frr version 10.5.1
frr defaults datacenter
!
log syslog informational
!
! No static configuration -- NovaRoute will configure BGP, BFD, and OSPF
! dynamically via the VTY Unix sockets.
!
line vty
!
`

	vtyshConf := `! vtysh configuration
service integrated-vtysh-config
`

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-frr-bootstrap", cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    r.getLabels(cluster, "frr-config"),
		},
		Data: map[string]string{
			"daemons":    daemonsContent,
			"frr.conf":   frrConf,
			"vtysh.conf": vtyshConf,
		},
	}

	if err := controllerutil.SetControllerReference(cluster, cm, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	return r.createOrUpdate(ctx, cm)
}

func (r *NovaRouteClusterReconciler) renderDaemonsFile(bgpd, ospfd, bfdd bool) string {
	boolToYesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}

	var sb strings.Builder
	sb.WriteString("# FRR daemon configuration -- enable the daemons NovaRoute needs.\n")
	sb.WriteString("#\n")
	sb.WriteString("# NovaRoute manages all configuration via the VTY Unix socket interface.\n")
	sb.WriteString("# Each enabled daemon creates a <daemon>.vty socket in /run/frr/.\n")
	sb.WriteString(fmt.Sprintf("bgpd=%s\n", boolToYesNo(bgpd)))
	sb.WriteString(fmt.Sprintf("ospfd=%s\n", boolToYesNo(ospfd)))
	sb.WriteString("ospf6d=no\n")
	sb.WriteString("ripd=no\n")
	sb.WriteString("ripngd=no\n")
	sb.WriteString("isisd=no\n")
	sb.WriteString("pimd=no\n")
	sb.WriteString("pim6d=no\n")
	sb.WriteString("ldpd=no\n")
	sb.WriteString("nhrpd=no\n")
	sb.WriteString("eigrpd=no\n")
	sb.WriteString("babeld=no\n")
	sb.WriteString("sharpd=no\n")
	sb.WriteString("pbrd=no\n")
	sb.WriteString(fmt.Sprintf("bfdd=%s\n", boolToYesNo(bfdd)))
	sb.WriteString("fabricd=no\n")
	sb.WriteString("vrrpd=no\n")
	sb.WriteString("pathd=no\n")
	sb.WriteString("\n")
	sb.WriteString("#\n")
	sb.WriteString("# Zebra is always started and cannot be disabled.\n")
	sb.WriteString("#\n")
	sb.WriteString("\n")
	sb.WriteString("# Management daemon.\n")
	sb.WriteString("mgmtd=yes\n")
	sb.WriteString("\n")
	sb.WriteString("# Options for the daemons.\n")
	sb.WriteString("mgmtd_options=\"--log syslog informational\"\n")
	sb.WriteString("zebra_options=\"-A 127.0.0.1 -s 90000000\"\n")
	sb.WriteString("bgpd_options=\"-A 127.0.0.1 -p 0\"\n")
	sb.WriteString("ospfd_options=\"-A 127.0.0.1\"\n")
	sb.WriteString("bfdd_options=\"-A 127.0.0.1\"\n")
	sb.WriteString("\n")
	sb.WriteString("# The FRR_DEFAULT_PROFILE sets the default configuration profile.\n")
	sb.WriteString("# Use \"datacenter\" for data center deployments.\n")
	sb.WriteString("frr_profile=\"datacenter\"\n")
	sb.WriteString("\n")
	sb.WriteString("# Maximum number of FIB routes.\n")
	sb.WriteString("MAX_FIB_ROUTES=1000000\n")
	sb.WriteString("\n")
	sb.WriteString("# watchfrr options -- restart daemons that crash.\n")
	sb.WriteString("watchfrr_enable=yes\n")
	sb.WriteString("watchfrr_options=\"\"\n")

	return sb.String()
}

// ----------------------------------------------------------------------------
// DaemonSet
// ----------------------------------------------------------------------------

func (r *NovaRouteClusterReconciler) reconcileDaemonSet(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	metricsPort := int32(9102)
	if cluster.Spec.Agent.MetricsPort != nil {
		metricsPort = *cluster.Spec.Agent.MetricsPort
	}

	// Resolve agent image
	agentImage := r.getAgentImage(cluster)
	frrImage := r.getFRRImage(cluster)

	// Build agent container
	agentContainer := corev1.Container{
		Name:            "novaroute-agent",
		Image:           agentImage,
		ImagePullPolicy: cluster.Spec.ImagePullPolicy,
		Args:            append([]string{"--config=/etc/novaroute/config.json"}, cluster.Spec.Agent.ExtraArgs...),
		Ports: []corev1.ContainerPort{
			{
				Name:          "metrics",
				ContainerPort: metricsPort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources: cluster.Spec.Agent.Resources,
		Env:       cluster.Spec.Agent.ExtraEnv,
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(metricsPort),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       15,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/readyz",
					Port: intstr.FromInt32(metricsPort),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN"},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "run",
				MountPath: "/run/novaroute",
			},
			{
				Name:      "frr-sock",
				MountPath: "/run/frr",
			},
			{
				Name:      "config",
				MountPath: "/etc/novaroute",
				ReadOnly:  true,
			},
		},
	}

	// Build FRR sidecar container
	frrContainer := corev1.Container{
		Name:            "frr",
		Image:           frrImage,
		ImagePullPolicy: cluster.Spec.ImagePullPolicy,
		Resources:       cluster.Spec.FRR.Resources,
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN", "NET_RAW", "SYS_ADMIN"},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "frr-sock",
				MountPath: "/run/frr",
			},
			{
				Name:      "frr-config",
				MountPath: "/etc/frr",
				ReadOnly:  true,
			},
		},
	}

	// Build tolerations -- tolerate all taints by default
	tolerations := cluster.Spec.Agent.Tolerations
	if len(tolerations) == 0 {
		tolerations = []corev1.Toleration{
			{
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
			{
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoExecute,
			},
		}
	}

	terminationGracePeriod := int64(60)

	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-agent", cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    r.getLabels(cluster, "agent"),
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: r.getSelectorLabels(cluster, "agent"),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: r.getLabels(cluster, "agent"),
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/port":   fmt.Sprintf("%d", metricsPort),
						"prometheus.io/path":   "/metrics",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:            r.getServiceAccountName(cluster),
					HostNetwork:                   true,
					DNSPolicy:                     corev1.DNSClusterFirstWithHostNet,
					NodeSelector:                  cluster.Spec.Agent.NodeSelector,
					Tolerations:                   tolerations,
					ImagePullSecrets:              cluster.Spec.ImagePullSecrets,
					TerminationGracePeriodSeconds: &terminationGracePeriod,
					Containers:                    []corev1.Container{agentContainer, frrContainer},
					Volumes: []corev1.Volume{
						{
							Name: "run",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/run/novaroute",
									Type: hostPathTypePtr(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "frr-sock",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: fmt.Sprintf("%s-config", cluster.Name),
									},
								},
							},
						},
						{
							Name: "frr-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: fmt.Sprintf("%s-frr-bootstrap", cluster.Name),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Apply update strategy
	if cluster.Spec.UpdateStrategy != nil {
		if cluster.Spec.UpdateStrategy.Type == "OnDelete" {
			daemonSet.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.OnDeleteDaemonSetStrategyType,
			}
		} else {
			maxUnavailable := intstr.FromInt32(1)
			if cluster.Spec.UpdateStrategy.MaxUnavailable != nil {
				maxUnavailable = intstr.FromInt32(*cluster.Spec.UpdateStrategy.MaxUnavailable)
			}
			daemonSet.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			}
		}
	}

	if err := controllerutil.SetControllerReference(cluster, daemonSet, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	return r.createOrUpdate(ctx, daemonSet)
}

// ----------------------------------------------------------------------------
// Metrics Service
// ----------------------------------------------------------------------------

func (r *NovaRouteClusterReconciler) reconcileService(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	metricsPort := int32(9102)
	if cluster.Spec.Agent.MetricsPort != nil {
		metricsPort = *cluster.Spec.Agent.MetricsPort
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-metrics", cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    r.getLabels(cluster, "metrics"),
		},
		Spec: corev1.ServiceSpec{
			Selector: r.getSelectorLabels(cluster, "agent"),
			Ports: []corev1.ServicePort{
				{
					Name:       "metrics",
					Port:       metricsPort,
					TargetPort: intstr.FromInt32(metricsPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			ClusterIP: "None", // headless service for per-node scraping
		},
	}

	if err := controllerutil.SetControllerReference(cluster, svc, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	return r.createOrUpdate(ctx, svc)
}

// ----------------------------------------------------------------------------
// Status
// ----------------------------------------------------------------------------

func (r *NovaRouteClusterReconciler) updateStatus(ctx context.Context, cluster *novaroutev1alpha1.NovaRouteCluster) error {
	logger := log.FromContext(ctx)

	// Fetch agent DaemonSet status
	agentDS := &appsv1.DaemonSet{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      fmt.Sprintf("%s-agent", cluster.Name),
		Namespace: cluster.Namespace,
	}, agentDS); err == nil {
		cluster.Status.Agent = &novaroutev1alpha1.ComponentStatus{
			Ready:        agentDS.Status.NumberReady == agentDS.Status.DesiredNumberScheduled,
			DesiredNodes: agentDS.Status.DesiredNumberScheduled,
			ReadyNodes:   agentDS.Status.NumberReady,
			UpdatedNodes: agentDS.Status.UpdatedNumberScheduled,
		}
	}

	// Update conditions
	agentReady := cluster.Status.Agent != nil && cluster.Status.Agent.Ready

	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeAgentOK,
		Status:             conditionStatus(agentReady),
		ObservedGeneration: cluster.Generation,
		Reason:             conditionReason(agentReady, "AgentReady", "AgentNotReady"),
		Message:            conditionMessage(agentReady, "Agent DaemonSet is ready", "Agent DaemonSet is not ready"),
	})

	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeReady,
		Status:             conditionStatus(agentReady),
		ObservedGeneration: cluster.Generation,
		Reason:             conditionReason(agentReady, "AllComponentsReady", "SomeComponentsNotReady"),
		Message:            conditionMessage(agentReady, "All components are ready", "Some components are not ready"),
	})

	// Update phase
	switch {
	case agentReady:
		cluster.Status.Phase = novaroutev1alpha1.ClusterPhaseRunning
	case cluster.Status.Agent != nil && cluster.Status.Agent.ReadyNodes > 0:
		cluster.Status.Phase = novaroutev1alpha1.ClusterPhaseDegraded
	default:
		cluster.Status.Phase = novaroutev1alpha1.ClusterPhaseInitializing
	}

	cluster.Status.Version = cluster.Spec.Version
	cluster.Status.ObservedGeneration = cluster.Generation

	if err := r.Status().Update(ctx, cluster); err != nil {
		logger.Error(err, "failed to update status")
		return err
	}

	return nil
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func (r *NovaRouteClusterReconciler) getLabels(cluster *novaroutev1alpha1.NovaRouteCluster, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "novaroute",
		"app.kubernetes.io/instance":   cluster.Name,
		"app.kubernetes.io/component":  component,
		"app.kubernetes.io/version":    cluster.Spec.Version,
		"app.kubernetes.io/managed-by": "novaroute-operator",
	}
}

func (r *NovaRouteClusterReconciler) getSelectorLabels(cluster *novaroutev1alpha1.NovaRouteCluster, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "novaroute",
		"app.kubernetes.io/instance":  cluster.Name,
		"app.kubernetes.io/component": component,
	}
}

func (r *NovaRouteClusterReconciler) getServiceAccountName(cluster *novaroutev1alpha1.NovaRouteCluster) string {
	return fmt.Sprintf("%s-agent", cluster.Name)
}

func (r *NovaRouteClusterReconciler) getAgentImage(cluster *novaroutev1alpha1.NovaRouteCluster) string {
	if cluster.Spec.Agent.Image != "" {
		return cluster.Spec.Agent.Image
	}
	repo := "ghcr.io/piwi3910/novaroute"
	if cluster.Spec.ImageRepository != "" {
		repo = cluster.Spec.ImageRepository
	}
	return fmt.Sprintf("%s/novaroute-agent:%s", repo, cluster.Spec.Version)
}

func (r *NovaRouteClusterReconciler) getFRRImage(cluster *novaroutev1alpha1.NovaRouteCluster) string {
	if cluster.Spec.FRR.Image != "" {
		return cluster.Spec.FRR.Image
	}
	return "ghcr.io/piwi3910/novaroute/novaroute-frr:10.5.1"
}

func (r *NovaRouteClusterReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
	logger := log.FromContext(ctx)

	key := client.ObjectKeyFromObject(obj)
	existingObj := obj.DeepCopyObject()
	existing, ok := existingObj.(client.Object)
	if !ok {
		return fmt.Errorf("object does not implement client.Object")
	}

	if err := r.Get(ctx, key, existing); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Creating resource",
				"kind", obj.GetObjectKind().GroupVersionKind().Kind,
				"name", key.Name,
				"namespace", key.Namespace)
			return r.Create(ctx, obj)
		}
		return fmt.Errorf("getting existing resource: %w", err)
	}

	// Update existing resource
	obj.SetResourceVersion(existing.GetResourceVersion())
	logger.V(1).Info("Updating resource",
		"kind", obj.GetObjectKind().GroupVersionKind().Kind,
		"name", key.Name,
		"namespace", key.Namespace)
	return r.Update(ctx, obj)
}

func hostPathTypePtr(t corev1.HostPathType) *corev1.HostPathType {
	return &t
}

func conditionStatus(ready bool) metav1.ConditionStatus {
	if ready {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func conditionReason(ready bool, trueReason, falseReason string) string {
	if ready {
		return trueReason
	}
	return falseReason
}

func conditionMessage(ready bool, trueMsg, falseMsg string) string {
	if ready {
		return trueMsg
	}
	return falseMsg
}

// SetupWithManager sets up the controller with the Manager.
func (r *NovaRouteClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&novaroutev1alpha1.NovaRouteCluster{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
