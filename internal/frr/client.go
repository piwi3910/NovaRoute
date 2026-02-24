package frr

import (
	"context"
	"fmt"
	"sync"

	frr "github.com/piwi3910/NovaRoute/api/frr"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the FRR northbound gRPC client with connection management
// and transactional configuration helpers.
type Client struct {
	nb   frr.NorthboundClient
	conn *grpc.ClientConn
	log  *zap.Logger
	mu   sync.Mutex
}

// NewClient creates a new FRR northbound gRPC client connected to the given
// target. The target can be a unix socket path (e.g. "unix:///var/run/frr/northbound.sock")
// or a TCP address (e.g. "localhost:50051"). The connection uses insecure
// credentials since FRR northbound typically runs on a local socket.
func NewClient(target string, logger *zap.Logger) (*Client, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("frr: failed to create gRPC client to %s: %w", target, err)
	}

	logger.Info("connected to FRR northbound gRPC", zap.String("target", target))

	return &Client{
		nb:   frr.NewNorthboundClient(conn),
		conn: conn,
		log:  logger,
	}, nil
}

// Close shuts down the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// IsConnected returns true if the underlying gRPC connection is in a ready
// or idle state (i.e. not shut down or in a transient failure).
func (c *Client) IsConnected() bool {
	if c.conn == nil {
		return false
	}
	state := c.conn.GetState()
	return state == connectivity.Ready || state == connectivity.Idle
}

// GetVersion calls GetCapabilities and returns the FRR version string.
func (c *Client) GetVersion(ctx context.Context) (string, error) {
	resp, err := c.nb.GetCapabilities(ctx, &frr.GetCapabilitiesRequest{})
	if err != nil {
		return "", fmt.Errorf("frr: GetCapabilities failed: %w", err)
	}
	return resp.GetFrrVersion(), nil
}

// applyChanges executes a transactional configuration change using the FRR
// northbound candidate/commit model:
//  1. CreateCandidate() to get a candidate configuration ID
//  2. EditCandidate() to apply the updates and deletes
//  3. Commit() with phase ALL to atomically apply
//  4. DeleteCandidate() to clean up
//
// If the commit fails, the candidate is deleted to avoid leaking resources.
// The method is serialized with a mutex to prevent concurrent candidate
// operations which FRR does not support on a single client.
func (c *Client) applyChanges(ctx context.Context, updates []*frr.PathValue, deletes []*frr.PathValue) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(updates) == 0 && len(deletes) == 0 {
		return nil
	}

	// Step 1: Create a candidate configuration.
	createResp, err := c.nb.CreateCandidate(ctx, &frr.CreateCandidateRequest{})
	if err != nil {
		return fmt.Errorf("frr: CreateCandidate failed: %w", err)
	}
	candidateID := createResp.GetCandidateId()

	c.log.Debug("created FRR candidate",
		zap.Uint32("candidate_id", candidateID),
		zap.Int("updates", len(updates)),
		zap.Int("deletes", len(deletes)),
	)

	// Ensure candidate is always cleaned up.
	cleanup := func() {
		if _, delErr := c.nb.DeleteCandidate(ctx, &frr.DeleteCandidateRequest{
			CandidateId: candidateID,
		}); delErr != nil {
			c.log.Warn("failed to delete FRR candidate",
				zap.Uint32("candidate_id", candidateID),
				zap.Error(delErr),
			)
		}
	}

	// Step 2: Edit the candidate with updates and deletes.
	_, err = c.nb.EditCandidate(ctx, &frr.EditCandidateRequest{
		CandidateId: candidateID,
		Update:      updates,
		Delete:      deletes,
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("frr: EditCandidate failed (candidate_id=%d): %w", candidateID, err)
	}

	// Step 3: Commit the candidate atomically.
	commitResp, err := c.nb.Commit(ctx, &frr.CommitRequest{
		CandidateId: candidateID,
		Phase:       frr.CommitRequest_ALL,
		Comment:     "NovaRoute configuration update",
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("frr: Commit failed (candidate_id=%d): %w", candidateID, err)
	}
	if commitResp.GetErrorMessage() != "" {
		cleanup()
		return fmt.Errorf("frr: Commit returned error (candidate_id=%d): %s", candidateID, commitResp.GetErrorMessage())
	}

	c.log.Debug("committed FRR candidate",
		zap.Uint32("candidate_id", candidateID),
		zap.Uint32("transaction_id", commitResp.GetTransactionId()),
	)

	// Step 4: Delete the candidate (cleanup).
	cleanup()

	return nil
}

// pv is a shorthand helper to create a PathValue.
func pv(path, value string) *frr.PathValue {
	return &frr.PathValue{
		Path:  path,
		Value: value,
	}
}

// pvDelete is a shorthand helper to create a PathValue for deletion (empty value).
func pvDelete(path string) *frr.PathValue {
	return &frr.PathValue{
		Path:  path,
		Value: "",
	}
}
