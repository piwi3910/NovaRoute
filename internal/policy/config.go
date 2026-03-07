// Package policy enforces ownership boundaries for route advertisement
// and protocol operations in NovaRoute. It validates which client can
// advertise which prefixes and perform which operations based on
// pre-shared tokens and configurable prefix policies.
package policy

import (
	"github.com/azrtydxb/NovaRoute/internal/config"
)

// Config holds the policy configuration for all owners.
type Config struct {
	Owners map[string]config.OwnerConfig
}
