// Package intent provides an in-memory intent store that tracks routing intents
// from multiple clients (NovaEdge, NovaNet, Admin). Intents are organized by owner
// and keyed by type+identifier for efficient lookup and deduplication.
package intent

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"go.uber.org/zap"
)

// Sentinel errors for input validation and lookup failures.
var (
	ErrOwnerEmpty        = errors.New("owner must not be empty")
	ErrIntentNil         = errors.New("intent must not be nil")
	ErrNeighborAddrEmpty = errors.New("neighbor address must not be empty")
	ErrPrefixEmpty       = errors.New("prefix must not be empty")
	ErrPeerAddrEmpty     = errors.New("peer address must not be empty")
	ErrIfaceNameEmpty    = errors.New("interface name must not be empty")
	ErrAreaIDEmpty       = errors.New("area ID must not be empty")
	ErrNoIntents         = errors.New("no intents found")
	ErrIntentNotFound    = errors.New("intent not found")
	ErrAreaIDInvalid     = errors.New("area ID must be in dotted-decimal format or an integer")
	ErrAreaIDIPv6        = errors.New("OSPF area_id must be a dotted-decimal IPv4 address or integer, not IPv6")
)

// PeerIntent represents a BGP peer intent with metadata.
type PeerIntent struct {
	Owner               string
	NeighborAddress     string
	RemoteAS            uint32
	PeerType            v1.PeerType
	Keepalive           uint32
	HoldTime            uint32
	BFDEnabled          bool
	BFDMinRxMs          uint32
	BFDMinTxMs          uint32
	BFDDetectMultiplier uint32
	Description         string
	AddressFamilies     []v1.AddressFamily
	SourceAddress       string
	EBGPMultihop        uint32
	Password            string //nolint:gosec // BGP neighbor password field, not a credential
	MaxPrefixes         uint32
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// PrefixIntent represents a prefix advertisement intent with metadata.
type PrefixIntent struct {
	Owner           string
	Prefix          string
	Protocol        v1.Protocol
	LocalPreference uint32
	Communities     []string
	MED             uint32
	NextHop         string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// BFDIntent represents a BFD session intent with metadata.
type BFDIntent struct {
	Owner            string
	PeerAddress      string
	MinRxMs          uint32
	MinTxMs          uint32
	DetectMultiplier uint32
	InterfaceName    string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// OSPFIntent represents an OSPF interface intent with metadata.
type OSPFIntent struct {
	Owner         string
	InterfaceName string
	AreaID        string
	Passive       bool
	Cost          uint32
	HelloInterval uint32
	DeadInterval  uint32
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OwnerIntents holds all intents belonging to a single owner.
type OwnerIntents struct {
	Peers    map[string]*PeerIntent   // key: "peer:<neighbor_address>"
	Prefixes map[string]*PrefixIntent // key: "prefix:<protocol>:<prefix>"
	BFD      map[string]*BFDIntent    // key: "bfd:<peer_address>"
	OSPF     map[string]*OSPFIntent   // key: "ospf:<interface_name>"
}

// newOwnerIntents creates an empty OwnerIntents with initialized maps.
func newOwnerIntents() *OwnerIntents {
	return &OwnerIntents{
		Peers:    make(map[string]*PeerIntent),
		Prefixes: make(map[string]*PrefixIntent),
		BFD:      make(map[string]*BFDIntent),
		OSPF:     make(map[string]*OSPFIntent),
	}
}

// Store is a thread-safe in-memory store for routing intents from multiple owners.
type Store struct {
	mu      sync.RWMutex
	intents map[string]*OwnerIntents // keyed by owner name
	logger  *zap.Logger
}

// NewStore creates a new intent store with an initialized logger.
func NewStore(logger *zap.Logger) *Store {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Store{
		intents: make(map[string]*OwnerIntents),
		logger:  logger.Named("intent-store"),
	}
}

// peerKey returns the map key for a peer intent.
func peerKey(neighborAddr string) string {
	return "peer:" + neighborAddr
}

// prefixKey returns the map key for a prefix intent.
func prefixKey(protocol string, prefix string) string {
	return "prefix:" + strings.ToLower(protocol) + ":" + prefix
}

// bfdKey returns the map key for a BFD intent.
func bfdKey(peerAddr string) string {
	return "bfd:" + peerAddr
}

// ospfKey returns the map key for an OSPF intent.
func ospfKey(ifaceName string) string {
	return "ospf:" + ifaceName
}

// protocolString converts a protocol enum to a lowercase string key.
// NOTE: This helper is duplicated across intent, reconciler, and server packages.
func protocolString(p v1.Protocol) string {
	switch p {
	case v1.Protocol_PROTOCOL_BGP:
		return "bgp"
	case v1.Protocol_PROTOCOL_OSPF:
		return "ospf"
	case v1.Protocol_PROTOCOL_UNSPECIFIED:
		return "unknown"
	default:
		return "unknown"
	}
}

// ensureOwner returns the OwnerIntents for the given owner, creating it if it
// does not exist. Must be called with s.mu held for writing.
func (s *Store) ensureOwner(owner string) *OwnerIntents {
	oi, ok := s.intents[owner]
	if !ok {
		oi = newOwnerIntents()
		s.intents[owner] = oi
	}
	return oi
}

// intentResult holds the result of a type-specific intent store operation.
// It carries pointers to the intent's timestamp fields, the previous CreatedAt
// value (nil when the intent is newly created), and the extra log fields.
type intentResult struct {
	createdAt         *time.Time
	updatedAt         *time.Time
	existingCreatedAt *time.Time
	logFields         []zap.Field
}

// setIntent is the common helper for adding/updating an intent.
// It validates owner and nil-ness, acquires the lock, ensures the owner exists,
// resolves timestamps (preserving CreatedAt on updates), and logs the action.
// The execute callback must store the intent in the appropriate map and return
// an intentResult with pointers to the new intent's timestamp fields.
func (s *Store) setIntent(
	owner string,
	intentNil bool,
	intentType string,
	validate func() error,
	execute func(oi *OwnerIntents) intentResult,
) error {
	if owner == "" {
		return ErrOwnerEmpty
	}
	if intentNil {
		return ErrIntentNil
	}
	if err := validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oi := s.ensureOwner(owner)
	now := time.Now()
	res := execute(oi)

	action := "created"
	if res.existingCreatedAt != nil {
		*res.createdAt = *res.existingCreatedAt
		action = "updated"
	} else {
		*res.createdAt = now
	}
	*res.updatedAt = now

	s.logger.Info(action+" "+intentType,
		append([]zap.Field{zap.String("owner", owner)}, res.logFields...)...,
	)
	return nil
}

// SetPeerIntent adds or updates a BGP peer intent for the given owner.
func (s *Store) SetPeerIntent(owner string, intent *PeerIntent) error {
	return s.setIntent(owner, intent == nil, "peer intent",
		func() error {
			if intent.NeighborAddress == "" {
				return ErrNeighborAddrEmpty
			}
			return nil
		},
		func(oi *OwnerIntents) intentResult {
			key := peerKey(intent.NeighborAddress)
			intent.Owner = owner
			existing := oi.Peers[key]
			oi.Peers[key] = intent
			res := intentResult{
				createdAt: &intent.CreatedAt,
				updatedAt: &intent.UpdatedAt,
				logFields: []zap.Field{zap.String("neighbor", intent.NeighborAddress)},
			}
			if existing != nil {
				res.existingCreatedAt = &existing.CreatedAt
			}
			return res
		},
	)
}

// RemovePeerIntent removes a BGP peer intent for the given owner and neighbor address.
func (s *Store) RemovePeerIntent(owner string, neighborAddr string) error {
	if owner == "" {
		return ErrOwnerEmpty
	}
	if neighborAddr == "" {
		return ErrNeighborAddrEmpty
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oi, ok := s.intents[owner]
	if !ok {
		return fmt.Errorf("owner %q: %w", owner, ErrNoIntents)
	}

	key := peerKey(neighborAddr)
	if _, ok := oi.Peers[key]; !ok {
		return fmt.Errorf("peer intent for neighbor %q not found for owner %q: %w", neighborAddr, owner, ErrIntentNotFound)
	}

	delete(oi.Peers, key)
	s.logger.Info("removed peer intent",
		zap.String("owner", owner),
		zap.String("neighbor", neighborAddr),
	)
	return nil
}

// SetPrefixIntent adds or updates a prefix advertisement intent for the given owner.
func (s *Store) SetPrefixIntent(owner string, intent *PrefixIntent) error {
	return s.setIntent(owner, intent == nil, "prefix intent",
		func() error {
			if intent.Prefix == "" {
				return ErrPrefixEmpty
			}
			return nil
		},
		func(oi *OwnerIntents) intentResult {
			key := prefixKey(protocolString(intent.Protocol), intent.Prefix)
			intent.Owner = owner
			existing := oi.Prefixes[key]
			oi.Prefixes[key] = intent
			res := intentResult{
				createdAt: &intent.CreatedAt,
				updatedAt: &intent.UpdatedAt,
				logFields: []zap.Field{
					zap.String("prefix", intent.Prefix),
					zap.String("protocol", protocolString(intent.Protocol)),
				},
			}
			if existing != nil {
				res.existingCreatedAt = &existing.CreatedAt
			}
			return res
		},
	)
}

// RemovePrefixIntent removes a prefix intent for the given owner, prefix, and protocol.
func (s *Store) RemovePrefixIntent(owner string, prefix string, protocol string) error {
	if owner == "" {
		return ErrOwnerEmpty
	}
	if prefix == "" {
		return ErrPrefixEmpty
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oi, ok := s.intents[owner]
	if !ok {
		return fmt.Errorf("owner %q: %w", owner, ErrNoIntents)
	}

	key := prefixKey(protocol, prefix)
	if _, ok := oi.Prefixes[key]; !ok {
		return fmt.Errorf("prefix intent for %q (protocol %s) not found for owner %q: %w", prefix, protocol, owner, ErrIntentNotFound)
	}

	delete(oi.Prefixes, key)
	s.logger.Info("removed prefix intent",
		zap.String("owner", owner),
		zap.String("prefix", prefix),
		zap.String("protocol", protocol),
	)
	return nil
}

// SetBFDIntent adds or updates a BFD session intent for the given owner.
func (s *Store) SetBFDIntent(owner string, intent *BFDIntent) error {
	return s.setIntent(owner, intent == nil, "BFD intent",
		func() error {
			if intent.PeerAddress == "" {
				return ErrPeerAddrEmpty
			}
			return nil
		},
		func(oi *OwnerIntents) intentResult {
			key := bfdKey(intent.PeerAddress)
			intent.Owner = owner
			existing := oi.BFD[key]
			oi.BFD[key] = intent
			res := intentResult{
				createdAt: &intent.CreatedAt,
				updatedAt: &intent.UpdatedAt,
				logFields: []zap.Field{zap.String("peer", intent.PeerAddress)},
			}
			if existing != nil {
				res.existingCreatedAt = &existing.CreatedAt
			}
			return res
		},
	)
}

// RemoveBFDIntent removes a BFD session intent for the given owner and peer address.
func (s *Store) RemoveBFDIntent(owner string, peerAddr string) error {
	if owner == "" {
		return ErrOwnerEmpty
	}
	if peerAddr == "" {
		return ErrPeerAddrEmpty
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oi, ok := s.intents[owner]
	if !ok {
		return fmt.Errorf("owner %q: %w", owner, ErrNoIntents)
	}

	key := bfdKey(peerAddr)
	if _, ok := oi.BFD[key]; !ok {
		return fmt.Errorf("BFD intent for peer %q not found for owner %q: %w", peerAddr, owner, ErrIntentNotFound)
	}

	delete(oi.BFD, key)
	s.logger.Info("removed BFD intent",
		zap.String("owner", owner),
		zap.String("peer", peerAddr),
	)
	return nil
}

// SetOSPFIntent adds or updates an OSPF interface intent for the given owner.
func (s *Store) SetOSPFIntent(owner string, intent *OSPFIntent) error {
	return s.setIntent(owner, intent == nil, "OSPF intent",
		func() error {
			if intent.InterfaceName == "" {
				return ErrIfaceNameEmpty
			}
			if intent.AreaID == "" {
				return ErrAreaIDEmpty
			}
			// Validate area ID format: must be dotted-decimal (e.g. "0.0.0.0") or an integer.
			if ip := net.ParseIP(intent.AreaID); ip != nil {
				if ip.To4() == nil {
					return fmt.Errorf("%s: %w", intent.AreaID, ErrAreaIDIPv6)
				}
			} else {
				// Not a dotted-decimal IP; check if it's a plain integer.
				valid := true
				for _, ch := range intent.AreaID {
					if ch < '0' || ch > '9' {
						valid = false
						break
					}
				}
				if !valid {
					return fmt.Errorf("got %q: %w", intent.AreaID, ErrAreaIDInvalid)
				}
			}
			return nil
		},
		func(oi *OwnerIntents) intentResult {
			key := ospfKey(intent.InterfaceName)
			intent.Owner = owner
			existing := oi.OSPF[key]
			oi.OSPF[key] = intent
			res := intentResult{
				createdAt: &intent.CreatedAt,
				updatedAt: &intent.UpdatedAt,
				logFields: []zap.Field{zap.String("interface", intent.InterfaceName)},
			}
			if existing != nil {
				res.existingCreatedAt = &existing.CreatedAt
			}
			return res
		},
	)
}

// RemoveOSPFIntent removes an OSPF interface intent for the given owner and interface name.
func (s *Store) RemoveOSPFIntent(owner string, ifaceName string) error {
	if owner == "" {
		return ErrOwnerEmpty
	}
	if ifaceName == "" {
		return ErrIfaceNameEmpty
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oi, ok := s.intents[owner]
	if !ok {
		return fmt.Errorf("owner %q: %w", owner, ErrNoIntents)
	}

	key := ospfKey(ifaceName)
	if _, ok := oi.OSPF[key]; !ok {
		return fmt.Errorf("OSPF intent for interface %q not found for owner %q: %w", ifaceName, owner, ErrIntentNotFound)
	}

	delete(oi.OSPF, key)
	s.logger.Info("removed OSPF intent",
		zap.String("owner", owner),
		zap.String("interface", ifaceName),
	)
	return nil
}

// GetAllIntents returns a deep-copy snapshot of all intents, keyed by owner.
func (s *Store) GetAllIntents() map[string]*OwnerIntents {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*OwnerIntents, len(s.intents))
	for owner, oi := range s.intents {
		result[owner] = s.copyOwnerIntents(oi)
	}
	return result
}

// GetOwnerIntents returns a deep-copy snapshot of all intents for the specified owner.
// Returns nil if the owner has no intents.
func (s *Store) GetOwnerIntents(owner string) *OwnerIntents {
	s.mu.RLock()
	defer s.mu.RUnlock()

	oi, ok := s.intents[owner]
	if !ok {
		return nil
	}
	return s.copyOwnerIntents(oi)
}

// GetPeerIntents returns all peer intents across all owners.
func (s *Store) GetPeerIntents() []*PeerIntent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*PeerIntent
	for _, oi := range s.intents {
		for _, p := range oi.Peers {
			result = append(result, copyPeerIntent(p))
		}
	}
	return result
}

// GetPrefixIntents returns all prefix intents across all owners.
func (s *Store) GetPrefixIntents() []*PrefixIntent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*PrefixIntent
	for _, oi := range s.intents {
		for _, p := range oi.Prefixes {
			result = append(result, copyPrefixIntent(p))
		}
	}
	return result
}

// GetBFDIntents returns all BFD intents across all owners.
func (s *Store) GetBFDIntents() []*BFDIntent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*BFDIntent
	for _, oi := range s.intents {
		for _, b := range oi.BFD {
			result = append(result, copyBFDIntent(b))
		}
	}
	return result
}

// GetOSPFIntents returns all OSPF intents across all owners.
func (s *Store) GetOSPFIntents() []*OSPFIntent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*OSPFIntent
	for _, oi := range s.intents {
		for _, o := range oi.OSPF {
			result = append(result, copyOSPFIntent(o))
		}
	}
	return result
}

// RemoveAllByOwner removes all intents for the specified owner.
// This is typically called during client disconnect cleanup.
func (s *Store) RemoveAllByOwner(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.intents[owner]; !ok {
		s.logger.Debug("no intents to remove for owner", zap.String("owner", owner))
		return
	}

	delete(s.intents, owner)
	s.logger.Info("removed all intents for owner", zap.String("owner", owner))
}

// GetOwnerPrefixes returns a list of prefix strings owned by the given owner.
func (s *Store) GetOwnerPrefixes(owner string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	oi, ok := s.intents[owner]
	if !ok {
		return nil
	}

	result := make([]string, 0, len(oi.Prefixes))
	for _, p := range oi.Prefixes {
		result = append(result, p.Prefix)
	}
	return result
}

// GetOwnerPeers returns a list of peer address strings owned by the given owner.
func (s *Store) GetOwnerPeers(owner string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	oi, ok := s.intents[owner]
	if !ok {
		return nil
	}

	result := make([]string, 0, len(oi.Peers))
	for _, p := range oi.Peers {
		result = append(result, p.NeighborAddress)
	}
	return result
}

// --- deep copy helpers ---

// copyOwnerIntents creates a deep copy of an OwnerIntents struct.
func (s *Store) copyOwnerIntents(oi *OwnerIntents) *OwnerIntents {
	c := newOwnerIntents()
	for k, v := range oi.Peers {
		c.Peers[k] = copyPeerIntent(v)
	}
	for k, v := range oi.Prefixes {
		c.Prefixes[k] = copyPrefixIntent(v)
	}
	for k, v := range oi.BFD {
		c.BFD[k] = copyBFDIntent(v)
	}
	for k, v := range oi.OSPF {
		c.OSPF[k] = copyOSPFIntent(v)
	}
	return c
}

func copyPeerIntent(src *PeerIntent) *PeerIntent {
	dst := *src
	if src.AddressFamilies != nil {
		dst.AddressFamilies = make([]v1.AddressFamily, len(src.AddressFamilies))
		copy(dst.AddressFamilies, src.AddressFamilies)
	}
	return &dst
}

func copyPrefixIntent(src *PrefixIntent) *PrefixIntent {
	dst := *src
	if src.Communities != nil {
		dst.Communities = make([]string, len(src.Communities))
		copy(dst.Communities, src.Communities)
	}
	return &dst
}

func copyBFDIntent(src *BFDIntent) *BFDIntent {
	dst := *src
	return &dst
}

func copyOSPFIntent(src *OSPFIntent) *OSPFIntent {
	dst := *src
	return &dst
}
