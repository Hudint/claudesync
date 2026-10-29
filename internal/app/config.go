// Package app implements claudesync: capturing the local Claude Code
// configuration into a git repository, syncing that repository, and applying
// it back to the local configuration directory.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Config holds the per-device paths and identity.
type Config struct {
	Home      string // user home directory
	ClaudeDir string // ~/.claude or $CLAUDE_CONFIG_DIR
	StateDir  string // ~/.claudesync or $CLAUDESYNC_HOME
	Host      string // device name used for hosts/<host>.json
	OS        string // runtime.GOOS
}

// RepoDir is the local clone of the config repository.
func (c *Config) RepoDir() string { return filepath.Join(c.StateDir, "repo") }

func (c *Config) statePath() string { return filepath.Join(c.StateDir, "state.json") }
func (c *Config) lockPath() string  { return filepath.Join(c.StateDir, "lock") }
func (c *Config) logPath() string   { return filepath.Join(c.StateDir, "claudesync.log") }
func (c *Config) backupDir() string { return filepath.Join(c.StateDir, "backups") }
func (c *Config) settingsPath() string {
	return filepath.Join(c.ClaudeDir, "settings.json")
}

// LoadConfig builds the configuration from the environment.
func LoadConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	c := &Config{Home: home, OS: runtime.GOOS}
	c.ClaudeDir = os.Getenv("CLAUDE_CONFIG_DIR")
	if c.ClaudeDir == "" {
		c.ClaudeDir = filepath.Join(home, ".claude")
	}
	c.StateDir = os.Getenv("CLAUDESYNC_HOME")
	if c.StateDir == "" {
		c.StateDir = filepath.Join(home, ".claudesync")
	}
	c.Host = os.Getenv("CLAUDESYNC_HOST")
	return c, nil
}

var hostSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

// SanitizeHost turns a hostname into a safe file name.
func SanitizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	h = strings.Trim(hostSanitizer.ReplaceAllString(h, "-"), "-")
	if h == "" {
		h = "device"
	}
	return h
}

func defaultHost() string {
	h, _ := os.Hostname()
	return SanitizeHost(h)
}

// DefaultLocalOnlyKeys are settings keys that never leave the device because
// they usually hold credentials or machine-specific values.
var DefaultLocalOnlyKeys = []string{
	"env",
	"apiKeyHelper",
	"awsAuthRefresh",
	"awsCredentialExport",
	"otelHeadersHelper",
	"forceLoginMethod",
	"forceLoginOrgUUID",
}

// RepoConfig is claudesync.json at the root of the config repository.
type RepoConfig struct {
	Version int `json:"version"`
	// Include lists extra paths relative to the Claude directory to sync.
	// A trailing slash marks a directory.
	Include []string `json:"include,omitempty"`
	// Exclude lists glob patterns (relative to the Claude directory) to skip.
	Exclude []string `json:"exclude,omitempty"`
	// LocalOnlyKeys are top-level settings.json keys that stay on each device.
	// When omitted, DefaultLocalOnlyKeys applies.
	LocalOnlyKeys *[]string `json:"localOnlyKeys,omitempty"`
	// SecretAllow lists regular expressions for lines the secret scan accepts.
	SecretAllow []string `json:"secretAllow,omitempty"`
	// Devices records every device and the profiles it belongs to.
	Devices map[string]DeviceConfig `json:"devices,omitempty"`
}

// DeviceConfig is one device's entry in claudesync.json.
type DeviceConfig struct {
	OS string `json:"os,omitempty"`
	// Profiles in priority order; new files and keys go into the first.
	Profiles []string `json:"profiles,omitempty"`
}

func (rc *RepoConfig) profilesFor(host string) []string {
	ps, _ := splitProfiles(rc.Devices[host].Profiles)
	return ps
}

func (rc *RepoConfig) localOnly() []string {
	if rc.LocalOnlyKeys == nil {
		return DefaultLocalOnlyKeys
	}
	return *rc.LocalOnlyKeys
}

const repoConfigName = "claudesync.json"

func loadRepoConfig(repoDir string) (*RepoConfig, error) {
	b, err := os.ReadFile(filepath.Join(repoDir, repoConfigName))
	if errors.Is(err, os.ErrNotExist) {
		return &RepoConfig{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var rc RepoConfig
	if err := json.Unmarshal(b, &rc); err != nil {
		return nil, fmt.Errorf("%s: %w", repoConfigName, err)
	}
	return &rc, nil
}
