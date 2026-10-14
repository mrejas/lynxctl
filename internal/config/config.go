// Package config handles profiles, the configuration file and credential
// storage.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	// AppName is used for the configuration directory and keyring service.
	AppName = "iotopenctl"
	// DefaultURL is the server used when nothing else is configured.
	DefaultURL = "https://lynx.iotopen.se"
	// DefaultProfile is the profile used when nothing else is selected.
	DefaultProfile = "default"

	configFileName = "config.yaml"
)

// AuthMethod tells how the secret of a profile was obtained.
type AuthMethod string

const (
	// AuthPassword means the secret is a session token from a password login.
	AuthPassword AuthMethod = "password"
	// AuthAPIKey means the secret is an API key supplied by the user.
	AuthAPIKey AuthMethod = "api-key"
)

// Config is the content of config.yaml.
type Config struct {
	CurrentProfile string              `yaml:"current-profile,omitempty"`
	Profiles       map[string]*Profile `yaml:"profiles,omitempty"`

	path string
}

// Profile describes one IoT Open environment. Secrets are never stored here.
type Profile struct {
	URL          string        `yaml:"url"`
	Username     string        `yaml:"username,omitempty"`
	Auth         AuthMethod    `yaml:"auth,omitempty"`
	Installation *Installation `yaml:"installation,omitempty"`
}

// Installation is the current installation of a profile. Name and client ID
// are cached so that showing them does not require an API call.
type Installation struct {
	ID       int64  `yaml:"id" json:"id"`
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`
	ClientID int64  `yaml:"client-id,omitempty" json:"client_id,omitempty"`
}

// Dir returns the configuration directory. IOTOPEN_CONFIG_DIR overrides the
// platform default.
func Dir() (string, error) {
	if dir := Getenv("CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine configuration directory: %w", err)
	}
	return filepath.Join(base, AppName), nil
}

// Load reads config.yaml from dir. A missing file gives an empty config.
func Load(dir string) (*Config, error) {
	cfg := &Config{path: filepath.Join(dir, configFileName)}
	data, err := os.ReadFile(cfg.path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration file %s: %w", cfg.path, err)
	}
	return cfg, nil
}

// Path returns the location of the configuration file.
func (c *Config) Path() string {
	return c.path
}

// Save writes the configuration atomically with owner-only permissions.
func (c *Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(c.path, data)
}

// Profile returns the named profile, or nil if it does not exist.
func (c *Config) Profile(name string) *Profile {
	return c.Profiles[name]
}

// EnsureProfile returns the named profile, creating it if needed.
func (c *Config) EnsureProfile(name string) *Profile {
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	p, ok := c.Profiles[name]
	if !ok {
		p = &Profile{}
		c.Profiles[name] = p
	}
	return p
}

// DeleteProfile removes the named profile. If it was the current profile,
// no profile is current afterwards.
func (c *Config) DeleteProfile(name string) {
	delete(c.Profiles, name)
	if c.CurrentProfile == name {
		c.CurrentProfile = ""
	}
}

// ProfileNames returns all profile names in sorted order.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
