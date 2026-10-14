// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 {
		t.Fatalf("new config has profiles: %v", cfg.Profiles)
	}
	p := cfg.EnsureProfile("dev")
	p.URL = "https://lynx-dev.iotopen.se"
	p.Auth = AuthPassword
	p.Installation = &Installation{ID: 42, Name: "Kök", ClientID: 1042}
	cfg.CurrentProfile = "dev"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(cfg.Path())
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config file permissions = %o, want 600", perm)
		}
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Profile("dev")
	if loaded.CurrentProfile != "dev" || got == nil || got.URL != p.URL || got.Auth != AuthPassword || *got.Installation != *p.Installation {
		t.Errorf("loaded config = %+v, profile %+v", loaded, got)
	}

	loaded.DeleteProfile("dev")
	if loaded.CurrentProfile != "" || loaded.Profile("dev") != nil {
		t.Errorf("DeleteProfile left %+v", loaded)
	}
}

func TestFileStore(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "credentials.yaml")}
	if _, err := s.Get("default"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("Get on empty store: %v", err)
	}
	if _, err := s.Set("default", "token-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("dev", "token-2"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("default"); err != nil || got != "token-1" {
		t.Errorf("Get(default) = %q, %v", got, err)
	}
	if err := s.Delete("default"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("default"); !errors.Is(err, ErrNoSecret) {
		t.Errorf("second Delete: %v", err)
	}
	if err := s.Delete("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty credentials file was not removed: %v", err)
	}
}

func TestGetenvPrefixes(t *testing.T) {
	t.Setenv("IOTOPEN_PROFILE", "")
	t.Setenv("LYNX_PROFILE", "lynx")
	if got := Getenv("PROFILE"); got != "lynx" {
		t.Errorf("Getenv falls back to LYNX_: got %q", got)
	}
	t.Setenv("IOTOPEN_PROFILE", "iotopen")
	if got := Getenv("PROFILE"); got != "iotopen" {
		t.Errorf("IOTOPEN_ takes precedence: got %q", got)
	}
}
