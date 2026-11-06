package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"message-gateway/internal/config"
	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

const shutdownTimeout = 10 * time.Second

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	client, err := napcat.NewClient(cfg.NapCat)
	if err != nil {
		log.Fatalf("napcat client: %v", err)
	}

	manager, err := message.NewManager(cfg.Manager)
	if err != nil {
		log.Fatalf("message manager: %v", err)
	}
	if err := manager.Register(cfg.SenderKey, client, cfg.Gap); err != nil {
		log.Fatalf("register sender: %v", err)
	}

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.HTTPAddr, err)
	}

	api := httpapi.New(manager, httpapi.Options{
		SenderKey:      cfg.SenderKey,
		APIKey:         cfg.APIKey,
		MaxConnections: cfg.WebSocketMaxConnections,
	})

	server := &http.Server{
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	runCtx, cancel := context.WithCancel(context.Background())
	var background sync.WaitGroup
	background.Add(2)
	go func() {
		defer background.Done()
		client.Run(runCtx)
	}()
	go func() {
		defer background.Done()
		manager.Run(runCtx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	log.Printf(
		"message-gateway listening on %s (sender %s, napcat %s)",
		cfg.HTTPAddr,
		cfg.SenderKey,
		cfg.NapCat.BaseURL,
	)

	exitCode := 0
	select {
	case sig := <-signals:
		log.Printf("received %s, shutting down", sig)
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server error: %v", err)
			exitCode = 1
		}
	}

	// Hijacked WebSocket connections are not tracked by net/http, so close
	// them explicitly before shutting the server down.
	api.CloseWebSockets()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
		_ = server.Close()
		exitCode = 1
	}
	shutdownCancel()

	cancel()
	background.Wait()
	log.Println("message-gateway stopped")

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
