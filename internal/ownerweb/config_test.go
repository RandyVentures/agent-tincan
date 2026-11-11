package ownerweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func fixtureConfig(t *testing.T) Config {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-passphrase-only"), 12)
	if err != nil {
		t.Fatal(err)
	}
	return Config{Listen: "127.0.0.1:8788", PublicURL: "https://tincan.example.test/tincan/", Agent: "owner-web", Targets: []string{"muse"}, PasswordHash: string(hash)}
}

func TestConfigRefusesUnsafeDeployments(t *testing.T) {
	c := fixtureConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"public-listener", func(c *Config) { c.Listen = "0.0.0.0:8788" }},
		{"dns-listener", func(c *Config) { c.Listen = "localhost:8788" }},
		{"no-https", func(c *Config) { c.PublicURL = "http://tincan.example.test/" }},
		{"remote-dev", func(c *Config) { c.Dev = true; c.PublicURL = "http://tincan.example.test/" }},
		{"credentials-in-url", func(c *Config) { c.PublicURL = "https://u:p@tincan.example.test/" }},
		{"weak-hash", func(c *Config) { c.PasswordHash = "not-a-hash" }},
		{"same-agent-target", func(c *Config) { c.Targets = []string{c.Agent} }},
		{"duplicate-target", func(c *Config) { c.Targets = []string{"muse", "muse"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := c
			tc.change(&bad)
			if bad.Validate() == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestConfigFileMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner web is hosted with the relay on POSIX; file permissions require Linux or macOS")
	}
	c := fixtureConfig(t)
	p := filepath.Join(t.TempDir(), "owner.json")
	data, _ := json.Marshal(c)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("world-readable owner file accepted")
	}
	os.Chmod(p, 0600)
	if _, err := LoadConfig(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(data, []byte(` {"another":true}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("multiple JSON objects accepted")
	}
}

func TestSaveNewDoesNotOverwriteOwnerCredentials(t *testing.T) {
	c := fixtureConfig(t)
	p := filepath.Join(t.TempDir(), "private", "owner.json")
	if err := SaveNew(p, c); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Agent = "another-owner"
	if err := SaveNew(p, c); err == nil {
		t.Fatal("overwrote owner config")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing credentials changed")
	}
}

func TestOwnerCookieAndSessionExpiry(t *testing.T) {
	c := fixtureConfig(t)
	origin, prefix := c.location()
	s := &Server{cfg: c, origin: origin, prefix: prefix, cookie: "__Secure-tincan-owner", sessions: map[string]*session{}}
	w := httptest.NewRecorder()
	s.setCookie(w, "fixture", 300)
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/tincan/" || cookie.Domain != "" {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	s.sessions[digest("expired")] = &session{csrf: "csrf", expires: time.Now().Add(-time.Second)}
	r := httptest.NewRequest("GET", c.PublicURL+"api/session", nil)
	r.AddCookie(&http.Cookie{Name: s.cookie, Value: "expired"})
	if _, sess := s.authenticate(r); sess != nil {
		t.Fatal("expired session accepted")
	}
}

func TestRootHostedOwnerUsesHostOnlyCookiePrefix(t *testing.T) {
	c := fixtureConfig(t)
	c.PublicURL = "https://tincan.example.test/"
	l, err := NewLedger(ledgerDB(t, ":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(c, &RelayBackend{}, l)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", c.PublicURL+"api/login", strings.NewReader(`{"password":"fixture-passphrase-only"}`))
	r.Header.Set("Origin", "https://tincan.example.test")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tincan-UI", "1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login = %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "__Host-tincan-owner" || cookie.Path != "/" || cookie.Domain != "" || !cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("unsafe root cookie: %+v", cookie)
	}
}
