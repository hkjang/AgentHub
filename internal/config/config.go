package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	EnvPostgresDSN       = "AGENTHUB_POSTGRES_DSN"
	EnvBootstrapAdmin    = "AGENTHUB_BOOTSTRAP_ADMIN"
	EnvBootstrapPassword = "AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD"
	EnvEncryptionKey     = "AGENTHUB_ENCRYPTION_KEY"
)

type Config struct {
	PostgresDSN       string
	BootstrapAdmin    string
	BootstrapPassword string
	EncryptionKey     []byte
	ListenAddress     string
}

func Load() (Config, error) {
	cfg := Config{
		PostgresDSN:       strings.TrimSpace(os.Getenv(EnvPostgresDSN)),
		BootstrapAdmin:    strings.TrimSpace(os.Getenv(EnvBootstrapAdmin)),
		BootstrapPassword: os.Getenv(EnvBootstrapPassword),
		ListenAddress:     ":8080",
	}
	var missing []string
	if cfg.PostgresDSN == "" {
		missing = append(missing, EnvPostgresDSN)
	}
	if cfg.BootstrapAdmin == "" {
		missing = append(missing, EnvBootstrapAdmin)
	}
	if cfg.BootstrapPassword == "" {
		missing = append(missing, EnvBootstrapPassword)
	}
	keyText := strings.TrimSpace(os.Getenv(EnvEncryptionKey))
	if keyText == "" {
		missing = append(missing, EnvEncryptionKey)
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if len(cfg.BootstrapPassword) < 12 {
		return Config{}, errors.New("bootstrap administrator password must contain at least 12 characters")
	}
	key, err := decodeKey(keyText)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvEncryptionKey, err)
	}
	cfg.EncryptionKey = key
	return cfg, nil
}

func decodeKey(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(value) == 32 {
		return []byte(value), nil
	}
	return nil, errors.New("must be exactly 32 bytes encoded as base64, hexadecimal, or raw text")
}
