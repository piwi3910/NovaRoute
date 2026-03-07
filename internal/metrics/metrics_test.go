package routemetrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// resetMetrics resets all counter/gauge/histogram metrics so tests are isolated.
// promauto-registered metrics share the default registry, so we reset
// them between tests to avoid cross-contamination.
func resetCounterVec(cv *prometheus.CounterVec) {
	cv.Reset()
}

func resetGaugeVec(gv *prometheus.GaugeVec) {
	gv.Reset()
}

func TestRecordIntent(t *testing.T) {
	t.Cleanup(func() { resetCounterVec(IntentsTotal) })

	tests := []struct {
		name      string
		owner     string
		typ       string
		operation string
	}{
		{name: "set peer", owner: "tenant-a", typ: "peer", operation: "set"},
		{name: "remove peer", owner: "tenant-a", typ: "peer", operation: "remove"},
		{name: "set prefix", owner: "tenant-b", typ: "prefix", operation: "set"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := testutil.ToFloat64(IntentsTotal.WithLabelValues(tc.owner, tc.typ, tc.operation))
			RecordIntent(tc.owner, tc.typ, tc.operation)
			after := testutil.ToFloat64(IntentsTotal.WithLabelValues(tc.owner, tc.typ, tc.operation))
			if after != before+1 {
				t.Errorf("expected counter to increment by 1, got diff=%f", after-before)
			}
		})
	}
}

func TestRecordFRRTransaction(t *testing.T) {
	t.Cleanup(func() { resetCounterVec(FRRTransactionsTotal) })

	tests := []struct {
		name     string
		result   string
		duration float64
	}{
		{name: "success", result: "success", duration: 0.5},
		{name: "failure", result: "failure", duration: 1.2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := testutil.ToFloat64(FRRTransactionsTotal.WithLabelValues(tc.result))
			RecordFRRTransaction(tc.result, tc.duration)
			after := testutil.ToFloat64(FRRTransactionsTotal.WithLabelValues(tc.result))
			if after != before+1 {
				t.Errorf("expected counter to increment by 1, got diff=%f", after-before)
			}
		})
	}

	// Verify histogram got observations via the default gatherer.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	found := false
	for _, fam := range families {
		if fam.GetName() == "novaroute_frr_transaction_duration_seconds" {
			found = true
			for _, m := range fam.GetMetric() {
				if m.GetHistogram().GetSampleCount() == 0 {
					t.Error("expected non-zero sample count in FRR transaction duration histogram")
				}
			}
		}
	}
	if !found {
		t.Error("expected novaroute_frr_transaction_duration_seconds metric family to exist")
	}
}

func TestRecordPolicyViolation(t *testing.T) {
	t.Cleanup(func() { resetCounterVec(PolicyViolationsTotal) })

	RecordPolicyViolation("tenant-a", "prefix_conflict")
	RecordPolicyViolation("tenant-a", "prefix_conflict")
	RecordPolicyViolation("tenant-b", "invalid_token")

	got := testutil.ToFloat64(PolicyViolationsTotal.WithLabelValues("tenant-a", "prefix_conflict"))
	if got != 2 {
		t.Errorf("expected 2, got %f", got)
	}

	got = testutil.ToFloat64(PolicyViolationsTotal.WithLabelValues("tenant-b", "invalid_token"))
	if got != 1 {
		t.Errorf("expected 1, got %f", got)
	}
}

func TestRecordEvent(t *testing.T) {
	t.Cleanup(func() { resetCounterVec(EventsTotal) })

	RecordEvent("peer_up")
	RecordEvent("peer_up")
	RecordEvent("peer_down")

	got := testutil.ToFloat64(EventsTotal.WithLabelValues("peer_up"))
	if got != 2 {
		t.Errorf("expected 2, got %f", got)
	}

	got = testutil.ToFloat64(EventsTotal.WithLabelValues("peer_down"))
	if got != 1 {
		t.Errorf("expected 1, got %f", got)
	}
}

func TestSetActivePeers(t *testing.T) {
	t.Cleanup(func() { resetGaugeVec(ActivePeers) })

	SetActivePeers("tenant-a", 5)
	got := testutil.ToFloat64(ActivePeers.WithLabelValues("tenant-a"))
	if got != 5 {
		t.Errorf("expected 5, got %f", got)
	}

	SetActivePeers("tenant-a", 3)
	got = testutil.ToFloat64(ActivePeers.WithLabelValues("tenant-a"))
	if got != 3 {
		t.Errorf("expected 3 after update, got %f", got)
	}
}

func TestSetActivePrefixes(t *testing.T) {
	t.Cleanup(func() { resetGaugeVec(ActivePrefixes) })

	SetActivePrefixes("tenant-a", "bgp", 10)
	SetActivePrefixes("tenant-a", "ospf", 4)

	got := testutil.ToFloat64(ActivePrefixes.WithLabelValues("tenant-a", "bgp"))
	if got != 10 {
		t.Errorf("expected 10, got %f", got)
	}

	got = testutil.ToFloat64(ActivePrefixes.WithLabelValues("tenant-a", "ospf"))
	if got != 4 {
		t.Errorf("expected 4, got %f", got)
	}
}

func TestSetActiveBFDSessions(t *testing.T) {
	t.Cleanup(func() { resetGaugeVec(ActiveBFDSessions) })

	SetActiveBFDSessions("tenant-a", 2)
	got := testutil.ToFloat64(ActiveBFDSessions.WithLabelValues("tenant-a"))
	if got != 2 {
		t.Errorf("expected 2, got %f", got)
	}
}

func TestSetActiveOSPFInterfaces(t *testing.T) {
	t.Cleanup(func() { resetGaugeVec(ActiveOSPFInterfaces) })

	SetActiveOSPFInterfaces("tenant-a", 7)
	got := testutil.ToFloat64(ActiveOSPFInterfaces.WithLabelValues("tenant-a"))
	if got != 7 {
		t.Errorf("expected 7, got %f", got)
	}
}

func TestSetRegisteredOwners(t *testing.T) {
	SetRegisteredOwners(3)
	got := testutil.ToFloat64(RegisteredOwners)
	if got != 3 {
		t.Errorf("expected 3, got %f", got)
	}

	SetRegisteredOwners(0)
	got = testutil.ToFloat64(RegisteredOwners)
	if got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestSetFRRConnected(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		expected  float64
	}{
		{name: "connected", connected: true, expected: 1},
		{name: "disconnected", connected: false, expected: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			SetFRRConnected(tc.connected)
			got := testutil.ToFloat64(FRRConnected)
			if got != tc.expected {
				t.Errorf("expected %f, got %f", tc.expected, got)
			}
		})
	}
}

func TestObserveGRPCDuration(t *testing.T) {
	// ObserveGRPCDuration should not panic and should record observations.
	ObserveGRPCDuration("Register", 0.05)
	ObserveGRPCDuration("Register", 0.1)
	ObserveGRPCDuration("GetStatus", 0.02)

	// Verify the histogram vec has observations by gathering from default registry.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	found := false
	for _, fam := range families {
		if fam.GetName() == "novaroute_grpc_request_duration_seconds" {
			found = true
			if len(fam.GetMetric()) == 0 {
				t.Error("expected at least one metric in gRPC duration histogram")
			}
		}
	}
	if !found {
		t.Error("expected novaroute_grpc_request_duration_seconds metric family to exist")
	}
}

func TestRecordReconcileCycleDuration(t *testing.T) {
	RecordReconcileCycleDuration(0.5)
	RecordReconcileCycleDuration(1.0)

	// Verify histogram has observations via default gatherer.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	found := false
	for _, fam := range families {
		if fam.GetName() == "novaroute_reconcile_cycle_duration_seconds" {
			found = true
			if len(fam.GetMetric()) == 0 {
				t.Error("expected at least one metric in reconcile cycle duration histogram")
			}
		}
	}
	if !found {
		t.Error("expected novaroute_reconcile_cycle_duration_seconds metric family to exist")
	}
}

func TestRecordEventDropped(t *testing.T) {
	before := testutil.ToFloat64(EventsDropped)
	RecordEventDropped()
	RecordEventDropped()
	after := testutil.ToFloat64(EventsDropped)
	if after != before+2 {
		t.Errorf("expected counter to increment by 2, got diff=%f", after-before)
	}
}

func TestRecordMonitoringError(t *testing.T) {
	t.Cleanup(func() { resetCounterVec(MonitoringErrors) })

	RecordMonitoringError("bgp")
	RecordMonitoringError("bgp")
	RecordMonitoringError("ospf")

	got := testutil.ToFloat64(MonitoringErrors.WithLabelValues("bgp"))
	if got != 2 {
		t.Errorf("expected 2, got %f", got)
	}

	got = testutil.ToFloat64(MonitoringErrors.WithLabelValues("ospf"))
	if got != 1 {
		t.Errorf("expected 1, got %f", got)
	}
}
