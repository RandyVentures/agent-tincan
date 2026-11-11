package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestOwnerWebSetupPreservesExistingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "owner.json")
	const before = "existing private config"
	if err := os.WriteFile(p, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, ownerWebCmd(), "setup", "--config", p, "--public-url", "https://tincan.example.test/")
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("setup = %v", err)
	}
	after, err := os.ReadFile(p)
	if err != nil || string(after) != before {
		t.Fatalf("existing file changed: %v", err)
	}
}

func TestOwnerWebHashPasswordUsesStdinWithoutPrintingPassphrase(t *testing.T) {
	const pass = "fixture-owner-passphrase-only"
	cmd := ownerWebCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(pass + "\n"))
	cmd.SetArgs([]string{"hash-password", "--password-stdin"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), pass) {
		t.Fatal("passphrase printed")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(strings.TrimSpace(out.String())), []byte(pass)); err != nil {
		t.Fatalf("hash output invalid: %v", err)
	}
}
