// Package server implements the NovaRoute gRPC server that handles
// RouteControl RPCs. It delegates authentication to the policy engine,
// stores intents in the intent store, and triggers reconciliation.
package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"github.com/piwi3910/NovaRoute/internal/frr"
	"github.com/piwi3910/NovaRoute/internal/intent"
	"github.com/piwi3910/NovaRoute/internal/metrics"
	"github.com/piwi3910/NovaRoute/internal/policy"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReconcilerInterface is satisfied by anything that can trigger a reconciliation
// loop (i.e. translate intents into FRR configuration).
type ReconcilerInterface interface {
	TriggerReconcile()
	UpdateBGPGlobal(localAS uint32, routerID string) (prevAS uint32, prevRouterID string)
}

// Session represents a registered client session.
type Session struct {
	Owner     string
	SessionID string
	CreatedAt time.Time
}

// Server implements the v1.RouteControlServer interface, mediating between
// gRPC clients and the intent store / policy engine / reconciler.
type Server struct {
	v1.UnimplementedRouteControlServer

	intentStore *intent.Store
	policy      *policy.Engine
	reconciler  ReconcilerInterface
	logger      *zap.Logger
	eventBus    *EventBus

	// FRR client for querying real state in GetStatus.
	frrClient      *frr.Client
	frrMu          sync.RWMutex
	frrConnectedAt time.Time

	// Session tracking
	sessions   map[string]*Session // keyed by owner
	sessionsMu sync.RWMutex
}

// NewServer creates a Server with all required dependencies.
func NewServer(store *intent.Store, policyEngine *policy.Engine, reconciler ReconcilerInterface, logger *zap.Logger) *Server {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Server{
		intentStore: store,
		policy:      policyEngine,
		reconciler:  reconciler,
		logger:      logger.Named("grpc-server"),
		eventBus:    NewEventBus(),
		sessions:    make(map[string]*Session),
	}
}

// New creates a new gRPC server, registers it on the given grpc.Server, and
// returns it. This is a convenience wrapper around NewServer.
func New(gs *grpc.Server, store *intent.Store, pol *policy.Engine, rec ReconcilerInterface, logger *zap.Logger) *Server {
	s := NewServer(store, pol, rec, logger)
	v1.RegisterRouteControlServer(gs, s)
	return s
}

// EventBus returns the server's event bus so that external components (e.g.
// the FRR reconciler) can publish events.
func (s *Server) EventBus() *EventBus {
	return s.eventBus
}

// SetFRRClient sets the FRR client used by GetStatus to query real FRR state.
func (s *Server) SetFRRClient(client *frr.Client) {
	s.frrMu.Lock()
	defer s.frrMu.Unlock()
	s.frrClient = client
	s.frrConnectedAt = time.Now()
}

// ---------------------------------------------------------------------------
// Session management
// ---------------------------------------------------------------------------

// Register authenticates a client, creates or re-asserts a session, and
// returns the current prefixes and peers owned by the client.
func (s *Server) Register(ctx context.Context, req *v1.RegisterRequest) (*v1.RegisterResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("Register", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Create or refresh session.
	sessionID := fmt.Sprintf("%s-%d", owner, time.Now().UnixNano())
	s.sessionsMu.Lock()
	s.sessions[owner] = &Session{
		Owner:     owner,
		SessionID: sessionID,
		CreatedAt: time.Now(),
	}
	sessionCount := len(s.sessions)
	s.sessionsMu.Unlock()

	metrics.SetRegisteredOwners(float64(sessionCount))

	// Gather current state for this owner.
	currentPrefixes := s.intentStore.GetOwnerPrefixes(owner)
	currentPeers := s.intentStore.GetOwnerPeers(owner)

	s.logger.Info("owner registered",
		zap.String("owner", owner),
		zap.String("session_id", sessionID),
		zap.Bool("reassert", req.GetReassertIntents()),
		zap.Int("existing_prefixes", len(currentPrefixes)),
		zap.Int("existing_peers", len(currentPeers)),
	)

	// Publish registration event.
	s.eventBus.Publish(&v1.RouteEvent{
		Type:          v1.EventType_EVENT_TYPE_OWNER_REGISTERED,
		Detail:        fmt.Sprintf("owner %s registered (session %s)", owner, sessionID),
		TimestampUnix: time.Now().Unix(),
		Owner:         owner,
		Metadata: map[string]string{
			"session_id": sessionID,
		},
	})
	metrics.RecordEvent("owner_registered")

	// If reassert is requested and the owner already has intents, trigger a
	// reconcile so that FRR re-applies them.
	if req.GetReassertIntents() && (len(currentPrefixes) > 0 || len(currentPeers) > 0) {
		s.reconciler.TriggerReconcile()
	}

	return &v1.RegisterResponse{
		SessionId:       sessionID,
		CurrentPrefixes: currentPrefixes,
		CurrentPeers:    currentPeers,
	}, nil
}

// Deregister removes the owner's session and, if requested, all of the
// owner's intents.
func (s *Server) Deregister(ctx context.Context, req *v1.DeregisterRequest) (*v1.DeregisterResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("Deregister", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Withdraw all intents if requested.
	if req.GetWithdrawAll() {
		s.intentStore.RemoveAllByOwner(owner)
		s.reconciler.TriggerReconcile()
		s.logger.Info("withdrew all intents for owner", zap.String("owner", owner))
	}

	// Remove session.
	s.sessionsMu.Lock()
	delete(s.sessions, owner)
	sessionCount := len(s.sessions)
	s.sessionsMu.Unlock()

	metrics.SetRegisteredOwners(float64(sessionCount))

	s.logger.Info("owner deregistered",
		zap.String("owner", owner),
		zap.Bool("withdraw_all", req.GetWithdrawAll()),
	)

	// Publish deregistration event.
	s.eventBus.Publish(&v1.RouteEvent{
		Type:          v1.EventType_EVENT_TYPE_OWNER_DEREGISTERED,
		Detail:        fmt.Sprintf("owner %s deregistered (withdraw_all=%v)", owner, req.GetWithdrawAll()),
		TimestampUnix: time.Now().Unix(),
		Owner:         owner,
	})
	metrics.RecordEvent("owner_deregistered")

	return &v1.DeregisterResponse{}, nil
}

// ---------------------------------------------------------------------------
// BGP Global Configuration
// ---------------------------------------------------------------------------

// ConfigureBGP dynamically updates the local BGP AS number and router-id.
// This allows clients like NovaEdge to configure per-node BGP settings at
// runtime without requiring env vars or config file changes.
func (s *Server) ConfigureBGP(ctx context.Context, req *v1.ConfigureBGPRequest) (*v1.ConfigureBGPResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("ConfigureBGP", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if req.GetLocalAs() == 0 {
		return nil, status.Error(codes.InvalidArgument, "local_as must not be zero")
	}
	if req.GetRouterId() == "" {
		return nil, status.Error(codes.InvalidArgument, "router_id must not be empty")
	}

	// Validate router_id is a valid unicast IPv4 address.
	routerIP := net.ParseIP(req.GetRouterId())
	if routerIP == nil || routerIP.To4() == nil {
		return nil, status.Error(codes.InvalidArgument, "router_id must be a valid IPv4 address")
	}
	if routerIP.IsMulticast() || routerIP.IsUnspecified() {
		return nil, status.Errorf(codes.InvalidArgument, "router_id must be a unicast IPv4 address, got %s", req.GetRouterId())
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Update BGP global config in the reconciler.
	prevAS, prevRouterID := s.reconciler.UpdateBGPGlobal(req.GetLocalAs(), req.GetRouterId())

	// Trigger reconciliation to apply the change.
	s.reconciler.TriggerReconcile()

	// Publish BGP configured event.
	s.eventBus.Publish(&v1.RouteEvent{
		Type:          v1.EventType_EVENT_TYPE_BGP_CONFIG_CHANGED,
		Detail:        fmt.Sprintf("BGP global config updated: AS=%d router_id=%s (was AS=%d router_id=%s)", req.GetLocalAs(), req.GetRouterId(), prevAS, prevRouterID),
		TimestampUnix: time.Now().Unix(),
		Owner:         owner,
		Metadata: map[string]string{
			"local_as":           fmt.Sprintf("%d", req.GetLocalAs()),
			"router_id":          req.GetRouterId(),
			"previous_as":        fmt.Sprintf("%d", prevAS),
			"previous_router_id": prevRouterID,
		},
	})
	metrics.RecordEvent("bgp_configured")

	s.logger.Info("BGP global config updated via RPC",
		zap.String("owner", owner),
		zap.Uint32("local_as", req.GetLocalAs()),
		zap.String("router_id", req.GetRouterId()),
		zap.Uint32("previous_as", prevAS),
		zap.String("previous_router_id", prevRouterID),
	)

	return &v1.ConfigureBGPResponse{
		PreviousAs:       prevAS,
		PreviousRouterId: prevRouterID,
	}, nil
}

// ---------------------------------------------------------------------------
// Peer management
// ---------------------------------------------------------------------------

// ApplyPeer creates or updates a BGP peer intent for the calling owner.
func (s *Server) ApplyPeer(ctx context.Context, req *v1.ApplyPeerRequest) (*v1.ApplyPeerResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("ApplyPeer", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()
	peer := req.GetPeer()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if peer == nil {
		return nil, status.Error(codes.InvalidArgument, "peer must not be nil")
	}
	if peer.GetNeighborAddress() == "" {
		return nil, status.Error(codes.InvalidArgument, "peer neighbor_address must not be empty")
	}
	if net.ParseIP(peer.GetNeighborAddress()) == nil {
		return nil, status.Errorf(codes.InvalidArgument, "peer neighbor_address is not a valid IP address: %s", peer.GetNeighborAddress())
	}
	if peer.GetSourceAddress() != "" && net.ParseIP(peer.GetSourceAddress()) == nil {
		return nil, status.Errorf(codes.InvalidArgument, "peer source_address is not a valid IP address: %s", peer.GetSourceAddress())
	}
	if (peer.GetKeepalive() > 0) != (peer.GetHoldTime() > 0) {
		return nil, status.Error(codes.InvalidArgument, "keepalive and hold_time must both be set or both be zero")
	}
	if peer.GetHoldTime() > 0 && peer.GetHoldTime() < 3 {
		return nil, status.Errorf(codes.InvalidArgument, "hold_time must be at least 3 seconds, got %d", peer.GetHoldTime())
	}
	if peer.GetKeepalive() > 0 && peer.GetHoldTime() < 3*peer.GetKeepalive() {
		return nil, status.Errorf(codes.InvalidArgument, "hold_time (%d) must be at least 3x keepalive (%d) per RFC 4271", peer.GetHoldTime(), peer.GetKeepalive())
	}
	if peer.GetRemoteAs() == 0 {
		return nil, status.Error(codes.InvalidArgument, "peer remote_as must not be zero")
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Validate peer operation policy.
	if err := s.policy.ValidatePeerOperation(owner); err != nil {
		metrics.RecordPolicyViolation(owner, "peer_operation_denied")
		s.eventBus.Publish(&v1.RouteEvent{
			Type:          v1.EventType_EVENT_TYPE_POLICY_VIOLATION,
			Detail:        fmt.Sprintf("peer operation denied for owner %s: %v", owner, err),
			TimestampUnix: time.Now().Unix(),
			Owner:         owner,
			Metadata: map[string]string{
				"operation": "apply_peer",
				"neighbor":  peer.GetNeighborAddress(),
			},
		})
		metrics.RecordEvent("policy_violation")
		return nil, status.Errorf(codes.PermissionDenied, "peer operation denied: %v", err)
	}

	// Build the intent.
	peerIntent := &intent.PeerIntent{
		Owner:               owner,
		NeighborAddress:     peer.GetNeighborAddress(),
		RemoteAS:            peer.GetRemoteAs(),
		PeerType:            peer.GetPeerType(),
		Keepalive:           peer.GetKeepalive(),
		HoldTime:            peer.GetHoldTime(),
		BFDEnabled:          peer.GetBfdEnabled(),
		BFDMinRxMs:          peer.GetBfdMinRxMs(),
		BFDMinTxMs:          peer.GetBfdMinTxMs(),
		BFDDetectMultiplier: peer.GetBfdDetectMultiplier(),
		Description:         peer.GetDescription(),
		AddressFamilies:     peer.GetAddressFamilies(),
		SourceAddress:       peer.GetSourceAddress(),
		EBGPMultihop:        peer.GetEbgpMultihop(),
		Password:            peer.GetPassword(),
	}

	// Use client-specified max-prefix, default to 1000 if not set.
	peerIntent.MaxPrefixes = peer.GetMaxPrefix()
	if peerIntent.MaxPrefixes == 0 {
		peerIntent.MaxPrefixes = 1000
	}

	// Default BFD parameters when BFD is enabled and params are unset.
	if peerIntent.BFDEnabled {
		if peerIntent.BFDMinRxMs == 0 {
			peerIntent.BFDMinRxMs = 300
		}
		if peerIntent.BFDMinTxMs == 0 {
			peerIntent.BFDMinTxMs = 300
		}
		if peerIntent.BFDDetectMultiplier == 0 {
			peerIntent.BFDDetectMultiplier = 3
		}
	}

	// Default to ipv4-unicast if no address families specified.
	if len(peerIntent.AddressFamilies) == 0 {
		peerIntent.AddressFamilies = []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST}
	}

	// Store intent.
	if err := s.intentStore.SetPeerIntent(owner, peerIntent); err != nil {
		s.logger.Error("failed to set peer intent",
			zap.String("owner", owner),
			zap.String("neighbor", peer.GetNeighborAddress()),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.Internal, "failed to store peer intent: %v", err)
	}

	metrics.RecordIntent(owner, "peer", "set")
	s.updateOwnerPeerGauge(owner)

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	metrics.RecordEvent("peer_applied")

	s.logger.Info("peer intent applied",
		zap.String("owner", owner),
		zap.String("neighbor", peer.GetNeighborAddress()),
		zap.Uint32("remote_as", peer.GetRemoteAs()),
	)

	return &v1.ApplyPeerResponse{}, nil
}

// RemovePeer removes a BGP peer intent for the calling owner.
func (s *Server) RemovePeer(ctx context.Context, req *v1.RemovePeerRequest) (*v1.RemovePeerResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("RemovePeer", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()
	neighborAddr := req.GetNeighborAddress()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if neighborAddr == "" {
		return nil, status.Error(codes.InvalidArgument, "neighbor_address must not be empty")
	}
	if net.ParseIP(neighborAddr) == nil {
		return nil, status.Errorf(codes.InvalidArgument, "neighbor_address is not a valid IP address: %s", neighborAddr)
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Validate peer operation policy.
	if err := s.policy.ValidatePeerOperation(owner); err != nil {
		metrics.RecordPolicyViolation(owner, "peer_operation_denied")
		return nil, status.Errorf(codes.PermissionDenied, "peer operation denied: %v", err)
	}

	// Remove intent.
	if err := s.intentStore.RemovePeerIntent(owner, neighborAddr); err != nil {
		s.logger.Error("failed to remove peer intent",
			zap.String("owner", owner),
			zap.String("neighbor", neighborAddr),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.NotFound, "failed to remove peer intent: %v", err)
	}

	metrics.RecordIntent(owner, "peer", "remove")
	s.updateOwnerPeerGauge(owner)

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	metrics.RecordEvent("peer_removed")

	s.logger.Info("peer intent removed",
		zap.String("owner", owner),
		zap.String("neighbor", neighborAddr),
	)

	return &v1.RemovePeerResponse{}, nil
}

// ---------------------------------------------------------------------------
// Prefix advertisement
// ---------------------------------------------------------------------------

// AdvertisePrefix creates or updates a prefix advertisement intent. It validates
// the prefix against the owner's policy and checks for conflicts with other
// owners.
func (s *Server) AdvertisePrefix(ctx context.Context, req *v1.AdvertisePrefixRequest) (*v1.AdvertisePrefixResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("AdvertisePrefix", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()
	prefix := req.GetPrefix()
	protocol := req.GetProtocol()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if prefix == "" {
		return nil, status.Error(codes.InvalidArgument, "prefix must not be empty")
	}
	if _, _, err := net.ParseCIDR(prefix); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "prefix is not a valid CIDR: %s", prefix)
	}
	if protocol == v1.Protocol_PROTOCOL_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "protocol must be specified")
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Validate prefix policy (type + CIDR restrictions).
	if err := s.policy.ValidatePrefix(owner, prefix); err != nil {
		metrics.RecordPolicyViolation(owner, "prefix_policy")
		s.eventBus.Publish(&v1.RouteEvent{
			Type:          v1.EventType_EVENT_TYPE_POLICY_VIOLATION,
			Detail:        fmt.Sprintf("prefix policy violation: %v", err),
			TimestampUnix: time.Now().Unix(),
			Owner:         owner,
			Metadata: map[string]string{
				"prefix":   prefix,
				"protocol": protocol.String(),
			},
		})
		metrics.RecordEvent("policy_violation")
		return nil, status.Errorf(codes.PermissionDenied, "prefix policy check failed: %v", err)
	}

	// Check for ownership conflicts across all owners.
	protocolStr := protocolString(protocol)
	existingOwner := s.findPrefixOwner(prefix, protocolStr)
	if err := s.policy.CheckConflict(owner, prefix, protocolStr, existingOwner); err != nil {
		metrics.RecordPolicyViolation(owner, "prefix_conflict")
		s.eventBus.Publish(&v1.RouteEvent{
			Type:          v1.EventType_EVENT_TYPE_POLICY_VIOLATION,
			Detail:        fmt.Sprintf("prefix conflict: %v", err),
			TimestampUnix: time.Now().Unix(),
			Owner:         owner,
			Metadata: map[string]string{
				"prefix":         prefix,
				"protocol":       protocol.String(),
				"existing_owner": existingOwner,
			},
		})
		metrics.RecordEvent("policy_violation")
		return nil, status.Errorf(codes.AlreadyExists, "prefix conflict: %v", err)
	}

	// Build the intent.
	prefixIntent := &intent.PrefixIntent{
		Owner:    owner,
		Prefix:   prefix,
		Protocol: protocol,
	}
	if attrs := req.GetAttributes(); attrs != nil {
		// Reject BGP-only attributes on OSPF prefixes.
		if protocol == v1.Protocol_PROTOCOL_OSPF {
			if attrs.GetLocalPreference() > 0 || len(attrs.GetCommunities()) > 0 || attrs.GetMed() > 0 || attrs.GetNextHop() != "" {
				return nil, status.Error(codes.InvalidArgument, "prefix attributes (local_preference, communities, med, next_hop) are not supported for OSPF protocol")
			}
		}
		// Validate next_hop is a valid IP when set.
		if attrs.GetNextHop() != "" && net.ParseIP(attrs.GetNextHop()) == nil {
			return nil, status.Errorf(codes.InvalidArgument, "prefix attributes next_hop is not a valid IP address: %s", attrs.GetNextHop())
		}
		prefixIntent.LocalPreference = attrs.GetLocalPreference()
		prefixIntent.Communities = attrs.GetCommunities()
		prefixIntent.MED = attrs.GetMed()
		prefixIntent.NextHop = attrs.GetNextHop()
	}

	// Store intent.
	if err := s.intentStore.SetPrefixIntent(owner, prefixIntent); err != nil {
		s.logger.Error("failed to set prefix intent",
			zap.String("owner", owner),
			zap.String("prefix", prefix),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.Internal, "failed to store prefix intent: %v", err)
	}

	metrics.RecordIntent(owner, "prefix", "set")
	s.updateOwnerPrefixGauge(owner)

	// Publish prefix advertised event.
	s.eventBus.Publish(&v1.RouteEvent{
		Type:          v1.EventType_EVENT_TYPE_PREFIX_ADVERTISED,
		Detail:        fmt.Sprintf("prefix %s advertised via %s by %s", prefix, protocolStr, owner),
		TimestampUnix: time.Now().Unix(),
		Owner:         owner,
		Metadata: map[string]string{
			"prefix":   prefix,
			"protocol": protocolStr,
		},
	})
	metrics.RecordEvent("prefix_advertised")

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	s.logger.Info("prefix intent applied",
		zap.String("owner", owner),
		zap.String("prefix", prefix),
		zap.String("protocol", protocolStr),
	)

	return &v1.AdvertisePrefixResponse{}, nil
}

// WithdrawPrefix removes a prefix advertisement intent for the calling owner.
func (s *Server) WithdrawPrefix(ctx context.Context, req *v1.WithdrawPrefixRequest) (*v1.WithdrawPrefixResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("WithdrawPrefix", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()
	prefix := req.GetPrefix()
	protocol := req.GetProtocol()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if prefix == "" {
		return nil, status.Error(codes.InvalidArgument, "prefix must not be empty")
	}
	if _, _, err := net.ParseCIDR(prefix); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "prefix is not a valid CIDR: %s", prefix)
	}
	if protocol == v1.Protocol_PROTOCOL_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "protocol must be specified")
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	protocolStr := protocolString(protocol)

	// Remove intent.
	if err := s.intentStore.RemovePrefixIntent(owner, prefix, protocolStr); err != nil {
		s.logger.Error("failed to remove prefix intent",
			zap.String("owner", owner),
			zap.String("prefix", prefix),
			zap.String("protocol", protocolStr),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.NotFound, "failed to remove prefix intent: %v", err)
	}

	metrics.RecordIntent(owner, "prefix", "remove")
	s.updateOwnerPrefixGauge(owner)

	// Publish prefix withdrawn event.
	s.eventBus.Publish(&v1.RouteEvent{
		Type:          v1.EventType_EVENT_TYPE_PREFIX_WITHDRAWN,
		Detail:        fmt.Sprintf("prefix %s withdrawn from %s by %s", prefix, protocolStr, owner),
		TimestampUnix: time.Now().Unix(),
		Owner:         owner,
		Metadata: map[string]string{
			"prefix":   prefix,
			"protocol": protocolStr,
		},
	})
	metrics.RecordEvent("prefix_withdrawn")

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	s.logger.Info("prefix intent withdrawn",
		zap.String("owner", owner),
		zap.String("prefix", prefix),
		zap.String("protocol", protocolStr),
	)

	return &v1.WithdrawPrefixResponse{}, nil
}

// ---------------------------------------------------------------------------
// BFD
// ---------------------------------------------------------------------------

// EnableBFD creates or updates a BFD session intent for the calling owner.
func (s *Server) EnableBFD(ctx context.Context, req *v1.EnableBFDRequest) (*v1.EnableBFDResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("EnableBFD", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if req.GetPeerAddress() == "" {
		return nil, status.Error(codes.InvalidArgument, "peer_address must not be empty")
	}
	if net.ParseIP(req.GetPeerAddress()) == nil {
		return nil, status.Errorf(codes.InvalidArgument, "peer_address is not a valid IP address: %s", req.GetPeerAddress())
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Validate BFD operation policy.
	if err := s.policy.ValidateBFDOperation(owner); err != nil {
		metrics.RecordPolicyViolation(owner, "bfd_operation_denied")
		s.eventBus.Publish(&v1.RouteEvent{
			Type:          v1.EventType_EVENT_TYPE_POLICY_VIOLATION,
			Detail:        fmt.Sprintf("BFD operation denied for owner %s: %v", owner, err),
			TimestampUnix: time.Now().Unix(),
			Owner:         owner,
			Metadata: map[string]string{
				"operation":    "enable_bfd",
				"peer_address": req.GetPeerAddress(),
			},
		})
		metrics.RecordEvent("policy_violation")
		return nil, status.Errorf(codes.PermissionDenied, "BFD operation denied: %v", err)
	}

	// Build the intent with sensible defaults.
	bfdIntent := &intent.BFDIntent{
		Owner:            owner,
		PeerAddress:      req.GetPeerAddress(),
		MinRxMs:          req.GetMinRxMs(),
		MinTxMs:          req.GetMinTxMs(),
		DetectMultiplier: req.GetDetectMultiplier(),
		InterfaceName:    req.GetInterfaceName(),
	}
	if bfdIntent.MinRxMs == 0 {
		bfdIntent.MinRxMs = 300
	}
	if bfdIntent.MinTxMs == 0 {
		bfdIntent.MinTxMs = 300
	}
	if bfdIntent.DetectMultiplier == 0 {
		bfdIntent.DetectMultiplier = 3
	}

	// Store intent.
	if err := s.intentStore.SetBFDIntent(owner, bfdIntent); err != nil {
		s.logger.Error("failed to set BFD intent",
			zap.String("owner", owner),
			zap.String("peer", req.GetPeerAddress()),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.Internal, "failed to store BFD intent: %v", err)
	}

	metrics.RecordIntent(owner, "bfd", "set")
	s.updateOwnerBFDGauge(owner)

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	metrics.RecordEvent("bfd_enabled")

	s.logger.Info("BFD intent applied",
		zap.String("owner", owner),
		zap.String("peer", req.GetPeerAddress()),
		zap.Uint32("min_rx_ms", bfdIntent.MinRxMs),
		zap.Uint32("min_tx_ms", bfdIntent.MinTxMs),
		zap.Uint32("detect_mult", bfdIntent.DetectMultiplier),
	)

	return &v1.EnableBFDResponse{}, nil
}

// DisableBFD removes a BFD session intent for the calling owner.
func (s *Server) DisableBFD(ctx context.Context, req *v1.DisableBFDRequest) (*v1.DisableBFDResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("DisableBFD", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()
	peerAddr := req.GetPeerAddress()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if peerAddr == "" {
		return nil, status.Error(codes.InvalidArgument, "peer_address must not be empty")
	}
	if net.ParseIP(peerAddr) == nil {
		return nil, status.Errorf(codes.InvalidArgument, "peer_address is not a valid IP address: %s", peerAddr)
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Validate BFD operation policy.
	if err := s.policy.ValidateBFDOperation(owner); err != nil {
		metrics.RecordPolicyViolation(owner, "bfd_operation_denied")
		return nil, status.Errorf(codes.PermissionDenied, "BFD operation denied: %v", err)
	}

	// Remove intent.
	if err := s.intentStore.RemoveBFDIntent(owner, peerAddr); err != nil {
		s.logger.Error("failed to remove BFD intent",
			zap.String("owner", owner),
			zap.String("peer", peerAddr),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.NotFound, "failed to remove BFD intent: %v", err)
	}

	metrics.RecordIntent(owner, "bfd", "remove")
	s.updateOwnerBFDGauge(owner)

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	metrics.RecordEvent("bfd_disabled")

	s.logger.Info("BFD intent disabled",
		zap.String("owner", owner),
		zap.String("peer", peerAddr),
	)

	return &v1.DisableBFDResponse{}, nil
}

// ---------------------------------------------------------------------------
// OSPF
// ---------------------------------------------------------------------------

// EnableOSPF creates or updates an OSPF interface intent for the calling owner.
func (s *Server) EnableOSPF(ctx context.Context, req *v1.EnableOSPFRequest) (*v1.EnableOSPFResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("EnableOSPF", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if req.GetInterfaceName() == "" {
		return nil, status.Error(codes.InvalidArgument, "interface_name must not be empty")
	}
	if req.GetAreaId() == "" {
		return nil, status.Error(codes.InvalidArgument, "area_id must not be empty")
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Validate OSPF operation policy.
	if err := s.policy.ValidateOSPFOperation(owner); err != nil {
		metrics.RecordPolicyViolation(owner, "ospf_operation_denied")
		s.eventBus.Publish(&v1.RouteEvent{
			Type:          v1.EventType_EVENT_TYPE_POLICY_VIOLATION,
			Detail:        fmt.Sprintf("OSPF operation denied for owner %s: %v", owner, err),
			TimestampUnix: time.Now().Unix(),
			Owner:         owner,
			Metadata: map[string]string{
				"operation":      "enable_ospf",
				"interface_name": req.GetInterfaceName(),
			},
		})
		metrics.RecordEvent("policy_violation")
		return nil, status.Errorf(codes.PermissionDenied, "OSPF operation denied: %v", err)
	}

	// Build the intent with sensible defaults.
	ospfIntent := &intent.OSPFIntent{
		Owner:         owner,
		InterfaceName: req.GetInterfaceName(),
		AreaID:        req.GetAreaId(),
		Passive:       req.GetPassive(),
		Cost:          req.GetCost(),
		HelloInterval: req.GetHelloInterval(),
		DeadInterval:  req.GetDeadInterval(),
	}
	if ospfIntent.Cost > 65535 {
		return nil, status.Errorf(codes.InvalidArgument, "OSPF cost must be between 1 and 65535, got %d", ospfIntent.Cost)
	}
	if ospfIntent.Cost == 0 {
		ospfIntent.Cost = 10
	}
	if ospfIntent.HelloInterval == 0 {
		ospfIntent.HelloInterval = 10
	}
	if ospfIntent.DeadInterval == 0 {
		ospfIntent.DeadInterval = 40
	}
	if ospfIntent.DeadInterval > 0 && ospfIntent.HelloInterval > 0 && ospfIntent.DeadInterval < ospfIntent.HelloInterval {
		return nil, status.Errorf(codes.InvalidArgument, "dead_interval (%d) must be >= hello_interval (%d)", ospfIntent.DeadInterval, ospfIntent.HelloInterval)
	}

	// Store intent.
	if err := s.intentStore.SetOSPFIntent(owner, ospfIntent); err != nil {
		s.logger.Error("failed to set OSPF intent",
			zap.String("owner", owner),
			zap.String("interface", req.GetInterfaceName()),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.Internal, "failed to store OSPF intent: %v", err)
	}

	metrics.RecordIntent(owner, "ospf", "set")
	s.updateOwnerOSPFGauge(owner)

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	metrics.RecordEvent("ospf_enabled")

	s.logger.Info("OSPF intent applied",
		zap.String("owner", owner),
		zap.String("interface", req.GetInterfaceName()),
		zap.String("area", req.GetAreaId()),
	)

	return &v1.EnableOSPFResponse{}, nil
}

// DisableOSPF removes an OSPF interface intent for the calling owner.
func (s *Server) DisableOSPF(ctx context.Context, req *v1.DisableOSPFRequest) (*v1.DisableOSPFResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("DisableOSPF", time.Since(start).Seconds()) }()

	owner := req.GetOwner()
	token := req.GetToken()
	ifaceName := req.GetInterfaceName()

	if owner == "" {
		return nil, status.Error(codes.InvalidArgument, "owner must not be empty")
	}
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "token must not be empty")
	}
	if ifaceName == "" {
		return nil, status.Error(codes.InvalidArgument, "interface_name must not be empty")
	}

	// Validate token.
	if err := s.policy.ValidateToken(owner, token); err != nil {
		metrics.RecordPolicyViolation(owner, "invalid_token")
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
	}

	// Validate OSPF operation policy.
	if err := s.policy.ValidateOSPFOperation(owner); err != nil {
		metrics.RecordPolicyViolation(owner, "ospf_operation_denied")
		return nil, status.Errorf(codes.PermissionDenied, "OSPF operation denied: %v", err)
	}

	// Remove intent.
	if err := s.intentStore.RemoveOSPFIntent(owner, ifaceName); err != nil {
		s.logger.Error("failed to remove OSPF intent",
			zap.String("owner", owner),
			zap.String("interface", ifaceName),
			zap.Error(err),
		)
		return nil, status.Errorf(codes.NotFound, "failed to remove OSPF intent: %v", err)
	}

	metrics.RecordIntent(owner, "ospf", "remove")
	s.updateOwnerOSPFGauge(owner)

	// Trigger reconciliation.
	s.reconciler.TriggerReconcile()

	metrics.RecordEvent("ospf_disabled")

	s.logger.Info("OSPF intent disabled",
		zap.String("owner", owner),
		zap.String("interface", ifaceName),
	)

	return &v1.DisableOSPFResponse{}, nil
}

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// GetStatus returns an aggregated view of all routing intents, optionally
// filtered by owner. When the FRR client is available, it enriches peer,
// BFD, and OSPF status with real state from FRR show commands.
func (s *Server) GetStatus(ctx context.Context, req *v1.GetStatusRequest) (*v1.GetStatusResponse, error) {
	start := time.Now()
	defer func() { metrics.ObserveGRPCDuration("GetStatus", time.Since(start).Seconds()) }()

	ownerFilter := req.GetOwnerFilter()
	resp := &v1.GetStatusResponse{}

	// Gather intents, optionally filtered by owner.
	var allIntents map[string]*intent.OwnerIntents
	if ownerFilter != "" {
		oi := s.intentStore.GetOwnerIntents(ownerFilter)
		if oi != nil {
			allIntents = map[string]*intent.OwnerIntents{ownerFilter: oi}
		} else {
			allIntents = make(map[string]*intent.OwnerIntents)
		}
	} else {
		allIntents = s.intentStore.GetAllIntents()
	}

	// Snapshot the FRR client under the read lock so we avoid data races.
	s.frrMu.RLock()
	frrClient := s.frrClient
	frrConnectedAt := s.frrConnectedAt
	s.frrMu.RUnlock()

	// Query real FRR state if the client is available.
	var bgpStates map[string]*frr.BGPNeighborState
	var bfdStates map[string]*frr.BFDPeerState
	var ospfStates map[string]*frr.OSPFNeighborState

	if frrClient != nil {
		if neighbors, err := frrClient.GetBGPNeighbors(ctx); err == nil {
			bgpStates = make(map[string]*frr.BGPNeighborState, len(neighbors))
			for i := range neighbors {
				bgpStates[neighbors[i].Address] = &neighbors[i]
			}
		} else {
			s.logger.Warn("GetStatus: failed to query BGP neighbors from FRR", zap.Error(err))
		}
		if peers, err := frrClient.GetBFDPeers(ctx); err == nil {
			bfdStates = make(map[string]*frr.BFDPeerState, len(peers))
			for i := range peers {
				bfdStates[peers[i].PeerAddress] = &peers[i]
			}
		} else {
			s.logger.Warn("GetStatus: failed to query BFD peers from FRR", zap.Error(err))
		}
		if neighbors, err := frrClient.GetOSPFNeighbors(ctx); err == nil {
			ospfStates = make(map[string]*frr.OSPFNeighborState, len(neighbors))
			for i := range neighbors {
				ospfStates[neighbors[i].NeighborID] = &neighbors[i]
			}
		} else {
			s.logger.Warn("GetStatus: failed to query OSPF neighbors from FRR", zap.Error(err))
		}
	}

	// Build peer status list, merging intent config with real FRR state.
	for owner, oi := range allIntents {
		for _, p := range oi.Peers {
			ps := &v1.PeerStatus{
				NeighborAddress: p.NeighborAddress,
				RemoteAs:        p.RemoteAS,
				Owner:           owner,
				BfdEnabled:      p.BFDEnabled,
				Description:     p.Description,
			}
			if frrClient != nil {
				ps.State = "configured" // Intent exists but peer not in FRR yet
			} else {
				ps.State = "pending" // FRR not connected, state unknown
			}
			if bgp, ok := bgpStates[p.NeighborAddress]; ok {
				ps.State = bgp.State
				ps.Uptime = bgp.UpTime
				ps.PrefixesReceived = bgp.PrefixesReceived
				ps.PrefixesSent = bgp.PrefixesSent
			}
			// Enrich with BFD status if available.
			if p.BFDEnabled {
				if bfd, ok := bfdStates[p.NeighborAddress]; ok {
					ps.BfdStatus = bfd.Status
				} else {
					ps.BfdStatus = "not found"
				}
			} else {
				ps.BfdStatus = "disabled"
			}
			resp.Peers = append(resp.Peers, ps)
		}
	}

	// Build prefix status list.
	for owner, oi := range allIntents {
		for _, p := range oi.Prefixes {
			resp.Prefixes = append(resp.Prefixes, &v1.PrefixStatus{
				Prefix:   p.Prefix,
				Protocol: p.Protocol,
				Owner:    owner,
				State:    "advertised",
			})
		}
	}

	// Build BFD session status list, merging with real FRR state.
	// Track which addresses are covered to avoid duplicates.
	bfdCovered := make(map[string]bool)
	for owner, oi := range allIntents {
		for _, b := range oi.BFD {
			bs := &v1.BFDSessionStatus{
				PeerAddress:      b.PeerAddress,
				Owner:            owner,
				MinRxMs:          b.MinRxMs,
				MinTxMs:          b.MinTxMs,
				DetectMultiplier: b.DetectMultiplier,
			}
			if frrClient != nil {
				bs.State = "configured"
			} else {
				bs.State = "pending"
			}
			if bfd, ok := bfdStates[b.PeerAddress]; ok {
				bs.State = bfd.Status
				bs.Uptime = bfd.Uptime
			}
			resp.BfdSessions = append(resp.BfdSessions, bs)
			bfdCovered[b.PeerAddress] = true
		}
	}
	// Include auto-linked BFD sessions from peers with BFDEnabled=true.
	for owner, oi := range allIntents {
		for _, p := range oi.Peers {
			if !p.BFDEnabled || bfdCovered[p.NeighborAddress] {
				continue
			}
			bs := &v1.BFDSessionStatus{
				PeerAddress:      p.NeighborAddress,
				Owner:            owner,
				MinRxMs:          p.BFDMinRxMs,
				MinTxMs:          p.BFDMinTxMs,
				DetectMultiplier: p.BFDDetectMultiplier,
			}
			if frrClient != nil {
				bs.State = "configured"
			} else {
				bs.State = "pending"
			}
			if bfd, ok := bfdStates[p.NeighborAddress]; ok {
				bs.State = bfd.Status
				bs.Uptime = bfd.Uptime
			}
			resp.BfdSessions = append(resp.BfdSessions, bs)
			bfdCovered[p.NeighborAddress] = true
		}
	}

	// Build OSPF interface status list, merging with real FRR state.
	for owner, oi := range allIntents {
		for _, o := range oi.OSPF {
			os := &v1.OSPFInterfaceStatus{
				InterfaceName: o.InterfaceName,
				AreaId:        o.AreaID,
				Owner:         owner,
				Cost:          o.Cost,
			}
			if frrClient != nil {
				os.State = "configured" // Intent exists but not in FRR yet
			} else {
				os.State = "pending" // FRR not connected, state unknown
			}
			// Count OSPF neighbors on this interface and get state.
			nbrCount := uint32(0)
			for _, nbr := range ospfStates {
				if nbr.Interface == o.InterfaceName {
					if os.State == "configured" {
						os.State = nbr.State
					}
					nbrCount++
				}
			}
			os.NeighborCount = nbrCount
			resp.OspfInterfaces = append(resp.OspfInterfaces, os)
		}
	}

	// Build real FRR status.
	resp.FrrStatus = &v1.FRRStatus{
		Version:   "unknown",
		Connected: false,
	}
	if frrClient != nil {
		resp.FrrStatus.Connected = frrClient.IsReady()
		if resp.FrrStatus.Connected {
			if version, err := frrClient.GetVersion(ctx); err == nil {
				resp.FrrStatus.Version = version
			} else {
				s.logger.Warn("GetStatus: failed to query FRR version", zap.Error(err))
			}
			resp.FrrStatus.Uptime = time.Since(frrConnectedAt).Truncate(time.Second).String()
		}
	}

	return resp, nil
}

// StreamEvents implements the server-streaming RPC that pushes RouteEvents
// to the client. The stream stays open until the client cancels or disconnects.
func (s *Server) StreamEvents(req *v1.StreamEventsRequest, stream grpc.ServerStreamingServer[v1.RouteEvent]) error {
	ownerFilter := req.GetOwnerFilter()
	eventTypes := req.GetEventTypes()

	ch := s.eventBus.Subscribe(ownerFilter, eventTypes)
	defer s.eventBus.Unsubscribe(ch)

	s.logger.Info("event stream started",
		zap.String("owner_filter", ownerFilter),
		zap.Strings("event_types", eventTypes),
	)

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				// Channel closed (unsubscribed).
				return nil
			}
			if err := stream.Send(event); err != nil {
				s.logger.Debug("event stream send failed",
					zap.Error(err),
				)
				return err
			}
		case <-stream.Context().Done():
			s.logger.Info("event stream ended (client disconnected)",
				zap.String("owner_filter", ownerFilter),
			)
			return stream.Context().Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// protocolString converts a protocol enum to a lowercase string key.
// NOTE: This helper is duplicated across intent, reconciler, and server packages.
func protocolString(p v1.Protocol) string {
	switch p {
	case v1.Protocol_PROTOCOL_BGP:
		return "bgp"
	case v1.Protocol_PROTOCOL_OSPF:
		return "ospf"
	default:
		return strings.ToLower(p.String())
	}
}

// findPrefixOwner searches all owners in the intent store for a prefix+protocol
// combination and returns the owning owner name, or "" if not found.
func (s *Server) findPrefixOwner(prefix, protocol string) string {
	allIntents := s.intentStore.GetAllIntents()
	for owner, oi := range allIntents {
		for _, p := range oi.Prefixes {
			if p.Prefix == prefix && strings.EqualFold(protocolString(p.Protocol), protocol) {
				return owner
			}
		}
	}
	return ""
}

// updateOwnerPeerGauge refreshes the active peers gauge for a given owner.
func (s *Server) updateOwnerPeerGauge(owner string) {
	oi := s.intentStore.GetOwnerIntents(owner)
	if oi == nil {
		metrics.SetActivePeers(owner, 0)
		return
	}
	metrics.SetActivePeers(owner, float64(len(oi.Peers)))
}

// updateOwnerPrefixGauge refreshes the active prefixes gauge for a given owner.
func (s *Server) updateOwnerPrefixGauge(owner string) {
	oi := s.intentStore.GetOwnerIntents(owner)
	if oi == nil {
		metrics.SetActivePrefixes(owner, "all", 0)
		return
	}
	// Count by protocol.
	bgpCount := 0
	ospfCount := 0
	for _, p := range oi.Prefixes {
		switch p.Protocol {
		case v1.Protocol_PROTOCOL_BGP:
			bgpCount++
		case v1.Protocol_PROTOCOL_OSPF:
			ospfCount++
		}
	}
	metrics.SetActivePrefixes(owner, "bgp", float64(bgpCount))
	metrics.SetActivePrefixes(owner, "ospf", float64(ospfCount))
}

// updateOwnerBFDGauge refreshes the active BFD sessions gauge for a given owner.
func (s *Server) updateOwnerBFDGauge(owner string) {
	oi := s.intentStore.GetOwnerIntents(owner)
	if oi == nil {
		metrics.SetActiveBFDSessions(owner, 0)
		return
	}
	metrics.SetActiveBFDSessions(owner, float64(len(oi.BFD)))
}

// updateOwnerOSPFGauge refreshes the active OSPF interfaces gauge for a given owner.
func (s *Server) updateOwnerOSPFGauge(owner string) {
	oi := s.intentStore.GetOwnerIntents(owner)
	if oi == nil {
		metrics.SetActiveOSPFInterfaces(owner, 0)
		return
	}
	metrics.SetActiveOSPFInterfaces(owner, float64(len(oi.OSPF)))
}
