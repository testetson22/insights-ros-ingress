package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RedHatInsights/insights-ros-ingress/internal/auth"
	"github.com/RedHatInsights/insights-ros-ingress/internal/config"
	"github.com/RedHatInsights/insights-ros-ingress/internal/health"
	"github.com/RedHatInsights/insights-ros-ingress/internal/logger"
	"github.com/RedHatInsights/insights-ros-ingress/internal/messaging"
	"github.com/RedHatInsights/insights-ros-ingress/internal/storage"
	"github.com/RedHatInsights/insights-ros-ingress/internal/upload"
	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

func main() {
	// Initialize logger
	log := logger.InitLogger()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.WithError(err).Fatal("Failed to load configuration")
	}

	log.WithFields(logrus.Fields{
		"service":      "insights-ros-ingress",
		"version":      "1.0.0",
		"main_port":    cfg.Server.Port,
		"metrics_port": 9090,
	}).Info("Starting Insights ROS Ingress service")

	// Initialize storage client
	storageClient, err := storage.NewMinIOClient(cfg.Storage)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize storage client")
	}

	// Initialize messaging client
	messagingClient, err := messaging.NewKafkaProducer(cfg.Kafka)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize messaging client")
	}
	defer func() {
		if err := messagingClient.Close(); err != nil {
			log.WithError(err).Error("Failed to close messaging client")
		}
	}()

	// Initialize health checker
	healthChecker := health.NewChecker(storageClient, messagingClient)

	// Initialize upload handler
	uploadHandler := upload.NewHandler(cfg, storageClient, messagingClient, log)

	// Setup main HTTP routes
	router := chi.NewRouter()

	// API routes - no authentication
	router.Route("/api/ingress/v1", func(r chi.Router) {
		r.Post("/upload", uploadHandler.HandleUpload)
	})

	// Health and observability routes
	router.Get("/health", healthChecker.Health)
	router.Get("/ready", healthChecker.Ready)

	// Create main HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(cfg.Server.IdleTimeout) * time.Second,
	}

	// Setup metrics HTTP routes with OAuth2 authentication
	authMiddleware := auth.KubernetesAuthMiddleware(log)
	metricsRouter := chi.NewRouter()

	// Metrics endpoint with OAuth2 authentication
	metricsRouter.With(authMiddleware).Get("/metrics", healthChecker.Metrics)

	// Create metrics HTTP server on port 9090
	metricsServer := &http.Server{
		Addr:         ":9090",
		Handler:      metricsRouter,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(cfg.Server.IdleTimeout) * time.Second,
	}

	// Start main server in a goroutine
	go func() {
		log.WithField("addr", server.Addr).Info("Starting main HTTP server")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Fatal("Main HTTP server failed")
		}
	}()

	// Start metrics server in a goroutine
	go func() {
		log.WithField("addr", metricsServer.Addr).Info("Starting metrics HTTP server with OAuth2 authentication")
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Fatal("Metrics HTTP server failed")
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down servers...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown both servers concurrently
	shutdownErrs := make(chan error, 2)
	
	go func() {
		if err := server.Shutdown(ctx); err != nil {
			shutdownErrs <- fmt.Errorf("main server shutdown error: %w", err)
		} else {
			shutdownErrs <- nil
		}
	}()
	
	go func() {
		if err := metricsServer.Shutdown(ctx); err != nil {
			shutdownErrs <- fmt.Errorf("metrics server shutdown error: %w", err)
		} else {
			shutdownErrs <- nil
		}
	}()

	// Wait for both servers to shutdown
	for i := 0; i < 2; i++ {
		if err := <-shutdownErrs; err != nil {
			log.WithError(err).Error("Server forced to shutdown")
		}
	}

	log.Info("All servers exited")
}
