// Package main implements the novaroute-agent binary, the node-local
// routing control plane daemon. It exposes a gRPC API on a Unix domain
// socket, manages routing intents from multiple clients, and reconciles
// the desired state to FRR via its VTY Unix socket interface.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	v1 "github.com/piwi3910/NovaRoute/api/v1"
	"github.com/piwi3910/NovaRoute/internal/config"
	"github.com/piwi3910/NovaRoute/internal/frr"
	"github.com/piwi3910/NovaRoute/internal/intent"
	"github.com/piwi3910/NovaRoute/internal/metrics"
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

func main() {
	configPath := flag.String("config", "/etc/novaroute/config.json", "path to JSON config file")
	flag.Parse()

	// Load and validate configuration.
	cfg, err := config.LoadFromFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		os.Exit(1)
	}

	config.ExpandEnvVars(cfg)

	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid config: %v\n", err)
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

	// Create intent store.
	store := intent.NewStore(logger)
	logger.Info("intent store initialized")

	// Convert config owners to policy engine config.
	policyCfg := convertPolicyConfig(cfg)
	policyEngine := policy.NewEngine(policyCfg, logger)
	logger.Info("policy engine initialized",
		zap.Int("owners", len(policyCfg.Owners)),
	)

	// Connect FRR client in a background goroutine with retry loop.
	// The VTY client connects to FRR daemon sockets in the socket directory.
	var frrClient *frr.Client
	frrReady := make(chan struct{})

	go func() {
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

			// Verify connectivity by checking that required sockets exist
			// and fetching the FRR version via zebra.
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

			frrClient = client
			logger.Info("FRR VTY connection established",
				zap.String("version", version),
			)
			close(frrReady)
			metrics.SetFRRConnected(true)
			return
		}
	}()

	// Create reconciler. It starts with a nil FRR client and will receive
	// the client once the FRR connection goroutine succeeds.
	bgpGlobal := &reconciler.BGPGlobalConfig{
		LocalAS:  cfg.BGP.LocalAS,
		RouterID: cfg.BGP.RouterID,
	}
	rec := reconciler.NewReconciler(store, nil, logger, bgpGlobal)

	// Create gRPC server.
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(recoveryUnaryInterceptor(logger)),
		grpc.ChainStreamInterceptor(recoveryStreamInterceptor(logger)),
	)
	srv := server.New(grpcServer, store, policyEngine, rec, logger)
	logger.Info("gRPC server created")

	if cfg.DisconnectGracePeriod > 0 {
		logger.Info("disconnect grace period configured (session TTL tracking not yet implemented)",
			zap.Int("grace_period_seconds", cfg.DisconnectGracePeriod),
		)
	}

	// Wire the server's event bus into the reconciler for FRR state change events.
	// This must happen BEFORE RunLoop so early reconciliation cycles can publish events.
	rec.SetEventPublisher(srv.EventBus())

	// Start reconciler loop. It will skip FRR operations until the client is set.
	rec.RunLoop(ctx, 30*time.Second)
	logger.Info("reconciler loop started")

	// When FRR connects, update the reconciler and server with the FRR client
	// and trigger an initial reconcile.
	go func() {
		select {
		case <-frrReady:
			rec.SetFRRClient(frrClient)
			srv.SetFRRClient(frrClient)
			logger.Info("FRR client injected into reconciler and server")

			// Publish FRR connected event.
			srv.EventBus().Publish(&v1.RouteEvent{
				Type:          v1.EventType_EVENT_TYPE_FRR_CONNECTED,
				Detail:        "FRR VTY connection established",
				TimestampUnix: time.Now().Unix(),
			})
			metrics.RecordEvent("frr_connected")

			rec.TriggerReconcile()
		case <-ctx.Done():
		}
	}()

	// Remove stale socket file if it exists.
	socketPath := cfg.ListenSocket
	if err := removeStaleSocket(socketPath); err != nil {
		logger.Fatal("failed to remove stale socket", zap.Error(err))
	}

	// Ensure the socket directory exists.
	socketDir := socketPath[:strings.LastIndex(socketPath, "/")]
	if socketDir != "" {
		if mkdirErr := os.MkdirAll(socketDir, 0o755); mkdirErr != nil {
			logger.Fatal("failed to create socket directory",
				zap.String("dir", socketDir),
				zap.Error(mkdirErr),
			)
		}
	}

	// Start listening on Unix socket.
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		logger.Fatal("failed to listen on Unix socket",
			zap.String("path", socketPath),
			zap.Error(err),
		)
	}
	defer lis.Close()

	// Set socket permissions to allow other containers on the node to connect.
	if chmodErr := os.Chmod(socketPath, 0o666); chmodErr != nil {
		logger.Warn("failed to set socket permissions", zap.Error(chmodErr))
	}

	logger.Info("gRPC server listening", zap.String("socket", socketPath))

	// Start gRPC server in background.
	go func() {
		if serveErr := grpcServer.Serve(lis); serveErr != nil {
			logger.Error("gRPC server stopped", zap.Error(serveErr))
			cancel()
		}
	}()

	// Start Prometheus metrics HTTP server.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	metricsMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if frrClient != nil && frrClient.IsReady() {
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
		if metricsErr := metricsServer.ListenAndServe(); metricsErr != nil && metricsErr != http.ErrServerClosed {
			logger.Error("metrics server stopped", zap.Error(metricsErr))
			cancel()
		}
	}()

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	case <-ctx.Done():
		logger.Info("context cancelled")
	}

	// Graceful shutdown.
	logger.Info("shutting down gracefully")
	cancel()

	// Wait for reconciler loop to exit before withdrawing state.
	rec.WaitForStop()
	logger.Info("reconciler loop stopped")

	// Withdraw all applied routing state from FRR before tearing down
	// the gRPC server and FRR connection. This ensures BGP peers,
	// advertised prefixes, BFD sessions, and OSPF interfaces are
	// cleanly removed so upstream routers see graceful withdrawal
	// rather than abrupt session drops.
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
	if frrClient != nil {
		if closeErr := frrClient.Close(); closeErr != nil {
			logger.Warn("FRR client close error", zap.Error(closeErr))
		}
		logger.Info("FRR client closed")
	}

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
	owners := make(map[string]policy.OwnerConfig, len(cfg.Owners))
	for name, oc := range cfg.Owners {
		owners[name] = policy.OwnerConfig{
			Token: oc.Token,
			AllowedPrefixes: policy.PrefixPolicy{
				Type:         oc.AllowedPrefixes.Type,
				AllowedCIDRs: oc.AllowedPrefixes.AllowedCIDRs,
			},
		}
	}
	return policy.Config{Owners: owners}
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
