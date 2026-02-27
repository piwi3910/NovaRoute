package controller

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	novaroutev1alpha1 "github.com/piwi3910/NovaRoute/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = novaroutev1alpha1.AddToScheme(s)
	return s
}

func boolPtr(b bool) *bool    { return &b }
func int32Ptr(i int32) *int32 { return &i }

func newTestCluster() *novaroutev1alpha1.NovaRouteCluster {
	return &novaroutev1alpha1.NovaRouteCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-cluster",
			Namespace:  "nova-system",
			Generation: 1,
		},
		Spec: novaroutev1alpha1.NovaRouteClusterSpec{
			Version:         "v0.1.0",
			ImageRepository: "ghcr.io/piwi3910/novaroute",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Agent: novaroutev1alpha1.NovaRouteAgentSpec{
				MetricsPort: int32Ptr(9102),
				LogLevel:    "info",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
				Owners: []novaroutev1alpha1.OwnerSpec{
					{
						Name:         "novaedge",
						Token:        "test-token-1",
						PrefixType:   "host_only",
						AllowedCIDRs: []string{"10.0.0.0/8"},
					},
					{
						Name:       "admin",
						Token:      "test-token-2",
						PrefixType: "any",
					},
				},
			},
			FRR: novaroutev1alpha1.FRRSpec{
				Image: "ghcr.io/piwi3910/novaroute/novaroute-frr:10.5.1",
				Daemons: &novaroutev1alpha1.FRRDaemons{
					BGPD:  boolPtr(true),
					OSPFD: boolPtr(true),
					BFDD:  boolPtr(false),
				},
			},
			UpdateStrategy: &novaroutev1alpha1.UpdateStrategySpec{
				Type:           "RollingUpdate",
				MaxUnavailable: int32Ptr(1),
			},
		},
	}
}

func TestReconcile_CreatesAllResources(t *testing.T) {
	scheme := newScheme()
	cluster := newTestCluster()

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		WithStatusSubresource(cluster).
		Build()

	r := &NovaRouteClusterReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	// First reconcile adds the finalizer
	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}

	// Second reconcile creates all resources
	result, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after duration")
	}

	// Verify ServiceAccount
	sa := &corev1.ServiceAccount{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-agent",
		Namespace: "nova-system",
	}, sa); err != nil {
		t.Fatalf("expected ServiceAccount to be created: %v", err)
	}
	if sa.Labels["app.kubernetes.io/component"] != "agent" {
		t.Errorf("expected component label 'agent', got %q", sa.Labels["app.kubernetes.io/component"])
	}

	// Verify ClusterRole
	cr := &rbacv1.ClusterRole{}
	if err := client.Get(ctx, types.NamespacedName{
		Name: "nova-system-test-cluster-agent",
	}, cr); err != nil {
		t.Fatalf("expected ClusterRole to be created: %v", err)
	}
	if len(cr.Rules) != 2 {
		t.Errorf("expected 2 policy rules, got %d", len(cr.Rules))
	}

	// Verify ClusterRoleBinding
	crb := &rbacv1.ClusterRoleBinding{}
	if err := client.Get(ctx, types.NamespacedName{
		Name: "nova-system-test-cluster-agent",
	}, crb); err != nil {
		t.Fatalf("expected ClusterRoleBinding to be created: %v", err)
	}
	if crb.Subjects[0].Name != "test-cluster-agent" {
		t.Errorf("expected subject name 'test-cluster-agent', got %q", crb.Subjects[0].Name)
	}

	// Verify agent config ConfigMap
	configCM := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-config",
		Namespace: "nova-system",
	}, configCM); err != nil {
		t.Fatalf("expected agent config ConfigMap to be created: %v", err)
	}

	// Verify FRR bootstrap ConfigMap
	frrCM := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-frr-bootstrap",
		Namespace: "nova-system",
	}, frrCM); err != nil {
		t.Fatalf("expected FRR bootstrap ConfigMap to be created: %v", err)
	}

	// Verify DaemonSet
	ds := &appsv1.DaemonSet{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-agent",
		Namespace: "nova-system",
	}, ds); err != nil {
		t.Fatalf("expected DaemonSet to be created: %v", err)
	}

	// Verify metrics Service
	svc := &corev1.Service{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-metrics",
		Namespace: "nova-system",
	}, svc); err != nil {
		t.Fatalf("expected metrics Service to be created: %v", err)
	}
}

func TestReconcile_AgentConfigJSON(t *testing.T) {
	scheme := newScheme()
	cluster := newTestCluster()

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		WithStatusSubresource(cluster).
		Build()

	r := &NovaRouteClusterReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	// Run reconcile twice (first adds finalizer)
	_, _ = r.Reconcile(ctx, req)
	_, _ = r.Reconcile(ctx, req)

	// Get the config ConfigMap
	cm := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-config",
		Namespace: "nova-system",
	}, cm); err != nil {
		t.Fatalf("failed to get config ConfigMap: %v", err)
	}

	configJSON := cm.Data["config.json"]
	if configJSON == "" {
		t.Fatal("config.json is empty")
	}

	// Parse the JSON
	var cfg agentConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("failed to parse config JSON: %v", err)
	}

	// Verify owners
	if len(cfg.Owners) != 2 {
		t.Fatalf("expected 2 owners, got %d", len(cfg.Owners))
	}

	novaedgeOwner, ok := cfg.Owners["novaedge"]
	if !ok {
		t.Fatal("expected 'novaedge' owner in config")
	}
	if novaedgeOwner.Token != "test-token-1" {
		t.Errorf("expected novaedge token 'test-token-1', got %q", novaedgeOwner.Token)
	}
	if novaedgeOwner.AllowedPrefixes.Type != "host_only" {
		t.Errorf("expected prefix type 'host_only', got %q", novaedgeOwner.AllowedPrefixes.Type)
	}
	if len(novaedgeOwner.AllowedPrefixes.AllowedCIDRs) != 1 || novaedgeOwner.AllowedPrefixes.AllowedCIDRs[0] != "10.0.0.0/8" {
		t.Errorf("unexpected allowed CIDRs: %v", novaedgeOwner.AllowedPrefixes.AllowedCIDRs)
	}

	adminOwner, ok := cfg.Owners["admin"]
	if !ok {
		t.Fatal("expected 'admin' owner in config")
	}
	if adminOwner.AllowedPrefixes.Type != "any" {
		t.Errorf("expected prefix type 'any', got %q", adminOwner.AllowedPrefixes.Type)
	}
	// Admin has no allowedCIDRs specified, should be empty slice
	if len(adminOwner.AllowedPrefixes.AllowedCIDRs) != 0 {
		t.Errorf("expected empty allowed CIDRs for admin, got %v", adminOwner.AllowedPrefixes.AllowedCIDRs)
	}

	// Verify other config fields
	if cfg.ListenSocket != "/run/novaroute/novaroute.sock" {
		t.Errorf("unexpected listen_socket: %q", cfg.ListenSocket)
	}
	if cfg.MetricsAddress != ":9102" {
		t.Errorf("unexpected metrics_address: %q", cfg.MetricsAddress)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("unexpected log_level: %q", cfg.LogLevel)
	}
}

func TestReconcile_FRRDaemonsConfig(t *testing.T) {
	scheme := newScheme()
	cluster := newTestCluster()
	// BFD is disabled in test cluster

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		WithStatusSubresource(cluster).
		Build()

	r := &NovaRouteClusterReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	_, _ = r.Reconcile(ctx, req)
	_, _ = r.Reconcile(ctx, req)

	cm := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-frr-bootstrap",
		Namespace: "nova-system",
	}, cm); err != nil {
		t.Fatalf("failed to get FRR bootstrap ConfigMap: %v", err)
	}

	daemons := cm.Data["daemons"]
	if daemons == "" {
		t.Fatal("daemons config is empty")
	}

	// BGP and OSPF should be enabled, BFD disabled
	if !containsLine(daemons, "bgpd=yes") {
		t.Error("expected bgpd=yes in daemons config")
	}
	if !containsLine(daemons, "ospfd=yes") {
		t.Error("expected ospfd=yes in daemons config")
	}
	if !containsLine(daemons, "bfdd=no") {
		t.Error("expected bfdd=no in daemons config (bfdd disabled in spec)")
	}

	// Verify frr.conf exists
	if cm.Data["frr.conf"] == "" {
		t.Error("frr.conf is empty")
	}

	// Verify vtysh.conf exists
	if cm.Data["vtysh.conf"] == "" {
		t.Error("vtysh.conf is empty")
	}
}

func TestReconcile_DaemonSetSpec(t *testing.T) {
	scheme := newScheme()
	cluster := newTestCluster()

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		WithStatusSubresource(cluster).
		Build()

	r := &NovaRouteClusterReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	_, _ = r.Reconcile(ctx, req)
	_, _ = r.Reconcile(ctx, req)

	ds := &appsv1.DaemonSet{}
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "test-cluster-agent",
		Namespace: "nova-system",
	}, ds); err != nil {
		t.Fatalf("failed to get DaemonSet: %v", err)
	}

	podSpec := ds.Spec.Template.Spec

	// Verify hostNetwork
	if !podSpec.HostNetwork {
		t.Error("expected hostNetwork: true")
	}

	// Verify dnsPolicy
	if podSpec.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
		t.Errorf("expected dnsPolicy ClusterFirstWithHostNet, got %q", podSpec.DNSPolicy)
	}

	// Verify terminationGracePeriodSeconds
	if podSpec.TerminationGracePeriodSeconds == nil || *podSpec.TerminationGracePeriodSeconds != 60 {
		t.Error("expected terminationGracePeriodSeconds: 60")
	}

	// Verify 2 containers
	if len(podSpec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(podSpec.Containers))
	}

	// Verify agent container
	agentContainer := podSpec.Containers[0]
	if agentContainer.Name != "novaroute-agent" {
		t.Errorf("expected first container name 'novaroute-agent', got %q", agentContainer.Name)
	}
	if agentContainer.Image != "ghcr.io/piwi3910/novaroute/novaroute-agent:v0.1.0" {
		t.Errorf("unexpected agent image: %q", agentContainer.Image)
	}
	if agentContainer.SecurityContext == nil || agentContainer.SecurityContext.Capabilities == nil {
		t.Fatal("expected security context with capabilities on agent container")
	}
	hasNetAdmin := false
	for _, cap := range agentContainer.SecurityContext.Capabilities.Add {
		if cap == "NET_ADMIN" {
			hasNetAdmin = true
		}
	}
	if !hasNetAdmin {
		t.Error("expected NET_ADMIN capability on agent container")
	}

	// Verify FRR container
	frrContainer := podSpec.Containers[1]
	if frrContainer.Name != "frr" {
		t.Errorf("expected second container name 'frr', got %q", frrContainer.Name)
	}
	if frrContainer.SecurityContext == nil || frrContainer.SecurityContext.Capabilities == nil {
		t.Fatal("expected security context with capabilities on FRR container")
	}
	expectedCaps := map[corev1.Capability]bool{"NET_ADMIN": false, "NET_RAW": false, "SYS_ADMIN": false}
	for _, cap := range frrContainer.SecurityContext.Capabilities.Add {
		expectedCaps[cap] = true
	}
	for cap, found := range expectedCaps {
		if !found {
			t.Errorf("expected %s capability on FRR container", cap)
		}
	}

	// Verify volumes
	if len(podSpec.Volumes) != 4 {
		t.Fatalf("expected 4 volumes, got %d", len(podSpec.Volumes))
	}
	volumeNames := make(map[string]bool)
	for _, v := range podSpec.Volumes {
		volumeNames[v.Name] = true
	}
	for _, name := range []string{"run", "frr-sock", "config", "frr-config"} {
		if !volumeNames[name] {
			t.Errorf("expected volume %q", name)
		}
	}

	// Verify tolerations (should tolerate all taints)
	if len(podSpec.Tolerations) != 2 {
		t.Errorf("expected 2 tolerations, got %d", len(podSpec.Tolerations))
	}
}

func TestReconcile_NotFound(t *testing.T) {
	scheme := newScheme()

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &NovaRouteClusterReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent",
			Namespace: "nova-system",
		},
	}

	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile should not return error for missing resource: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("should not requeue for missing resource")
	}
}

func TestGetAgentImage(t *testing.T) {
	scheme := newScheme()
	r := &NovaRouteClusterReconciler{Scheme: scheme}

	tests := []struct {
		name     string
		cluster  *novaroutev1alpha1.NovaRouteCluster
		expected string
	}{
		{
			name: "default image from repo and version",
			cluster: &novaroutev1alpha1.NovaRouteCluster{
				Spec: novaroutev1alpha1.NovaRouteClusterSpec{
					Version:         "v0.2.0",
					ImageRepository: "ghcr.io/piwi3910/novaroute",
				},
			},
			expected: "ghcr.io/piwi3910/novaroute/novaroute-agent:v0.2.0",
		},
		{
			name: "override image",
			cluster: &novaroutev1alpha1.NovaRouteCluster{
				Spec: novaroutev1alpha1.NovaRouteClusterSpec{
					Version: "v0.2.0",
					Agent: novaroutev1alpha1.NovaRouteAgentSpec{
						Image: "custom-registry.io/my-agent:latest",
					},
				},
			},
			expected: "custom-registry.io/my-agent:latest",
		},
		{
			name: "default repo when not specified",
			cluster: &novaroutev1alpha1.NovaRouteCluster{
				Spec: novaroutev1alpha1.NovaRouteClusterSpec{
					Version: "v1.0.0",
				},
			},
			expected: "ghcr.io/piwi3910/novaroute/novaroute-agent:v1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.getAgentImage(tt.cluster)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestGetFRRImage(t *testing.T) {
	scheme := newScheme()
	r := &NovaRouteClusterReconciler{Scheme: scheme}

	tests := []struct {
		name     string
		cluster  *novaroutev1alpha1.NovaRouteCluster
		expected string
	}{
		{
			name: "default FRR image",
			cluster: &novaroutev1alpha1.NovaRouteCluster{
				Spec: novaroutev1alpha1.NovaRouteClusterSpec{
					FRR: novaroutev1alpha1.FRRSpec{},
				},
			},
			expected: "ghcr.io/piwi3910/novaroute/novaroute-frr:10.5.1",
		},
		{
			name: "override FRR image",
			cluster: &novaroutev1alpha1.NovaRouteCluster{
				Spec: novaroutev1alpha1.NovaRouteClusterSpec{
					FRR: novaroutev1alpha1.FRRSpec{
						Image: "custom-frr:9.0.0",
					},
				},
			},
			expected: "custom-frr:9.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.getFRRImage(tt.cluster)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestRenderDaemonsFile(t *testing.T) {
	r := &NovaRouteClusterReconciler{}

	content := r.renderDaemonsFile(true, false, true)

	if !containsLine(content, "bgpd=yes") {
		t.Error("expected bgpd=yes")
	}
	if !containsLine(content, "ospfd=no") {
		t.Error("expected ospfd=no")
	}
	if !containsLine(content, "bfdd=yes") {
		t.Error("expected bfdd=yes")
	}
	if !containsLine(content, "mgmtd=yes") {
		t.Error("expected mgmtd=yes (always enabled)")
	}
	if !containsLine(content, "watchfrr_enable=yes") {
		t.Error("expected watchfrr_enable=yes")
	}
}

// containsLine checks if a multiline string contains a line with the given content.
func containsLine(content, line string) bool {
	for _, l := range splitLines(content) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
