package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cloudterminal "cloud-terminal"
	"cloud-terminal/internal/agent"
	"cloud-terminal/internal/audit"
	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/gateway"
)

func main() {
	var configPath string
	var mode string
	flag.StringVar(&configPath, "config", "policy.yaml", "security policy file")
	flag.StringVar(&mode, "mode", "local", "run mode: local, cloud, or agent")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	created, err := config.Ensure(configPath)
	if err != nil {
		logger.Error("ensure config", "path", configPath, "error", err)
		os.Exit(1)
	}
	if created {
		logger.Info("created default config", "path", configPath)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}

	store := config.NewStore(configPath, cfg)

	writer, err := audit.NewJSONLWriter(cfg.Server.AuditLogPath)
	if err != nil {
		logger.Error("open audit log", "error", err)
		os.Exit(1)
	}
	defer writer.Close()

	runtime := edge.NewRuntime(edge.Options{
		PolicyProvider: store,
		Audit:          writer,
		DefaultEnv:     cfg.Edge.Env,
		DefaultDir:     cfg.Edge.WorkDir,
		CommandTimeout: cfg.Edge.CommandTimeout.Duration,
		MaxOutputSize:  cfg.Edge.MaxOutputBytes,
		Logger:         logger,
	})

	cloudTunnel := cfg.CloudTunnel
	if mode == "local" && cloudTunnel.Enabled {
		mode = "agent"
	}

	if mode == "agent" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		srv := gateway.NewServer(gateway.Options{
			Runtime:            nil,
			StaticFS:           cloudterminal.WebFS(),
			Config:             store,
			EdgeID:             cfg.Edge.ID,
			EdgeName:           cfg.Edge.Name,
			WorkbenchStatePath: cfg.Server.WorkbenchStatePath,
			Logger:             logger,
			AgentMode:          true,
		})

		// Resolve the cloud base URL used for proxying auth and user APIs.
		// gateway_url is the actual cloud API server; discovery_url is used
		// only when gateway_url is not configured (and typically points to
		// the same host anyway).
		cloudBase := strings.TrimSpace(cloudTunnel.GatewayURL)
		if cloudBase == "" {
			cloudBase = strings.TrimSpace(cloudTunnel.DiscoveryURL)
		}

		httpServer := &http.Server{
			Addr:              cfg.Server.Addr,
			Handler:           srv.AgentRoutes(cloudBase),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			logger.Info("agent admin listening", "addr", cfg.Server.Addr, "edge_id", cfg.Edge.ID, "cloud", cloudBase)
			logger.Info("agent admin login URL", "url", fmt.Sprintf("http://%s/agent/login.html", cfg.Server.Addr))
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("admin server failed", "error", err)
			}
		}()

		agent := agent.New(agent.Options{
			DiscoveryURL: cloudTunnel.DiscoveryURL,
			GatewayURL:   cloudTunnel.GatewayURL,
			Username:     cloudTunnel.Account,
			SessionID:    cloudTunnel.SessionID,
			Runtime:      runtime,
			Config:       store,
			EdgeID:       cfg.Edge.ID,
			EdgeName:     cfg.Edge.Name,
			Logger:       logger,
		})
		agentErr := make(chan error, 1)
		go func() {
			agentErr <- agent.Run(ctx)
		}()

		select {
		case <-ctx.Done():
		case err := <-agentErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("agent failed", "error", err)
			}
			logger.Info("agent tunnel stopped; local admin still available, waiting for signal")
			<-ctx.Done()
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return
	}

	var serverRuntime gateway.Runtime
	if mode != "cloud" {
		serverRuntime = gateway.NewLocalRuntime(runtime)
	}

	srv := gateway.NewServer(gateway.Options{
		Runtime:            serverRuntime,
		StaticFS:           cloudterminal.WebFS(),
		Config:             store,
		EdgeID:             cfg.Edge.ID,
		EdgeName:           cfg.Edge.Name,
		WorkbenchStatePath: cfg.Server.WorkbenchStatePath,
		Logger:             logger,
	})

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("cloud terminal listening", "addr", cfg.Server.Addr, "edge_id", cfg.Edge.ID, "mode", mode)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		os.Exit(1)
	}
}
