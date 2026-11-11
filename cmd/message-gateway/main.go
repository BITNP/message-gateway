package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"message-gateway/internal/accounts"
	"message-gateway/internal/config"
	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

const shutdownTimeout = 10 * time.Second

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	registry, err := accounts.Build(cfg.Accounts)
	if err != nil {
		log.Fatalf("accounts: %v", err)
	}

	manager, err := message.NewManager(cfg.Manager)
	if err != nil {
		log.Fatalf("message manager: %v", err)
	}
	for _, account := range registry.List() {
		if err := manager.Register(account.Name, account.Sender, cfg.Gap); err != nil {
			log.Fatalf("register account %s: %v", account.Name, err)
		}
	}

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.HTTPAddr, err)
	}

	api := httpapi.New(manager, httpapi.Options{
		Accounts:       httpapiAccounts(registry),
		MaxConnections: cfg.WebSocketMaxConnections,
		StatusCapacity: cfg.StatusCapacity,
	})

	server := &http.Server{
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
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
		registry.Run(runCtx)
	}()
	go func() {
		defer background.Done()
		manager.Run(runCtx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	names := make([]string, 0, registry.Len())
	for _, account := range registry.List() {
		names = append(names, account.Name)
	}
	log.Printf(
		"message-gateway listening on %s (accounts %s)",
		cfg.HTTPAddr,
		strings.Join(names, ", "),
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

func httpapiAccounts(registry *accounts.Registry) map[string]httpapi.Account {
	out := make(map[string]httpapi.Account, registry.Len())
	for _, account := range registry.List() {
		out[account.Name] = httpapi.Account{
			Name:      account.Name,
			Driver:    account.Driver,
			Platform:  account.Platform,
			TokenHash: account.TokenHash,
			Proxy:     account.Proxy,
		}
	}
	return out
}
