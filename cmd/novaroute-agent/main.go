// Package main implements the novaroute-agent binary, the node-local
// routing control plane daemon. It exposes a gRPC API on a Unix domain
// socket, manages routing intents from multiple clients, and reconciles
// the desired state to FRR via its VTY Unix socket interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"github.com/piwi3910/NovaRoute/internal/config"
	"github.com/piwi3910/NovaRoute/internal/frr"
	"github.com/piwi3910/NovaRoute/internal/intent"
	metrics "github.com/piwi3910/NovaRoute/internal/metrics"
	"github.com/piwi3910/NovaRoute/internal/policy"
	"github.com/piwi3910/NovaRoute/internal/reconciler"
	"github.com/piwi3910/NovaRoute/internal/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
)

// errInvalidSocketPath is returned when the listen_socket path has no directory separator.
var errInvalidSocketPath = errors.New("invalid listen_socket path: must contain a directory separator")

// frrState holds the shared FRR client state used across goroutines.
type frrState struct {
	mu     sync.Mutex
	client *frr.Client
	ready  chan struct{}
}

func main() {
	configPath := flag.String("config", "/etc/novaroute/config.json", "path to JSON config file")
	flag.Parse()

	// Load and validate configuration.
	cfg, err := loadAndValidateConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Set up structured logger.
	logger, err := buildLogger(cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: building logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	logger.Info("novaroute-agent starting",
		zap.String("config", *configPath),
		zap.String("log_level", cfg.LogLevel),
		zap.String("listen_socket", cfg.ListenSocket),
		zap.String("metrics_address", cfg.MetricsAddress),
	)

	// Create root context with cancellation for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create intent store and policy engine.
	store := intent.NewStore(logger)
	logger.Info("intent store initialized")

	policyCfg := convertPolicyConfig(cfg)
	policyEngine := policy.NewEngine(policyCfg, logger)
	logger.Info("policy engine initialized",
		zap.Int("owners", len(policyCfg.Owners)),
	)

	// Start FRR connection in background.
	fs := &frrState{ready: make(chan struct{})}
	go connectFRR(ctx, cfg, logger, fs)

	// Create reconciler and gRPC server.
	rec, srv, grpcServer := createServers(cfg, store, policyEngine, logger, fs)

	// Wire event bus and start reconciler loop.
	rec.SetEventPublisher(srv.EventBus())
	rec.RunLoop(ctx, 30*time.Second)
	logger.Info("reconciler loop started")

	// Inject FRR client when ready and bootstrap config peers.
	go waitForFRR(ctx, fs, rec, srv, cfg, store, logger)

	// Set up and start the Unix socket listener.
	lis, socketPath, err := setupSocketListener(ctx, cfg, logger)
	if err != nil {
		logger.Fatal("socket setup failed", zap.Error(err))
	}
	defer func() { _ = lis.Close() }()
	logger.Info("gRPC server listening", zap.String("socket", socketPath))

	// Start gRPC server in background.
	go func() {
		if serveErr := grpcServer.Serve(lis); serveErr != nil {
			logger.Error("gRPC server stopped", zap.Error(serveErr))
			cancel()
		}
	}()

	// Start Prometheus metrics HTTP server.
	metricsServer := startMetricsServer(cfg, logger, fs, cancel)

	// Wait for shutdown signal.
	awaitShutdownSignal(ctx, logger)

	// Graceful shutdown.
	gracefulShutdown(ctx, logger, cancel, fs, srv, rec, grpcServer, metricsServer, socketPath)
}

// loadAndValidateConfig loads the configuration file, expands env vars,
// and validates the result.
func loadAndValidateConfig(path string) (*config.Config, error) {
	cfg, err := config.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	config.ExpandEnvVars(cfg)

	if err := config.Validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// connectFRR runs the FRR VTY connection retry loop in a background goroutine.
func connectFRR(ctx context.Context, cfg *config.Config, logger *zap.Logger, fs *frrState) {
	metrics.SetFRRConnected(false)
	retryInterval := time.Duration(cfg.FRR.RetryInterval) * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		logger.Info("connecting to FRR VTY sockets",
			zap.String("socket_dir", cfg.FRR.SocketDir),
		)

		client := frr.NewClient(cfg.FRR.SocketDir, logger)

		if !client.IsReady() {
			logger.Warn("FRR VTY sockets not ready, retrying",
				zap.Duration("retry_in", retryInterval),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}

		vCtx, vCancel := context.WithTimeout(ctx, time.Duration(cfg.FRR.ConnectTimeout)*time.Second)
		version, vErr := client.GetVersion(vCtx)
		vCancel()

		if vErr != nil {
			logger.Warn("FRR sockets exist but GetVersion failed, retrying",
				zap.Error(vErr),
				zap.Duration("retry_in", retryInterval),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
				continue
			}
		}

		fs.mu.Lock()
		fs.client = client
		fs.mu.Unlock()
		logger.Info("FRR VTY connection established",
			zap.String("version", version),
		)
		close(fs.ready)
		metrics.SetFRRConnected(true)
		return
	}
}

// createServers creates the reconciler, gRPC server, and NovaRoute server.
func createServers(cfg *config.Config, store *intent.Store, policyEngine *policy.Engine, logger *zap.Logger, fs *frrState) (*reconciler.Reconciler, *server.Server, *grpc.Server) {
	_ = fs // FRR client is nil initially; injected later.

	bgpGlobal := &reconciler.BGPGlobalConfig{
		LocalAS:  cfg.BGP.LocalAS,
		RouterID: cfg.BGP.RouterID,
	}
	rec := reconciler.NewReconciler(store, nil, logger, bgpGlobal)

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(recoveryUnaryInterceptor(logger)),
		grpc.ChainStreamInterceptor(recoveryStreamInterceptor(logger)),
	)
	srv := server.New(grpcServer, store, policyEngine, rec, logger)
	logger.Info("gRPC server created")

	if cfg.DisconnectGracePeriod > 0 {
		logger.Warn("disconnect_grace_period is configured but not yet supported; intents will be removed immediately on deregister",
			zap.Int("grace_period_seconds", cfg.DisconnectGracePeriod),
		)
	}

	return rec, srv, grpcServer
}

// waitForFRR waits for the FRR client to be ready, then injects it into the
// reconciler and server, and bootstraps config-defined BGP peers.
func waitForFRR(ctx context.Context, fs *frrState, rec *reconciler.Reconciler, srv *server.Server, cfg *config.Config, store *intent.Store, logger *zap.Logger) {
	select {
	case <-fs.ready:
		fs.mu.Lock()
		rec.SetFRRClient(fs.client)
		srv.SetFRRClient(fs.client)
		fs.mu.Unlock()
		logger.Info("FRR client injected into reconciler and server")

		srv.EventBus().Publish(&v1.RouteEvent{
			Type:          v1.EventType_EVENT_TYPE_FRR_CONNECTED,
			Detail:        "FRR VTY connection established",
			TimestampUnix: time.Now().Unix(),
		})
		metrics.RecordEvent("frr_connected")

		// Bootstrap config-defined BGP peers before triggering reconcile.
		initConfigPeers(cfg, store, logger)

		rec.TriggerReconcile()
	case <-ctx.Done():
	}
}

// configOwner is the special owner name used for peers defined in the config file.
const configOwner = "_config"

// initConfigPeers loads BGP peers from the agent config into the intent store.
// These peers are applied on the first reconciliation cycle after FRR is ready.
func initConfigPeers(cfg *config.Config, store *intent.Store, logger *zap.Logger) {
	if len(cfg.BGP.Peers) == 0 {
		return
	}

	logger.Info("bootstrapping config-defined BGP peers",
		zap.Int("count", len(cfg.BGP.Peers)),
	)

	for _, peer := range cfg.BGP.Peers {
		addressFamilies := make([]v1.AddressFamily, 0, len(peer.AddressFamilies))
		for _, af := range peer.AddressFamilies {
			switch strings.ToLower(af) {
			case "ipv4-unicast", "ipv4_unicast":
				addressFamilies = append(addressFamilies, v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST)
			case "ipv6-unicast", "ipv6_unicast":
				addressFamilies = append(addressFamilies, v1.AddressFamily_ADDRESS_FAMILY_IPV6_UNICAST)
			default:
				logger.Warn("unknown address family in config peer, skipping",
					zap.String("neighbor", peer.NeighborAddress),
					zap.String("address_family", af),
				)
			}
		}
		// Default to IPv4 unicast if no address families specified.
		if len(addressFamilies) == 0 {
			addressFamilies = []v1.AddressFamily{v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST}
		}

		peerIntent := &intent.PeerIntent{
			Owner:               configOwner,
			NeighborAddress:     peer.NeighborAddress,
			RemoteAS:            peer.RemoteAS,
			PeerType:            v1.PeerType_PEER_TYPE_EXTERNAL,
			Keepalive:           peer.Keepalive,
			HoldTime:            peer.HoldTime,
			BFDEnabled:          peer.BFDEnabled,
			BFDMinRxMs:          peer.BFDMinRxMs,
			BFDMinTxMs:          peer.BFDMinTxMs,
			BFDDetectMultiplier: peer.BFDDetectMultiplier,
			Description:         peer.Description,
			AddressFamilies:     addressFamilies,
			SourceAddress:       peer.SourceAddress,
			EBGPMultihop:        peer.EBGPMultihop,
			Password:            peer.Password,
			MaxPrefixes:         peer.MaxPrefixes,
		}

		if err := store.SetPeerIntent(configOwner, peerIntent); err != nil {
			logger.Error("failed to set config peer intent",
				zap.String("neighbor", peer.NeighborAddress),
				zap.Error(err),
			)
			continue
		}

		logger.Info("config peer loaded into intent store",
			zap.String("neighbor", peer.NeighborAddress),
			zap.Uint32("remote_as", peer.RemoteAS),
			zap.Bool("bfd", peer.BFDEnabled),
		)
	}
}

// setupSocketListener prepares the Unix domain socket directory, removes stale
// sockets, starts listening, and sets permissions.
func setupSocketListener(ctx context.Context, cfg *config.Config, logger *zap.Logger) (net.Listener, string, error) {
	socketPath := cfg.ListenSocket

	if err := removeStaleSocket(socketPath); err != nil {
		return nil, socketPath, fmt.Errorf("remove stale socket: %w", err)
	}

	lastSlash := strings.LastIndex(socketPath, "/")
	if lastSlash < 0 {
		return nil, socketPath, errInvalidSocketPath
	}
	socketDir := socketPath[:lastSlash]
	if socketDir != "" {
		if mkdirErr := os.MkdirAll(socketDir, 0o750); mkdirErr != nil {
			return nil, socketPath, fmt.Errorf("create socket directory %s: %w", socketDir, mkdirErr)
		}
	}

	lis, err := (&net.ListenConfig{}).Listen(ctx, "unix", socketPath)
	if err != nil {
		return nil, socketPath, fmt.Errorf("listen on Unix socket %s: %w", socketPath, err)
	}

	if chmodErr := os.Chmod(socketPath, 0o600); chmodErr != nil {
		logger.Warn("failed to set socket permissions", zap.Error(chmodErr))
	}

	return lis, socketPath, nil
}

// startMetricsServer creates and starts the Prometheus metrics HTTP server.
func startMetricsServer(cfg *config.Config, logger *zap.Logger, fs *frrState, cancel context.CancelFunc) *http.Server {
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	metricsMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		client := fs.client
		fs.mu.Unlock()
		if client != nil && client.IsReady() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready: FRR not connected"))
		}
	})

	metricsServer := &http.Server{
		Addr:              cfg.MetricsAddress,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("metrics server starting", zap.String("address", cfg.MetricsAddress))
		if metricsErr := metricsServer.ListenAndServe(); metricsErr != nil && !errors.Is(metricsErr, http.ErrServerClosed) {
			logger.Error("metrics server stopped", zap.Error(metricsErr))
			cancel()
		}
	}()

	return metricsServer
}

// awaitShutdownSignal blocks until a SIGTERM/SIGINT or context cancellation.
func awaitShutdownSignal(ctx context.Context, logger *zap.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	case <-ctx.Done():
		logger.Info("context cancelled")
	}
}

// gracefulShutdown orchestrates a clean shutdown of all components.
func gracefulShutdown(
	_ context.Context,
	logger *zap.Logger,
	cancel context.CancelFunc,
	fs *frrState,
	srv *server.Server,
	rec *reconciler.Reconciler,
	grpcServer *grpc.Server,
	metricsServer *http.Server,
	socketPath string,
) {
	logger.Info("shutting down gracefully")

	// Publish FRR disconnected event before shutdown.
	fs.mu.Lock()
	if fs.client != nil {
		srv.EventBus().Publish(&v1.RouteEvent{
			Type:          v1.EventType_EVENT_TYPE_FRR_DISCONNECTED,
			Detail:        "FRR VTY connection closing (agent shutdown)",
			TimestampUnix: time.Now().Unix(),
		})
		metrics.RecordEvent("frr_disconnected")
	}
	fs.mu.Unlock()

	cancel()

	// Wait for reconciler loop to exit before withdrawing state.
	rec.WaitForStop()
	logger.Info("reconciler loop stopped")

	// Withdraw all applied routing state from FRR.
	withdrawCtx, withdrawCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if withdrawErr := rec.WithdrawAll(withdrawCtx); withdrawErr != nil {
		logger.Warn("WithdrawAll completed with errors", zap.Error(withdrawErr))
	} else {
		logger.Info("all routing state withdrawn from FRR")
	}
	withdrawCancel()

	// Stop gRPC server gracefully.
	grpcServer.GracefulStop()
	logger.Info("gRPC server stopped")

	// Stop metrics server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if shutdownErr := metricsServer.Shutdown(shutdownCtx); shutdownErr != nil {
		logger.Warn("metrics server shutdown error", zap.Error(shutdownErr))
	}
	logger.Info("metrics server stopped")

	// Close FRR client.
	fs.mu.Lock()
	if fs.client != nil {
		if closeErr := fs.client.Close(); closeErr != nil {
			logger.Warn("FRR client close error", zap.Error(closeErr))
		}
		logger.Info("FRR client closed")
	}
	fs.mu.Unlock()

	// Clean up socket.
	_ = os.Remove(socketPath)

	logger.Info("novaroute-agent shutdown complete")
}

// buildLogger creates a zap.Logger with the given log level.
func buildLogger(level string) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	switch strings.ToLower(level) {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "info":
		zapLevel = zapcore.InfoLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	zapCfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapLevel),
		Development:      false,
		Encoding:         "json",
		EncoderConfig:    zap.NewProductionEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}
	zapCfg.EncoderConfig.TimeKey = "ts"
	zapCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return zapCfg.Build()
}

// convertPolicyConfig converts the config.Owners map to a policy.Config.
func convertPolicyConfig(cfg *config.Config) policy.Config {
	return policy.Config{Owners: cfg.Owners}
}

// removeStaleSocket removes a Unix socket file if it exists and is a socket.
func removeStaleSocket(path string) error {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat socket %s: %w", path, err)
	}

	if fi.Mode()&os.ModeSocket != 0 {
		if removeErr := os.Remove(path); removeErr != nil {
			return fmt.Errorf("remove stale socket %s: %w", path, removeErr)
		}
	}

	return nil
}

// recoveryUnaryInterceptor returns a gRPC unary interceptor that recovers from
// panics in RPC handlers, logs the panic, and returns an Internal error.
func recoveryUnaryInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered in gRPC handler",
					zap.String("method", info.FullMethod),
					zap.Any("panic", r),
				)
				err = grpcStatus.Errorf(grpcCodes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// recoveryStreamInterceptor returns a gRPC stream interceptor that recovers from
// panics in streaming RPC handlers, logs the panic, and returns an Internal error.
func recoveryStreamInterceptor(logger *zap.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered in gRPC stream handler",
					zap.String("method", info.FullMethod),
					zap.Any("panic", r),
				)
				err = grpcStatus.Errorf(grpcCodes.Internal, "internal server error")
			}
		}()
		return handler(srv, ss)
	}
}
