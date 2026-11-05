package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"message-gateway/internal/fakenapcat"
)

const shutdownTimeout = 5 * time.Second

func main() {
	flags := flag.NewFlagSet("fake-napcat", flag.ExitOnError)
	addr := flags.String("addr", envString("FAKE_NAPCAT_ADDR", fakenapcat.DefaultAddr), "HTTP listen address")
	token := flags.String("token", envString("FAKE_NAPCAT_ACCESS_TOKEN", os.Getenv("NAPCAT_ACCESS_TOKEN")), "optional bearer token")
	loginID := flags.Int64("login-id", envInt64("FAKE_NAPCAT_LOGIN_ID", fakenapcat.DefaultLoginID), "fake bot QQ id")
	nickname := flags.String("login-nickname", envString("FAKE_NAPCAT_LOGIN_NICKNAME", fakenapcat.DefaultLoginNickname), "fake bot nickname")
	friends := flags.String("friends", envListString("FAKE_NAPCAT_FRIENDS", "10001"), "comma-separated friend ids")
	groups := flags.String("groups", envListString("FAKE_NAPCAT_GROUPS", "20001"), "comma-separated group ids")
	members := flags.String("members", envListString("FAKE_NAPCAT_MEMBERS", "10001"), "comma-separated member ids in every group")
	failFirst := flags.Int("fail-first", envInt("FAKE_NAPCAT_FAIL_FIRST", 0), "fail the first N send attempts")
	failMode := flags.String("fail-mode", envString("FAKE_NAPCAT_FAIL_MODE", string(fakenapcat.FailModeTransient)), "transient, permanent, or http500")
	delay := flags.Duration("delay", envDuration("FAKE_NAPCAT_DELAY", 0), "delay before send responses")
	messageIDStart := flags.Int64("message-id-start", envInt64("FAKE_NAPCAT_MESSAGE_ID_START", fakenapcat.DefaultMessageIDStart), "first fake message id")
	if err := flags.Parse(os.Args[1:]); err != nil {
		log.Fatalf("flags: %v", err)
	}

	friendIDs, err := parseIDList(*friends)
	if err != nil {
		log.Fatalf("friends: %v", err)
	}
	groupIDs, err := parseIDList(*groups)
	if err != nil {
		log.Fatalf("groups: %v", err)
	}
	memberIDs, err := parseIDList(*members)
	if err != nil {
		log.Fatalf("members: %v", err)
	}

	address := strings.TrimSpace(*addr)
	if address == "" {
		address = fakenapcat.DefaultAddr
	}
	accountID := *loginID
	if accountID == 0 {
		accountID = fakenapcat.DefaultLoginID
	}
	accountName := strings.TrimSpace(*nickname)
	if accountName == "" {
		accountName = fakenapcat.DefaultLoginNickname
	}
	failureMode := fakenapcat.FailMode(strings.ToLower(strings.TrimSpace(*failMode)))
	if failureMode == "" {
		failureMode = fakenapcat.FailModeTransient
	}

	server, err := fakenapcat.NewServer(fakenapcat.Config{
		Addr:           address,
		AccessToken:    strings.TrimSpace(*token),
		LoginID:        accountID,
		LoginNickname:  accountName,
		Friends:        friendIDs,
		Groups:         groupIDs,
		Members:        memberIDs,
		FailFirst:      *failFirst,
		FailMode:       failureMode,
		Delay:          *delay,
		MessageIDStart: *messageIDStart,
	})
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("listen on %s: %v", address, err)
	}

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	log.Printf(
		"fake-napcat listening on %s (login=%d nickname=%q friends=%v groups=%v members=%v fail_first=%d fail_mode=%s)",
		address,
		accountID,
		accountName,
		friendIDs,
		groupIDs,
		memberIDs,
		*failFirst,
		failureMode,
	)

	select {
	case <-ctx.Done():
		log.Printf("received shutdown signal")
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server error: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
		_ = httpServer.Close()
	}
	cancel()
}

func parseIDList(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, err
		}
		if id <= 0 {
			return nil, errors.New("ids must be positive")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func envString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

// envListString allows an explicitly empty value to clear a list, while an
// unset variable still uses the default.
func envListString(key, fallback string) string {
	value, exists := os.LookupEnv(key)
	if !exists {
		return fallback
	}
	return strings.TrimSpace(value)
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return value
}

func envInt64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return value
}
