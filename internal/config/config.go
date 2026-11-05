// Package config loads and validates the gateway's environment configuration.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

const (
	defaultHTTPAddr  = ":8081"
	defaultSenderKey = "qq"

	defaultQueueSize    = 100
	defaultSendTimeout  = 30 * time.Second
	defaultMaxAttempts  = 3
	defaultRetryBackoff = 2 * time.Second

	defaultGapFixed   = 3 * time.Second
	defaultGapLength  = time.Second
	defaultGapPerRune = 50 * time.Millisecond
	defaultGapJitter  = time.Second
	defaultGapMin     = time.Second

	defaultNapcatTimeout          = 10 * time.Second
	defaultNapcatDirectoryRefresh = 30 * time.Minute
	defaultNapcatDirectoryRetry   = 5 * time.Second
	defaultNapcatDirectoryTimeout = 60 * time.Second
)

// Config is the validated runtime configuration for the gateway.
type Config struct {
	HTTPAddr  string
	APIKey    string
	SenderKey string
	NapCat    napcat.Config
	Manager   message.Config
	Gap       message.GapPolicy
}

// Load reads and validates the gateway environment. The API key and NapCat
// base URL are required because the gateway has no disabled mode.
func Load(getenv func(string) string) (Config, error) {
	apiKey := strings.TrimSpace(getenv("MESSAGE_GATEWAY_API_KEY"))
	if apiKey == "" {
		return Config{}, errors.New("MESSAGE_GATEWAY_API_KEY is required")
	}

	httpAddr := strings.TrimSpace(getenv("HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}

	senderKey := strings.TrimSpace(getenv("MESSAGE_SENDER_KEY"))
	if senderKey == "" {
		senderKey = defaultSenderKey
	}

	baseURL := strings.TrimSpace(getenv("NAPCAT_BASE_URL"))
	if baseURL == "" {
		return Config{}, errors.New("NAPCAT_BASE_URL is required")
	}

	queueSize, err := parsePositiveIntSetting(getenv("MESSAGE_QUEUE_SIZE"), defaultQueueSize)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_QUEUE_SIZE: %w", err)
	}

	sendTimeout, err := parsePositiveDurationSetting(getenv("MESSAGE_SEND_TIMEOUT"), defaultSendTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_SEND_TIMEOUT: %w", err)
	}

	maxAttempts, err := parsePositiveIntSetting(getenv("MESSAGE_MAX_ATTEMPTS"), defaultMaxAttempts)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_MAX_ATTEMPTS: %w", err)
	}

	retryBackoff, err := parseNonNegativeDurationSetting(getenv("MESSAGE_RETRY_BACKOFF"), defaultRetryBackoff)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_RETRY_BACKOFF: %w", err)
	}

	minGap, err := parseNonNegativeDurationSetting(getenv("MESSAGE_GAP_MIN"), defaultGapMin)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_GAP_MIN: %w", err)
	}

	gapMode := message.GapMode(strings.ToLower(strings.TrimSpace(getenv("MESSAGE_GAP_MODE"))))
	if gapMode == "" {
		gapMode = message.GapModeFixed
	}

	fixedGap, err := parseNonNegativeDurationSetting(getenv("MESSAGE_GAP_FIXED"), defaultGapFixed)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_GAP_FIXED: %w", err)
	}

	lengthBase, err := parseNonNegativeDurationSetting(getenv("MESSAGE_GAP_LENGTH_BASE"), defaultGapLength)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_GAP_LENGTH_BASE: %w", err)
	}

	perRune, err := parseNonNegativeDurationSetting(getenv("MESSAGE_GAP_PER_RUNE"), defaultGapPerRune)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_GAP_PER_RUNE: %w", err)
	}

	jitter, err := parseNonNegativeDurationSetting(getenv("MESSAGE_GAP_JITTER"), defaultGapJitter)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_GAP_JITTER: %w", err)
	}

	gapBase := fixedGap
	if gapMode == message.GapModeLength {
		gapBase = lengthBase
	}
	gapPolicy, err := message.NewGapPolicy(gapMode, gapBase, perRune, jitter)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_GAP_MODE: %w", err)
	}

	timeout, err := parsePositiveDurationSetting(getenv("NAPCAT_TIMEOUT"), defaultNapcatTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("invalid NAPCAT_TIMEOUT: %w", err)
	}

	directoryRefresh, err := parsePositiveDurationSetting(getenv("NAPCAT_DIRECTORY_REFRESH"), defaultNapcatDirectoryRefresh)
	if err != nil {
		return Config{}, fmt.Errorf("invalid NAPCAT_DIRECTORY_REFRESH: %w", err)
	}

	directoryRetry, err := parsePositiveDurationSetting(getenv("NAPCAT_DIRECTORY_RETRY"), defaultNapcatDirectoryRetry)
	if err != nil {
		return Config{}, fmt.Errorf("invalid NAPCAT_DIRECTORY_RETRY: %w", err)
	}

	directoryTimeout, err := parsePositiveDurationSetting(getenv("NAPCAT_DIRECTORY_TIMEOUT"), defaultNapcatDirectoryTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("invalid NAPCAT_DIRECTORY_TIMEOUT: %w", err)
	}

	groupTempSession, err := parseBoolSetting(getenv("NAPCAT_GROUP_TEMP_SESSION"), true)
	if err != nil {
		return Config{}, fmt.Errorf("invalid NAPCAT_GROUP_TEMP_SESSION: %w", err)
	}

	return Config{
		HTTPAddr:  httpAddr,
		APIKey:    apiKey,
		SenderKey: senderKey,
		NapCat: napcat.Config{
			BaseURL:          baseURL,
			AccessToken:      strings.TrimSpace(getenv("NAPCAT_ACCESS_TOKEN")),
			Timeout:          timeout,
			DirectoryRefresh: directoryRefresh,
			DirectoryRetry:   directoryRetry,
			DirectoryTimeout: directoryTimeout,
			GroupTempSession: groupTempSession,
		},
		Manager: message.Config{
			QueueSize:    queueSize,
			SendTimeout:  sendTimeout,
			MaxAttempts:  maxAttempts,
			RetryBackoff: retryBackoff,
			MinGap:       minGap,
		},
		Gap: gapPolicy,
	}, nil
}

func parsePositiveIntSetting(raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, errors.New("must be positive")
	}
	return value, nil
}

func parsePositiveDurationSetting(raw string, fallback time.Duration) (time.Duration, error) {
	value, err := parseDurationSetting(raw, fallback)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, errors.New("must be positive")
	}
	return value, nil
}

func parseNonNegativeDurationSetting(raw string, fallback time.Duration) (time.Duration, error) {
	value, err := parseDurationSetting(raw, fallback)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, errors.New("must not be negative")
	}
	return value, nil
}

func parseDurationSetting(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	return time.ParseDuration(raw)
}

func parseBoolSetting(raw string, fallback bool) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseBool(raw)
}
