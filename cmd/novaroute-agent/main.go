// Package main implements the novaroute-agent binary, the node-local
// routing control plane daemon. It exposes a gRPC API on a Unix domain
// socket, manages routing intents from multiple clients, and reconciles
// the desired state to FRR via its northbound gRPC interface.
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

	"github.com/piwi3910/NovaRoute/internal/config"
	"github.com/piwi3910/NovaRoute/internal/frr"
	"github.com/piwi3910/NovaRoute/internal/intent"
	"github.com/piwi3910/NovaRoute/internal/policy"
	"github.com/piwi3910/NovaRoute/internal/reconciler"
	"github.com/piwi3910/NovaRoute/internal/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
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
	var frrClient *frr.Client
	frrReady := make(chan struct{})

	go func() {
		retryInterval := time.Duration(cfg.FRR.RetryInterval) * time.Second
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			logger.Info("connecting to FRR northbound gRPC",
				zap.String("target", cfg.FRR.Target),
			)

			client, connErr := frr.NewClient(cfg.FRR.Target, logger)
			if connErr != nil {
				logger.Warn("failed to connect to FRR, retrying",
					zap.Error(connErr),
					zap.Duration("retry_in", retryInterval),
				)
				select {
				case <-ctx.Done():
					return
				case <-time.After(retryInterval):
					continue
				}
			}

			// Verify connectivity by fetching FRR version.
			vCtx, vCancel := context.WithTimeout(ctx, time.Duration(cfg.FRR.ConnectTimeout)*time.Second)
			version, vErr := client.GetVersion(vCtx)
			vCancel()

			if vErr != nil {
				logger.Warn("FRR connected but GetVersion failed, retrying",
					zap.Error(vErr),
					zap.Duration("retry_in", retryInterval),
				)
				_ = client.Close()
				select {
				case <-ctx.Done():
					return
				case <-time.After(retryInterval):
					continue
				}
			}

			frrClient = client
			logger.Info("FRR connection established",
				zap.String("version", version),
			)
			close(frrReady)
			return
		}
	}()

	// Create reconciler. It handles a nil frrClient gracefully and will
	// start applying once the client is available.
	rec := reconciler.NewReconciler(store, frrClient, logger)

	// Wait briefly for FRR to connect before starting the reconciler loop,
	// but do not block startup indefinitely.
	select {
	case <-frrReady:
		// FRR connected, reconciler will use the client immediately.
		rec = reconciler.NewReconciler(store, frrClient, logger)
		logger.Info("FRR ready, starting reconciler with active client")
	case <-time.After(5 * time.Second):
		logger.Warn("FRR not ready after 5s, starting reconciler without FRR client (will retry)")
	}

	// Start reconciler loop in background.
	rec.RunLoop(ctx, 30*time.Second)
	logger.Info("reconciler loop started")

	// Create gRPC server.
	grpcServer := grpc.NewServer()
	server.New(grpcServer, store, policyEngine, rec, logger)
	logger.Info("gRPC server created")

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
