// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

const credentialsFileName = "credentials.yaml"

// ErrNoSecret is returned when no secret is stored for a profile.
var ErrNoSecret = errors.New("no stored credentials")

// SecretStore stores one secret (session token or API key) per profile.
type SecretStore interface {
	Get(profile string) (string, error)
	// Set stores the secret and returns a human-readable description of
	// where it ended up.
	Set(profile, secret string) (string, error)
	Delete(profile string) error
}

// DefaultStore returns the OS keyring with a file fallback. Setting
// IOTOPEN_NO_KEYRING disables the keyring entirely.
func DefaultStore(dir string) SecretStore {
	file := &FileStore{Path: filepath.Join(dir, credentialsFileName)}
	if Getenv("NO_KEYRING") != "" {
		return file
	}
	return &chainStore{keyring: keyringStore{service: AppName}, file: file}
}

// chainStore prefers the keyring and falls back to a file when the keyring
// is unavailable (headless Linux, containers, ...).
type chainStore struct {
	keyring keyringStore
	file    *FileStore
}

func (s *chainStore) Get(profile string) (string, error) {
	if secret, err := s.keyring.Get(profile); err == nil {
		return secret, nil
	}
	return s.file.Get(profile)
}

func (s *chainStore) Set(profile, secret string) (string, error) {
	where, err := s.keyring.Set(profile, secret)
	if err == nil {
		// Make sure an old file entry cannot shadow or outlive the new one.
		_ = s.file.Delete(profile)
		return where, nil
	}
	return s.file.Set(profile, secret)
}

func (s *chainStore) Delete(profile string) error {
	// Keyring errors are ignored: the keyring may simply be unavailable, in
	// which case the secret can only be in the file.
	_ = s.keyring.Delete(profile)
	if err := s.file.Delete(profile); err != nil && !errors.Is(err, ErrNoSecret) {
		return err
	}
	return nil
}

type keyringStore struct {
	service string
}

func (s keyringStore) Get(profile string) (string, error) {
	secret, err := keyring.Get(s.service, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoSecret
	}
	return secret, err
}

func (s keyringStore) Set(profile, secret string) (string, error) {
	if err := keyring.Set(s.service, profile, secret); err != nil {
		return "", err
	}
	return "the system keyring", nil
}

func (s keyringStore) Delete(profile string) error {
	err := keyring.Delete(s.service, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNoSecret
	}
	return err
}

// FileStore keeps secrets in a YAML file readable only by the owner.
type FileStore struct {
	Path string
}

func (s *FileStore) load() (map[string]string, error) {
	secrets := map[string]string{}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return secrets, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &secrets); err != nil {
		return nil, fmt.Errorf("invalid credentials file %s: %w", s.Path, err)
	}
	return secrets, nil
}

func (s *FileStore) save(secrets map[string]string) error {
	if len(secrets) == 0 {
		err := os.Remove(s.Path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := yaml.Marshal(secrets)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Path, data)
}

func (s *FileStore) Get(profile string) (string, error) {
	secrets, err := s.load()
	if err != nil {
		return "", err
	}
	secret, ok := secrets[profile]
	if !ok {
		return "", ErrNoSecret
	}
	return secret, nil
}

func (s *FileStore) Set(profile, secret string) (string, error) {
	secrets, err := s.load()
	if err != nil {
		return "", err
	}
	secrets[profile] = secret
	if err := s.save(secrets); err != nil {
		return "", err
	}
	return s.Path, nil
}

func (s *FileStore) Delete(profile string) error {
	secrets, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := secrets[profile]; !ok {
		return ErrNoSecret
	}
	delete(secrets, profile)
	return s.save(secrets)
}
