// Package ownerweb serves the owner's authenticated text workspace. It never
// exposes a relay handler, admin socket or credential to the browser.
package ownerweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	Listen       string   `json:"listen"`
	PublicURL    string   `json:"public_url"`
	Agent        string   `json:"agent"`
	Targets      []string `json:"targets"`
	PasswordHash string   `json:"password_hash"`
	Dev          bool     `json:"dev,omitempty"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var prefixRE = regexp.MustCompile(`^/([a-z0-9-]+/)*$`)

func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
		return c, errors.New("owner web config must be a regular private file (mode 0600), at most 16 KiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("owner web config: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("owner web config must contain one JSON object")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || port == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("owner web listen must be a literal loopback IP and port")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return errors.New("owner web public_url must be an absolute HTTPS URL without credentials, query or fragment")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if !prefixRE.MatchString(u.Path) {
		return errors.New("owner web URL path must end in / and contain only lowercase letters, digits, dashes and slashes")
	}
	if c.Dev {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("owner web dev mode requires an HTTP loopback public_url")
		}
	} else if u.Scheme != "https" {
		return errors.New("owner web requires HTTPS; dev is restricted to loopback")
	}
	if !nameRE.MatchString(c.Agent) || len(c.Targets) == 0 || len(c.Targets) > 8 {
		return errors.New("owner web needs an agent name and 1 to 8 allowed targets")
	}
	seen := map[string]bool{}
	for _, target := range c.Targets {
		if !nameRE.MatchString(target) || target == c.Agent || seen[target] {
			return errors.New("owner web target names must be distinct, valid and different from its agent")
		}
		seen[target] = true
	}
	cost, err := bcrypt.Cost([]byte(c.PasswordHash))
	minimum := 12
	if c.Dev {
		minimum = bcrypt.MinCost
	}
	if err != nil || cost < minimum || cost > 14 {
		return errors.New("owner web password hash must be bcrypt with cost 12 to 14 (dev permits test costs)")
	}
	return nil
}

func HashPassword(password []byte) (string, error) {
	if len(password) < 16 || len(password) > 72 {
		return "", errors.New("use a passphrase of 16 to 72 UTF-8 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword(password, 12)
	return string(hash), err
}

// SaveNew refuses to overwrite any existing path, including a symlink.
// Only a password hash is saved; callers must never persist the passphrase.
func SaveNew(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (c Config) location() (origin, prefix string) {
	u, _ := url.Parse(c.PublicURL)
	prefix = u.Path
	if prefix == "" {
		prefix = "/"
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), prefix
}
