// Package policy enforces ownership boundaries for route advertisement
// and protocol operations in NovaRoute. It validates which client can
// advertise which prefixes and perform which operations based on
// pre-shared tokens and configurable prefix policies.
package policy

// Config holds the policy configuration for all owners.
type Config struct {
	// Owners maps owner names to their policy configuration.
	Owners map[string]OwnerConfig
}

// OwnerConfig defines the authentication and prefix policy for a single owner.
type OwnerConfig struct {
	// Token is the pre-shared authentication token for this owner.
	Token string

	// AllowedPrefixes defines the prefix policy for this owner.
	AllowedPrefixes PrefixPolicy
}

// PrefixPolicy defines what kinds of prefixes an owner is allowed to advertise.
type PrefixPolicy struct {
	// Type controls the category of allowed prefixes:
	//   "host_only" - only /32 (IPv4) and /128 (IPv6) host routes
	//   "subnet"    - only /8 through /28 subnet routes, no host routes
	//   "any"       - all prefix lengths are allowed
	Type string

	// AllowedCIDRs is an optional list of CIDR ranges that further restrict
	// which prefixes can be advertised. If non-empty, the advertised prefix
	// must fall within at least one of these CIDRs.
	AllowedCIDRs []string
}
