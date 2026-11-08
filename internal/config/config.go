// Package config loads and validates the gateway's environment configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"message-gateway/internal/message"
)

const (
	defaultHTTPAddr = ":8081"

	defaultWebSocketMaxConnections = 16
	defaultStatusCapacity          = 4096

	defaultQueueSize    = 100
	defaultSendTimeout  = 30 * time.Second
	defaultMaxAttempts  = 3
	defaultRetryBackoff = 2 * time.Second

	defaultGapFixed   = 3 * time.Second
	defaultGapLength  = time.Second
	defaultGapPerRune = 50 * time.Millisecond
	defaultGapJitter  = time.Second
	defaultGapMin     = time.Second

	defaultAccountTimeout   = 10 * time.Second
	defaultDirectoryRefresh = 30 * time.Minute
	defaultDirectoryRetry   = 5 * time.Second
	defaultDirectoryTimeout = 60 * time.Second
	defaultGroupTempSession = true
)

var accountNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Account is one outbound endpoint the gateway can send through. The gateway
// does not know which platform account is logged in behind Address.
type Account struct {
	Name             string
	Driver           string
	Address          string
	AccessToken      string
	Timeout          time.Duration
	DirectoryRefresh time.Duration
	DirectoryRetry   time.Duration
	DirectoryTimeout time.Duration
	GroupTempSession bool
}

// Config is the validated runtime configuration for the gateway.
type Config struct {
	HTTPAddr                string
	APIKey                  string
	WebSocketMaxConnections int
	StatusCapacity          int
	Accounts                []Account
	Manager                 message.Config
	Gap                     message.GapPolicy
}

// Load reads and validates the gateway environment. MESSAGE_ACCOUNTS_FILE is
// required because account definitions live in the JSON file, not in the
// environment.
func Load(getenv func(string) string) (Config, error) {
	apiKey := strings.TrimSpace(getenv("MESSAGE_GATEWAY_API_KEY"))
	if apiKey == "" {
		return Config{}, errors.New("MESSAGE_GATEWAY_API_KEY is required")
	}

	httpAddr := strings.TrimSpace(getenv("HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}

	webSocketMaxConnections, err := parsePositiveIntSetting(
		getenv("MESSAGE_WS_MAX_CONNECTIONS"),
		defaultWebSocketMaxConnections,
	)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_WS_MAX_CONNECTIONS: %w", err)
	}

	statusCapacity, err := parsePositiveIntSetting(getenv("MESSAGE_STATUS_CAPACITY"), defaultStatusCapacity)
	if err != nil {
		return Config{}, fmt.Errorf("invalid MESSAGE_STATUS_CAPACITY: %w", err)
	}

	accountsFile := strings.TrimSpace(getenv("MESSAGE_ACCOUNTS_FILE"))
	if accountsFile == "" {
		return Config{}, errors.New("MESSAGE_ACCOUNTS_FILE is required")
	}

	accounts, err := loadAccounts(accountsFile)
	if err != nil {
		return Config{}, err
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

	return Config{
		HTTPAddr:                httpAddr,
		APIKey:                  apiKey,
		WebSocketMaxConnections: webSocketMaxConnections,
		StatusCapacity:          statusCapacity,
		Accounts:                accounts,
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

type fileConfig struct {
	Accounts []fileAccount `json:"accounts"`
}

type fileAccount struct {
	Name             string `json:"name"`
	Driver           string `json:"driver"`
	Address          string `json:"address"`
	AccessToken      string `json:"access_token"`
	Timeout          string `json:"timeout"`
	DirectoryRefresh string `json:"directory_refresh"`
	DirectoryRetry   string `json:"directory_retry"`
	DirectoryTimeout string `json:"directory_timeout"`
	GroupTempSession *bool  `json:"group_temp_session"`
}

func loadAccounts(path string) ([]Account, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read accounts file %s: %w", path, err)
	}

	var fc fileConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fc); err != nil {
		return nil, fmt.Errorf("parse accounts file %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("accounts file %s: must contain a single JSON object", path)
	}

	if len(fc.Accounts) == 0 {
		return nil, fmt.Errorf("accounts file %s: at least one entry in \"accounts\" is required", path)
	}

	seen := make(map[string]struct{}, len(fc.Accounts))
	accounts := make([]Account, 0, len(fc.Accounts))
	for i, fa := range fc.Accounts {
		account, err := buildAccount(fa)
		if err != nil {
			return nil, fmt.Errorf("accounts file %s: account %d: %w", path, i+1, err)
		}
		if _, ok := seen[account.Name]; ok {
			return nil, fmt.Errorf("accounts file %s: duplicate account name %q", path, account.Name)
		}
		seen[account.Name] = struct{}{}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

func buildAccount(fa fileAccount) (Account, error) {
	name := strings.TrimSpace(fa.Name)
	if !accountNamePattern.MatchString(name) {
		return Account{}, fmt.Errorf(
			"invalid name %q: must match %s",
			name,
			accountNamePattern,
		)
	}

	driver := strings.ToLower(strings.TrimSpace(fa.Driver))
	if driver != "napcat" {
		return Account{}, fmt.Errorf(
			"unsupported driver %q (supported: napcat)",
			driver,
		)
	}

	address, err := validateAddress(fa.Address)
	if err != nil {
		return Account{}, err
	}

	timeout, err := parsePositiveDurationField("timeout", fa.Timeout, defaultAccountTimeout)
	if err != nil {
		return Account{}, err
	}

	directoryRefresh, err := parsePositiveDurationField(
		"directory_refresh",
		fa.DirectoryRefresh,
		defaultDirectoryRefresh,
	)
	if err != nil {
		return Account{}, err
	}

	directoryRetry, err := parsePositiveDurationField(
		"directory_retry",
		fa.DirectoryRetry,
		defaultDirectoryRetry,
	)
	if err != nil {
		return Account{}, err
	}

	directoryTimeout, err := parsePositiveDurationField(
		"directory_timeout",
		fa.DirectoryTimeout,
		defaultDirectoryTimeout,
	)
	if err != nil {
		return Account{}, err
	}

	groupTempSession := defaultGroupTempSession
	if fa.GroupTempSession != nil {
		groupTempSession = *fa.GroupTempSession
	}

	accessToken := strings.TrimSpace(fa.AccessToken)
	if driver == "napcat" && accessToken == "" {
		return Account{}, errors.New("access_token is required for napcat")
	}

	return Account{
		Name:             name,
		Driver:           driver,
		Address:          address,
		AccessToken:      accessToken,
		Timeout:          timeout,
		DirectoryRefresh: directoryRefresh,
		DirectoryRetry:   directoryRetry,
		DirectoryTimeout: directoryTimeout,
		GroupTempSession: groupTempSession,
	}, nil
}

func validateAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if address == "" {
		return "", errors.New("address is required")
	}

	parsed, err := url.Parse(address)
	if err != nil {
		return "", fmt.Errorf("parse address: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("address scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("address host is required")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("address must not contain a query or fragment")
	}
	if parsed.User != nil {
		return "", errors.New("address must not contain credentials")
	}

	return strings.TrimRight(address, "/"), nil
}

func parsePositiveDurationField(name, raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
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
