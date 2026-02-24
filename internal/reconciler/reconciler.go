// Package reconciler translates routing intents from the intent store into
// FRR configuration via the FRR VTY socket client. It periodically
// compares the desired state (intents) with the applied state (what was
// last pushed to FRR) and applies the difference, handling additions,
// updates, and removals.
package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"github.com/piwi3910/NovaRoute/internal/frr"
	"github.com/piwi3910/NovaRoute/internal/intent"
	"github.com/piwi3910/NovaRoute/internal/metrics"
	"go.uber.org/zap"
)

// BGPGlobalConfig holds the BGP AS number and router ID needed to bootstrap
// the BGP instance in FRR before any neighbors or networks can be added.
type BGPGlobalConfig struct {
	LocalAS  uint32
	RouterID string
}

// Reconciler periodically compares the intent store's desired state with
// FRR's actual state and applies the difference. It tracks what has been
// applied to detect drift and ensure convergence.
type Reconciler struct {
	intentStore *intent.Store
	frrClient   *frr.Client
	logger      *zap.Logger
	bgpGlobal   *BGPGlobalConfig

	// Track what we've applied to FRR to detect drift.
	appliedPeers    map[string]*intent.PeerIntent
	appliedPrefixes map[string]*intent.PrefixIntent
	appliedBFD      map[string]*intent.BFDIntent
	appliedOSPF     map[string]*intent.OSPFIntent
	bgpConfigured   bool
	mu              sync.Mutex

	// triggerCh signals an immediate reconciliation.
	triggerCh chan struct{}
}

// NewReconciler creates a new Reconciler that reads intents from the given
// store and applies them to FRR via the provided client.
func NewReconciler(store *intent.Store, frrClient *frr.Client, logger *zap.Logger, bgpGlobal *BGPGlobalConfig) *Reconciler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Reconciler{
		intentStore:     store,
		frrClient:       frrClient,
		logger:          logger.Named("reconciler"),
		bgpGlobal:       bgpGlobal,
		appliedPeers:    make(map[string]*intent.PeerIntent),
		appliedPrefixes: make(map[string]*intent.PrefixIntent),
		appliedBFD:      make(map[string]*intent.BFDIntent),
		appliedOSPF:     make(map[string]*intent.OSPFIntent),
		triggerCh:       make(chan struct{}, 1),
	}
}

// Reconcile is the main reconciliation method. It:
//  1. Gets all intents from the store
//  2. Compares with applied state
//  3. Applies additions (new intents not in applied)
//  4. Applies removals (applied items no longer in intents)
//  5. Records metrics for each FRR transaction
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.logger.Debug("starting reconciliation cycle")
	start := time.Now()

	// Ensure BGP global is configured before reconciling peers/prefixes.
	if err := r.ensureBGPGlobal(ctx); err != nil {
		r.logger.Error("failed to ensure BGP global config", zap.Error(err))
		// Continue anyway — peer/prefix operations will fail but BFD/OSPF may work.
	}

	desiredPeers := r.intentStore.GetPeerIntents()
	desiredPrefixes := r.intentStore.GetPrefixIntents()
	desiredBFD := r.intentStore.GetBFDIntents()
	desiredOSPF := r.intentStore.GetOSPFIntents()

	var errs []error

	if err := r.ReconcilePeers(ctx, desiredPeers); err != nil {
		errs = append(errs, fmt.Errorf("reconcile peers: %w", err))
	}
	if err := r.ReconcilePrefixes(ctx, desiredPrefixes); err != nil {
		errs = append(errs, fmt.Errorf("reconcile prefixes: %w", err))
	}
	if err := r.ReconcileBFD(ctx, desiredBFD); err != nil {
		errs = append(errs, fmt.Errorf("reconcile BFD: %w", err))
	}
	if err := r.ReconcileOSPF(ctx, desiredOSPF); err != nil {
		errs = append(errs, fmt.Errorf("reconcile OSPF: %w", err))
	}

	duration := time.Since(start).Seconds()

	if len(errs) > 0 {
		metrics.RecordFRRTransaction("failure", duration)
		r.logger.Error("reconciliation completed with errors",
			zap.Int("error_count", len(errs)),
			zap.Duration("duration", time.Since(start)),
		)
		return fmt.Errorf("reconciliation had %d errors; first: %w", len(errs), errs[0])
	}

	metrics.RecordFRRTransaction("success", duration)
	r.logger.Debug("reconciliation cycle complete",
		zap.Duration("duration", time.Since(start)),
	)
	return nil
}

// ReconcilePeers compares desired peer intents with applied state and
// adds/removes BGP neighbors as needed.
func (r *Reconciler) ReconcilePeers(ctx context.Context, desired []*intent.PeerIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build a map of desired peers keyed by neighbor address.
	desiredMap := make(map[string]*intent.PeerIntent, len(desired))
	for _, p := range desired {
		key := peerKey(p.NeighborAddress)
		desiredMap[key] = p
	}

	var errs []error

	// Add or update peers that are desired but not yet applied (or changed).
	for key, dp := range desiredMap {
		existing, applied := r.appliedPeers[key]
		if applied && peerEqual(existing, dp) {
			continue
		}

		if err := r.applyPeerIntent(ctx, dp); err != nil {
			errs = append(errs, fmt.Errorf("apply peer %s: %w", dp.NeighborAddress, err))
			continue
		}
		r.appliedPeers[key] = dp
		metrics.RecordIntent(dp.Owner, "peer", "apply")
	}

	// Remove peers that are applied but no longer desired.
	for key, ap := range r.appliedPeers {
		if _, stillDesired := desiredMap[key]; !stillDesired {
			if err := r.removePeerFromFRR(ctx, ap.NeighborAddress); err != nil {
				errs = append(errs, fmt.Errorf("remove peer %s: %w", ap.NeighborAddress, err))
				continue
			}
			delete(r.appliedPeers, key)
			metrics.RecordIntent(ap.Owner, "peer", "remove")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("peer reconciliation had %d errors; first: %w", len(errs), errs[0])
	}
	return nil
}

// ReconcilePrefixes compares desired prefix intents with applied state and
// adds/removes prefix advertisements as needed.
func (r *Reconciler) ReconcilePrefixes(ctx context.Context, desired []*intent.PrefixIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build a map of desired prefixes keyed by protocol:prefix.
	desiredMap := make(map[string]*intent.PrefixIntent, len(desired))
	for _, p := range desired {
		key := prefixKey(p.Protocol, p.Prefix)
		desiredMap[key] = p
	}

	var errs []error

	// Add or update prefixes that are desired but not yet applied (or changed).
	for key, dp := range desiredMap {
		existing, applied := r.appliedPrefixes[key]
		if applied && prefixEqual(existing, dp) {
			continue
		}

		if err := r.applyPrefixIntent(ctx, dp); err != nil {
			errs = append(errs, fmt.Errorf("apply prefix %s: %w", dp.Prefix, err))
			continue
		}
		r.appliedPrefixes[key] = dp
		metrics.RecordIntent(dp.Owner, "prefix", "apply")
	}

	// Remove prefixes that are applied but no longer desired.
	for key, ap := range r.appliedPrefixes {
		if _, stillDesired := desiredMap[key]; !stillDesired {
			if err := r.removePrefixFromFRR(ctx, ap); err != nil {
				errs = append(errs, fmt.Errorf("remove prefix %s: %w", ap.Prefix, err))
				continue
			}
			delete(r.appliedPrefixes, key)
			metrics.RecordIntent(ap.Owner, "prefix", "remove")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("prefix reconciliation had %d errors; first: %w", len(errs), errs[0])
	}
	return nil
}

// ReconcileBFD compares desired BFD intents with applied state and
// adds/removes BFD sessions as needed.
func (r *Reconciler) ReconcileBFD(ctx context.Context, desired []*intent.BFDIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build a map of desired BFD sessions keyed by peer address.
	desiredMap := make(map[string]*intent.BFDIntent, len(desired))
	for _, b := range desired {
		key := bfdKey(b.PeerAddress)
		desiredMap[key] = b
	}

	var errs []error

	// Add or update BFD sessions that are desired but not yet applied (or changed).
	for key, db := range desiredMap {
		existing, applied := r.appliedBFD[key]
		if applied && bfdEqual(existing, db) {
			continue
		}

		if err := r.applyBFDIntent(ctx, db); err != nil {
			errs = append(errs, fmt.Errorf("apply BFD %s: %w", db.PeerAddress, err))
			continue
		}
		r.appliedBFD[key] = db
		metrics.RecordIntent(db.Owner, "bfd", "apply")
	}

	// Remove BFD sessions that are applied but no longer desired.
	for key, ab := range r.appliedBFD {
		if _, stillDesired := desiredMap[key]; !stillDesired {
			if err := r.removeBFDFromFRR(ctx, ab.PeerAddress); err != nil {
				errs = append(errs, fmt.Errorf("remove BFD %s: %w", ab.PeerAddress, err))
				continue
			}
			delete(r.appliedBFD, key)
			metrics.RecordIntent(ab.Owner, "bfd", "remove")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("BFD reconciliation had %d errors; first: %w", len(errs), errs[0])
	}
	return nil
}

// ReconcileOSPF compares desired OSPF intents with applied state and
// enables/disables OSPF interfaces as needed.
func (r *Reconciler) ReconcileOSPF(ctx context.Context, desired []*intent.OSPFIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build a map of desired OSPF interfaces keyed by interface name.
	desiredMap := make(map[string]*intent.OSPFIntent, len(desired))
	for _, o := range desired {
		key := ospfKey(o.InterfaceName)
		desiredMap[key] = o
	}

	var errs []error

	// Add or update OSPF interfaces that are desired but not yet applied (or changed).
	for key, do := range desiredMap {
		existing, applied := r.appliedOSPF[key]
		if applied && ospfEqual(existing, do) {
			continue
		}

		if err := r.applyOSPFIntent(ctx, do); err != nil {
			errs = append(errs, fmt.Errorf("apply OSPF %s: %w", do.InterfaceName, err))
			continue
		}
		r.appliedOSPF[key] = do
		metrics.RecordIntent(do.Owner, "ospf", "apply")
	}

	// Remove OSPF interfaces that are applied but no longer desired.
	for key, ao := range r.appliedOSPF {
		if _, stillDesired := desiredMap[key]; !stillDesired {
			if err := r.removeOSPFFromFRR(ctx, ao); err != nil {
				errs = append(errs, fmt.Errorf("remove OSPF %s: %w", ao.InterfaceName, err))
				continue
			}
			delete(r.appliedOSPF, key)
			metrics.RecordIntent(ao.Owner, "ospf", "remove")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("OSPF reconciliation had %d errors; first: %w", len(errs), errs[0])
	}
	return nil
}

// ApplyIntent applies a single intent to FRR based on its type.
// The intentType should be one of: "peer", "prefix", "bfd", "ospf".
// The value should be the corresponding intent struct pointer.
func (r *Reconciler) ApplyIntent(ctx context.Context, intentType string, value interface{}) error {
	switch intentType {
	case "peer":
		pi, ok := value.(*intent.PeerIntent)
		if !ok {
			return fmt.Errorf("ApplyIntent: expected *intent.PeerIntent, got %T", value)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.applyPeerIntent(ctx, pi); err != nil {
			return err
		}
		r.appliedPeers[peerKey(pi.NeighborAddress)] = pi
		return nil

	case "prefix":
		pi, ok := value.(*intent.PrefixIntent)
		if !ok {
			return fmt.Errorf("ApplyIntent: expected *intent.PrefixIntent, got %T", value)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.applyPrefixIntent(ctx, pi); err != nil {
			return err
		}
		r.appliedPrefixes[prefixKey(pi.Protocol, pi.Prefix)] = pi
		return nil

	case "bfd":
		bi, ok := value.(*intent.BFDIntent)
		if !ok {
			return fmt.Errorf("ApplyIntent: expected *intent.BFDIntent, got %T", value)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.applyBFDIntent(ctx, bi); err != nil {
			return err
		}
		r.appliedBFD[bfdKey(bi.PeerAddress)] = bi
		return nil

	case "ospf":
		oi, ok := value.(*intent.OSPFIntent)
		if !ok {
			return fmt.Errorf("ApplyIntent: expected *intent.OSPFIntent, got %T", value)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.applyOSPFIntent(ctx, oi); err != nil {
			return err
		}
		r.appliedOSPF[ospfKey(oi.InterfaceName)] = oi
		return nil

	default:
		return fmt.Errorf("ApplyIntent: unknown intent type %q", intentType)
	}
}

// RemoveIntent removes a single applied configuration from FRR by intent type
// and key. The key format depends on the intent type:
//   - peer: neighbor address (e.g. "10.0.0.1")
//   - prefix: "protocol:prefix" (e.g. "bgp:10.0.0.0/24")
//   - bfd: peer address (e.g. "10.0.0.1")
//   - ospf: interface name (e.g. "eth0")
func (r *Reconciler) RemoveIntent(ctx context.Context, intentType string, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch intentType {
	case "peer":
		mapKey := peerKey(key)
		ap, ok := r.appliedPeers[mapKey]
		if !ok {
			return fmt.Errorf("RemoveIntent: peer %q not found in applied state", key)
		}
		if err := r.removePeerFromFRR(ctx, ap.NeighborAddress); err != nil {
			return err
		}
		delete(r.appliedPeers, mapKey)
		return nil

	case "prefix":
		// key is expected to be the full prefixKey (e.g. "bgp:10.0.0.0/24")
		mapKey := "prefix:" + key
		ap, ok := r.appliedPrefixes[mapKey]
		if !ok {
			return fmt.Errorf("RemoveIntent: prefix %q not found in applied state", key)
		}
		if err := r.removePrefixFromFRR(ctx, ap); err != nil {
			return err
		}
		delete(r.appliedPrefixes, mapKey)
		return nil

	case "bfd":
		mapKey := bfdKey(key)
		_, ok := r.appliedBFD[mapKey]
		if !ok {
			return fmt.Errorf("RemoveIntent: BFD %q not found in applied state", key)
		}
		if err := r.removeBFDFromFRR(ctx, key); err != nil {
			return err
		}
		delete(r.appliedBFD, mapKey)
		return nil

	case "ospf":
		mapKey := ospfKey(key)
		ao, ok := r.appliedOSPF[mapKey]
		if !ok {
			return fmt.Errorf("RemoveIntent: OSPF %q not found in applied state", key)
		}
		if err := r.removeOSPFFromFRR(ctx, ao); err != nil {
			return err
		}
		delete(r.appliedOSPF, mapKey)
		return nil

	default:
		return fmt.Errorf("RemoveIntent: unknown intent type %q", intentType)
	}
}

// RunLoop starts a goroutine that calls Reconcile() at the given interval.
// It also listens for immediate trigger signals via TriggerReconcile().
// The loop runs until the context is cancelled.
func (r *Reconciler) RunLoop(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		r.logger.Info("reconciler loop started",
			zap.Duration("interval", interval),
		)

		for {
			select {
			case <-ctx.Done():
				r.logger.Info("reconciler loop stopped")
				return
			case <-ticker.C:
				if err := r.Reconcile(ctx); err != nil {
					r.logger.Error("periodic reconciliation failed", zap.Error(err))
				}
			case <-r.triggerCh:
				if err := r.Reconcile(ctx); err != nil {
					r.logger.Error("triggered reconciliation failed", zap.Error(err))
				}
			}
		}
	}()
}

// TriggerReconcile triggers an immediate reconciliation cycle. If a
// reconciliation is already pending, the signal is coalesced (non-blocking).
func (r *Reconciler) TriggerReconcile() {
	select {
	case r.triggerCh <- struct{}{}:
		r.logger.Debug("reconciliation triggered")
	default:
		r.logger.Debug("reconciliation already pending, skipping trigger")
	}
}

// SetFRRClient updates the FRR client used by the reconciler. This is called
// when the FRR connection is established after the reconciler has already started.
func (r *Reconciler) SetFRRClient(client *frr.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frrClient = client
	r.logger.Info("FRR client updated in reconciler")
}

// --- FRR application helpers ---

// ensureBGPGlobal configures the BGP instance (router bgp <AS>) in FRR if it
// hasn't been done yet. This must be called before any peer or prefix operations.
func (r *Reconciler) ensureBGPGlobal(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.bgpConfigured {
		return nil
	}
	if r.frrClient == nil {
		return fmt.Errorf("FRR client not available")
	}
	if r.bgpGlobal == nil || r.bgpGlobal.LocalAS == 0 {
		return fmt.Errorf("BGP global config not set (local_as=0)")
	}

	r.logger.Info("configuring BGP global",
		zap.Uint32("local_as", r.bgpGlobal.LocalAS),
		zap.String("router_id", r.bgpGlobal.RouterID),
	)

	if err := r.frrClient.ConfigureBGPGlobal(ctx, r.bgpGlobal.LocalAS, r.bgpGlobal.RouterID); err != nil {
		return fmt.Errorf("configure BGP global: %w", err)
	}

	r.bgpConfigured = true
	r.logger.Info("BGP global configured successfully")
	return nil
}

// applyPeerIntent translates a PeerIntent into FRR client calls:
// AddNeighbor + ActivateNeighborAFI for each address family.
func (r *Reconciler) applyPeerIntent(ctx context.Context, p *intent.PeerIntent) error {
	peerType := resolvePeerType(p.PeerType)

	start := time.Now()
	err := r.frrClient.AddNeighbor(ctx, p.NeighborAddress, p.RemoteAS, peerType, p.Keepalive, p.HoldTime)
	duration := time.Since(start).Seconds()

	if err != nil {
		metrics.RecordFRRTransaction("failure", duration)
		return fmt.Errorf("add neighbor %s: %w", p.NeighborAddress, err)
	}
	metrics.RecordFRRTransaction("success", duration)

	// Activate each address family.
	for _, af := range p.AddressFamilies {
		afiName := resolveAddressFamily(af)
		if afiName == "" {
			continue
		}

		afiStart := time.Now()
		afiErr := r.frrClient.ActivateNeighborAFI(ctx, p.NeighborAddress, afiName)
		afiDuration := time.Since(afiStart).Seconds()

		if afiErr != nil {
			metrics.RecordFRRTransaction("failure", afiDuration)
			return fmt.Errorf("activate AFI %s for neighbor %s: %w", afiName, p.NeighborAddress, afiErr)
		}
		metrics.RecordFRRTransaction("success", afiDuration)
	}

	r.logger.Info("applied peer intent",
		zap.String("neighbor", p.NeighborAddress),
		zap.Uint32("remote_as", p.RemoteAS),
		zap.String("owner", p.Owner),
	)
	return nil
}

// removePeerFromFRR removes a BGP neighbor from FRR.
func (r *Reconciler) removePeerFromFRR(ctx context.Context, addr string) error {
	start := time.Now()
	err := r.frrClient.RemoveNeighbor(ctx, addr)
	duration := time.Since(start).Seconds()

	if err != nil {
		metrics.RecordFRRTransaction("failure", duration)
		return fmt.Errorf("remove neighbor %s: %w", addr, err)
	}
	metrics.RecordFRRTransaction("success", duration)

	r.logger.Info("removed peer from FRR", zap.String("neighbor", addr))
	return nil
}

// applyPrefixIntent translates a PrefixIntent into FRR client calls.
// BGP prefixes use AdvertiseNetwork; OSPF prefixes are handled via OSPF
// interface configuration (no separate prefix call needed).
func (r *Reconciler) applyPrefixIntent(ctx context.Context, p *intent.PrefixIntent) error {
	switch p.Protocol {
	case v1.Protocol_PROTOCOL_BGP:
		afi := detectAFI(p.Prefix)

		start := time.Now()
		err := r.frrClient.AdvertiseNetwork(ctx, p.Prefix, afi)
		duration := time.Since(start).Seconds()

		if err != nil {
			metrics.RecordFRRTransaction("failure", duration)
			return fmt.Errorf("advertise network %s: %w", p.Prefix, err)
		}
		metrics.RecordFRRTransaction("success", duration)

		r.logger.Info("applied BGP prefix intent",
			zap.String("prefix", p.Prefix),
			zap.String("owner", p.Owner),
		)

	case v1.Protocol_PROTOCOL_OSPF:
		// OSPF prefix advertisement is handled via OSPF interface
		// configuration. The prefix itself does not need a separate
		// FRR call; the OSPF interface intent covers it.
		r.logger.Debug("OSPF prefix intent noted (handled via OSPF interface config)",
			zap.String("prefix", p.Prefix),
			zap.String("owner", p.Owner),
		)

	default:
		return fmt.Errorf("unsupported protocol %v for prefix %s", p.Protocol, p.Prefix)
	}

	return nil
}

// removePrefixFromFRR removes a prefix advertisement from FRR.
func (r *Reconciler) removePrefixFromFRR(ctx context.Context, p *intent.PrefixIntent) error {
	switch p.Protocol {
	case v1.Protocol_PROTOCOL_BGP:
		afi := detectAFI(p.Prefix)

		start := time.Now()
		err := r.frrClient.WithdrawNetwork(ctx, p.Prefix, afi)
		duration := time.Since(start).Seconds()

		if err != nil {
			metrics.RecordFRRTransaction("failure", duration)
			return fmt.Errorf("withdraw network %s: %w", p.Prefix, err)
		}
		metrics.RecordFRRTransaction("success", duration)

		r.logger.Info("removed BGP prefix from FRR",
			zap.String("prefix", p.Prefix),
			zap.String("owner", p.Owner),
		)

	case v1.Protocol_PROTOCOL_OSPF:
		// OSPF prefix removal is handled via OSPF interface removal.
		r.logger.Debug("OSPF prefix removal noted (handled via OSPF interface config)",
			zap.String("prefix", p.Prefix),
			zap.String("owner", p.Owner),
		)

	default:
		return fmt.Errorf("unsupported protocol %v for prefix removal %s", p.Protocol, p.Prefix)
	}

	return nil
}

// applyBFDIntent translates a BFDIntent into an FRR AddBFDPeer call.
func (r *Reconciler) applyBFDIntent(ctx context.Context, b *intent.BFDIntent) error {
	start := time.Now()
	err := r.frrClient.AddBFDPeer(ctx, b.PeerAddress, b.MinRxMs, b.MinTxMs, b.DetectMultiplier, b.InterfaceName)
	duration := time.Since(start).Seconds()

	if err != nil {
		metrics.RecordFRRTransaction("failure", duration)
		return fmt.Errorf("add BFD peer %s: %w", b.PeerAddress, err)
	}
	metrics.RecordFRRTransaction("success", duration)

	r.logger.Info("applied BFD intent",
		zap.String("peer", b.PeerAddress),
		zap.String("owner", b.Owner),
	)
	return nil
}

// removeBFDFromFRR removes a BFD session from FRR.
func (r *Reconciler) removeBFDFromFRR(ctx context.Context, peerAddr string) error {
	start := time.Now()
	err := r.frrClient.RemoveBFDPeer(ctx, peerAddr)
	duration := time.Since(start).Seconds()

	if err != nil {
		metrics.RecordFRRTransaction("failure", duration)
		return fmt.Errorf("remove BFD peer %s: %w", peerAddr, err)
	}
	metrics.RecordFRRTransaction("success", duration)

	r.logger.Info("removed BFD peer from FRR", zap.String("peer", peerAddr))
	return nil
}

// applyOSPFIntent translates an OSPFIntent into an FRR EnableOSPFInterface call.
func (r *Reconciler) applyOSPFIntent(ctx context.Context, o *intent.OSPFIntent) error {
	start := time.Now()
	err := r.frrClient.EnableOSPFInterface(ctx, o.InterfaceName, o.AreaID, o.Passive, o.Cost, o.HelloInterval, o.DeadInterval)
	duration := time.Since(start).Seconds()

	if err != nil {
		metrics.RecordFRRTransaction("failure", duration)
		return fmt.Errorf("enable OSPF on %s: %w", o.InterfaceName, err)
	}
	metrics.RecordFRRTransaction("success", duration)

	r.logger.Info("applied OSPF intent",
		zap.String("interface", o.InterfaceName),
		zap.String("area", o.AreaID),
		zap.String("owner", o.Owner),
	)
	return nil
}

// removeOSPFFromFRR disables OSPF on an interface in FRR.
func (r *Reconciler) removeOSPFFromFRR(ctx context.Context, o *intent.OSPFIntent) error {
	start := time.Now()
	err := r.frrClient.DisableOSPFInterface(ctx, o.InterfaceName, o.AreaID)
	duration := time.Since(start).Seconds()

	if err != nil {
		metrics.RecordFRRTransaction("failure", duration)
		return fmt.Errorf("disable OSPF on %s: %w", o.InterfaceName, err)
	}
	metrics.RecordFRRTransaction("success", duration)

	r.logger.Info("removed OSPF interface from FRR",
		zap.String("interface", o.InterfaceName),
		zap.String("area", o.AreaID),
		zap.String("owner", o.Owner),
	)
	return nil
}

// --- Key generation helpers ---

// peerKey returns the map key for a peer intent.
func peerKey(neighborAddr string) string {
	return "peer:" + neighborAddr
}

// prefixKey returns the map key for a prefix intent.
func prefixKey(protocol v1.Protocol, prefix string) string {
	return "prefix:" + protocolString(protocol) + ":" + prefix
}

// bfdKey returns the map key for a BFD intent.
func bfdKey(peerAddr string) string {
	return "bfd:" + peerAddr
}

// ospfKey returns the map key for an OSPF intent.
func ospfKey(ifaceName string) string {
	return "ospf:" + ifaceName
}

// --- Enum/type resolution helpers ---

// resolvePeerType converts a v1.PeerType enum to the string expected by the
// FRR client ("internal" or "external").
func resolvePeerType(pt v1.PeerType) string {
	switch pt {
	case v1.PeerType_PEER_TYPE_INTERNAL:
		return "internal"
	case v1.PeerType_PEER_TYPE_EXTERNAL:
		return "external"
	default:
		return "external"
	}
}

// resolveAddressFamily converts a v1.AddressFamily enum to the friendly
// AFI name accepted by the FRR client.
func resolveAddressFamily(af v1.AddressFamily) string {
	switch af {
	case v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST:
		return "ipv4-unicast"
	case v1.AddressFamily_ADDRESS_FAMILY_IPV6_UNICAST:
		return "ipv6-unicast"
	default:
		return ""
	}
}

// protocolString converts a v1.Protocol enum to a lowercase string.
func protocolString(p v1.Protocol) string {
	switch p {
	case v1.Protocol_PROTOCOL_BGP:
		return "bgp"
	case v1.Protocol_PROTOCOL_OSPF:
		return "ospf"
	default:
		return "unknown"
	}
}

// detectAFI determines the AFI (ipv4-unicast or ipv6-unicast) from the prefix
// format. A prefix containing ":" is assumed to be IPv6.
func detectAFI(prefix string) string {
	for _, ch := range prefix {
		if ch == ':' {
			return "ipv6-unicast"
		}
	}
	return "ipv4-unicast"
}

// --- Equality helpers for drift detection ---

// peerEqual returns true if two peer intents are functionally equivalent
// (ignoring timestamps and owner).
func peerEqual(a, b *intent.PeerIntent) bool {
	if a.NeighborAddress != b.NeighborAddress {
		return false
	}
	if a.RemoteAS != b.RemoteAS {
		return false
	}
	if a.PeerType != b.PeerType {
		return false
	}
	if a.Keepalive != b.Keepalive {
		return false
	}
	if a.HoldTime != b.HoldTime {
		return false
	}
	if a.BFDEnabled != b.BFDEnabled {
		return false
	}
	if a.EBGPMultihop != b.EBGPMultihop {
		return false
	}
	if a.Password != b.Password {
		return false
	}
	if a.SourceAddress != b.SourceAddress {
		return false
	}
	if len(a.AddressFamilies) != len(b.AddressFamilies) {
		return false
	}
	for i := range a.AddressFamilies {
		if a.AddressFamilies[i] != b.AddressFamilies[i] {
			return false
		}
	}
	return true
}

// prefixEqual returns true if two prefix intents are functionally equivalent.
func prefixEqual(a, b *intent.PrefixIntent) bool {
	if a.Prefix != b.Prefix {
		return false
	}
	if a.Protocol != b.Protocol {
		return false
	}
	if a.LocalPreference != b.LocalPreference {
		return false
	}
	if a.MED != b.MED {
		return false
	}
	if a.NextHop != b.NextHop {
		return false
	}
	if len(a.Communities) != len(b.Communities) {
		return false
	}
	for i := range a.Communities {
		if a.Communities[i] != b.Communities[i] {
			return false
		}
	}
	return true
}

// bfdEqual returns true if two BFD intents are functionally equivalent.
func bfdEqual(a, b *intent.BFDIntent) bool {
	return a.PeerAddress == b.PeerAddress &&
		a.MinRxMs == b.MinRxMs &&
		a.MinTxMs == b.MinTxMs &&
		a.DetectMultiplier == b.DetectMultiplier &&
		a.InterfaceName == b.InterfaceName
}

// ospfEqual returns true if two OSPF intents are functionally equivalent.
func ospfEqual(a, b *intent.OSPFIntent) bool {
	return a.InterfaceName == b.InterfaceName &&
		a.AreaID == b.AreaID &&
		a.Passive == b.Passive &&
		a.Cost == b.Cost &&
		a.HelloInterval == b.HelloInterval &&
		a.DeadInterval == b.DeadInterval
}
