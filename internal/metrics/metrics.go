// Package routemetrics provides Prometheus metrics for NovaRoute.
package routemetrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Counters

// IntentsTotal tracks total intent operations (set, remove) by owner and type.
var IntentsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "novaroute_intents_total",
		Help: "Total intent operations (set, remove).",
	},
	[]string{"owner", "type", "operation"},
)

// FRRTransactionsTotal tracks FRR candidate/commit operations by result (success, failure).
var FRRTransactionsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "novaroute_frr_transactions_total",
		Help: "FRR candidate/commit operations (success, failure).",
	},
	[]string{"result"},
)

// PolicyViolationsTotal tracks policy check failures by owner and reason.
var PolicyViolationsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "novaroute_policy_violations_total",
		Help: "Policy check failures.",
	},
	[]string{"owner", "reason"},
)

// EventsTotal tracks events emitted by type.
var EventsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "novaroute_events_total",
		Help: "Events emitted.",
	},
	[]string{"type"},
)

// Gauges

// ActivePeers tracks the current number of active BGP peers by owner.
var ActivePeers = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "novaroute_active_peers",
		Help: "Current active BGP peers by owner.",
	},
	[]string{"owner"},
)

// ActivePrefixes tracks the current number of advertised prefixes by owner and protocol.
var ActivePrefixes = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "novaroute_active_prefixes",
		Help: "Current advertised prefixes.",
	},
	[]string{"owner", "protocol"},
)

// ActiveBFDSessions tracks the current number of BFD sessions by owner.
var ActiveBFDSessions = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "novaroute_active_bfd_sessions",
		Help: "Current BFD sessions.",
	},
	[]string{"owner"},
)

// ActiveOSPFInterfaces tracks the current number of OSPF interfaces by owner.
var ActiveOSPFInterfaces = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "novaroute_active_ospf_interfaces",
		Help: "Current OSPF interfaces.",
	},
	[]string{"owner"},
)

// RegisteredOwners tracks the number of registered owners.
var RegisteredOwners = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "novaroute_registered_owners",
		Help: "Number of registered owners.",
	},
)

// FRRConnected indicates whether the system is connected to FRR (1 = connected, 0 = disconnected).
var FRRConnected = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "novaroute_frr_connected",
		Help: "1 if connected to FRR, 0 otherwise.",
	},
)

// Histograms

// FRRTransactionDuration observes FRR transaction latency in seconds.
var FRRTransactionDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "novaroute_frr_transaction_duration_seconds",
		Help:    "FRR transaction latency.",
		Buckets: prometheus.DefBuckets,
	},
)

// GRPCRequestDuration observes gRPC request latency in seconds by method.
var GRPCRequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "novaroute_grpc_request_duration_seconds",
		Help:    "gRPC request latency.",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"method"},
)

// ReconcileCycleDuration observes the duration of each reconciliation cycle in seconds.
var ReconcileCycleDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "novaroute_reconcile_cycle_duration_seconds",
		Help:    "Reconciliation cycle latency.",
		Buckets: prometheus.DefBuckets,
	},
)

// EventsDropped tracks events dropped due to slow subscribers.
var EventsDropped = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "novaroute_events_dropped_total",
		Help: "Total events dropped due to slow subscribers.",
	},
)

// MonitoringErrors tracks errors encountered during FRR state monitoring by protocol.
var MonitoringErrors = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "novaroute_monitoring_errors_total",
		Help: "Errors encountered during FRR state monitoring.",
	},
	[]string{"protocol"},
)

// Helper functions

// RecordIntent increments the intents counter for the given owner, intent type, and operation.
func RecordIntent(owner, intentType, operation string) {
	IntentsTotal.WithLabelValues(owner, intentType, operation).Inc()
}

// RecordFRRTransaction increments the FRR transactions counter and observes the duration.
func RecordFRRTransaction(result string, duration float64) {
	FRRTransactionsTotal.WithLabelValues(result).Inc()
	FRRTransactionDuration.Observe(duration)
}

// RecordPolicyViolation increments the policy violations counter for the given owner and reason.
func RecordPolicyViolation(owner, reason string) {
	PolicyViolationsTotal.WithLabelValues(owner, reason).Inc()
}

// RecordEvent increments the events counter for the given event type.
func RecordEvent(eventType string) {
	EventsTotal.WithLabelValues(eventType).Inc()
}

// SetActivePeers sets the current number of active BGP peers for the given owner.
func SetActivePeers(owner string, count float64) {
	ActivePeers.WithLabelValues(owner).Set(count)
}

// SetActivePrefixes sets the current number of advertised prefixes for the given owner and protocol.
func SetActivePrefixes(owner, protocol string, count float64) {
	ActivePrefixes.WithLabelValues(owner, protocol).Set(count)
}

// SetActiveBFDSessions sets the current number of BFD sessions for the given owner.
func SetActiveBFDSessions(owner string, count float64) {
	ActiveBFDSessions.WithLabelValues(owner).Set(count)
}

// SetActiveOSPFInterfaces sets the current number of OSPF interfaces for the given owner.
func SetActiveOSPFInterfaces(owner string, count float64) {
	ActiveOSPFInterfaces.WithLabelValues(owner).Set(count)
}

// SetRegisteredOwners sets the current number of registered owners.
func SetRegisteredOwners(count float64) {
	RegisteredOwners.Set(count)
}

// SetFRRConnected sets the FRR connection status gauge (1 = connected, 0 = disconnected).
func SetFRRConnected(connected bool) {
	if connected {
		FRRConnected.Set(1)
	} else {
		FRRConnected.Set(0)
	}
}

// ObserveGRPCDuration observes the gRPC request duration for the given method.
func ObserveGRPCDuration(method string, duration float64) {
	GRPCRequestDuration.WithLabelValues(method).Observe(duration)
}

// RecordReconcileCycleDuration observes a reconciliation cycle duration.
func RecordReconcileCycleDuration(duration float64) {
	ReconcileCycleDuration.Observe(duration)
}

// RecordEventDropped increments the dropped events counter.
func RecordEventDropped() {
	EventsDropped.Inc()
}

// RecordMonitoringError increments the monitoring error counter for the given protocol.
func RecordMonitoringError(protocol string) {
	MonitoringErrors.WithLabelValues(protocol).Inc()
}
