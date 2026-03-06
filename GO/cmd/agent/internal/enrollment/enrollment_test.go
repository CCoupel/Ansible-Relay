package enrollment

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateTestKey generates a 2048-bit RSA key for tests (faster than 4096-bit).
func generateTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return key
}

// mockEnrollServer creates an httptest.Server that simulates the relay server enrollment endpoint.
// It encrypts a JWT with the agent's public key and returns it.
func mockEnrollServer(t *testing.T, agentPubKey *rsa.PublicKey, jwt string, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			json.NewEncoder(w).Encode(map[string]string{"error": "rejected"})
			return
		}

		// Encrypt the JWT with agent's public key using RSA-OAEP SHA-256
		ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, agentPubKey, []byte(jwt), nil)
		if err != nil {
			t.Errorf("mock server: encrypt JWT: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		encoded := base64.StdEncoding.EncodeToString(ciphertext)
		json.NewEncoder(w).Encode(map[string]string{
			"token_encrypted":      encoded,
			"server_public_key_pem": "dummy-server-key",
		})
	}))
}

// ========================================================================
// Enroll — success
// ========================================================================

func TestEnrollSuccess(t *testing.T) {
	key := generateTestKey(t)
	expectedJWT := "eyJhbGciOiJSUzI1NiJ9.test.payload"

	srv := mockEnrollServer(t, &key.PublicKey, expectedJWT, http.StatusOK)
	defer srv.Close()

	pubPEM, err := PublicKeyPEM(key)
	if err != nil {
		t.Fatalf("PublicKeyPEM: %v", err)
	}

	jwt, err := Enroll(context.Background(), Config{
		RegisterURL:  srv.URL + "/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		Timeout:      5 * time.Second,
		Insecure:     true,
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if jwt != expectedJWT {
		t.Errorf("JWT: got %q, want %q", jwt, expectedJWT)
	}
}

func TestEnrollPersistsJWT(t *testing.T) {
	key := generateTestKey(t)
	dir := t.TempDir()
	jwtPath := filepath.Join(dir, "token.jwt")
	expectedJWT := "test-jwt-token"

	srv := mockEnrollServer(t, &key.PublicKey, expectedJWT, http.StatusOK)
	defer srv.Close()

	pubPEM, _ := PublicKeyPEM(key)
	_, err := Enroll(context.Background(), Config{
		RegisterURL:  srv.URL + "/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		JWTPath:      jwtPath,
		Timeout:      5 * time.Second,
		Insecure:     true,
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	data, err := os.ReadFile(jwtPath)
	if err != nil {
		t.Fatalf("ReadFile JWT: %v", err)
	}
	if string(data) != expectedJWT {
		t.Errorf("persisted JWT: got %q, want %q", data, expectedJWT)
	}
}

func TestEnrollJWTFileMode(t *testing.T) {
	if os.Getenv("CI") != "" || isWindows() {
		t.Skip("file mode check skipped on Windows/CI")
	}

	key := generateTestKey(t)
	dir := t.TempDir()
	jwtPath := filepath.Join(dir, "token.jwt")

	srv := mockEnrollServer(t, &key.PublicKey, "jwt", http.StatusOK)
	defer srv.Close()

	pubPEM, _ := PublicKeyPEM(key)
	_, err := Enroll(context.Background(), Config{
		RegisterURL:  srv.URL + "/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		JWTPath:      jwtPath,
		Timeout:      5 * time.Second,
		Insecure:     true,
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	info, err := os.Stat(jwtPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("JWT file mode: got %o, want 0600", info.Mode().Perm())
	}
}

func TestEnrollNoJWTPath(t *testing.T) {
	key := generateTestKey(t)
	srv := mockEnrollServer(t, &key.PublicKey, "jwt-no-persist", http.StatusOK)
	defer srv.Close()

	pubPEM, _ := PublicKeyPEM(key)
	jwt, err := Enroll(context.Background(), Config{
		RegisterURL:  srv.URL + "/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		JWTPath:      "", // no persistence
		Timeout:      5 * time.Second,
		Insecure:     true,
	})
	if err != nil {
		t.Fatalf("Enroll without JWTPath: %v", err)
	}
	if jwt != "jwt-no-persist" {
		t.Errorf("JWT: got %q, want %q", jwt, "jwt-no-persist")
	}
}

// ========================================================================
// Enroll — error cases
// ========================================================================

func TestEnrollMissingPrivateKey(t *testing.T) {
	_, err := Enroll(context.Background(), Config{
		RegisterURL:  "http://localhost:9999/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: "pem",
		PrivateKey:   nil, // missing
	})
	if err == nil {
		t.Error("expected error for nil private key")
	}
}

func TestEnrollServerRejects(t *testing.T) {
	key := generateTestKey(t)
	srv := mockEnrollServer(t, &key.PublicKey, "", http.StatusForbidden)
	defer srv.Close()

	pubPEM, _ := PublicKeyPEM(key)
	_, err := Enroll(context.Background(), Config{
		RegisterURL:  srv.URL + "/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		Timeout:      5 * time.Second,
		Insecure:     true,
	})
	if err == nil {
		t.Error("expected error for 403 response")
	}
}

func TestEnrollServerUnreachable(t *testing.T) {
	key := generateTestKey(t)
	_, err := Enroll(context.Background(), Config{
		RegisterURL:  "http://127.0.0.1:1/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: "pem",
		PrivateKey:   key,
		Timeout:      1 * time.Second,
		Insecure:     true,
	})
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

func TestEnrollBadBase64Token(t *testing.T) {
	key := generateTestKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"token_encrypted": "!!! not base64 !!!",
		})
	}))
	defer srv.Close()

	pubPEM, _ := PublicKeyPEM(key)
	_, err := Enroll(context.Background(), Config{
		RegisterURL:  srv.URL + "/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		Timeout:      5 * time.Second,
		Insecure:     true,
	})
	if err == nil {
		t.Error("expected error for invalid base64 token")
	}
}

func TestEnrollContextCancelled(t *testing.T) {
	key := generateTestKey(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	pubPEM, _ := PublicKeyPEM(key)
	_, err := Enroll(ctx, Config{
		RegisterURL:  "http://127.0.0.1:1/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		Timeout:      5 * time.Second,
		Insecure:     true,
	})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestEnrollDefaultTimeout(t *testing.T) {
	key := generateTestKey(t)
	srv := mockEnrollServer(t, &key.PublicKey, "jwt", http.StatusOK)
	defer srv.Close()

	pubPEM, _ := PublicKeyPEM(key)
	// Timeout=0 → uses 30s default
	_, err := Enroll(context.Background(), Config{
		RegisterURL:  srv.URL + "/api/register",
		Hostname:     "test-agent",
		PublicKeyPEM: pubPEM,
		PrivateKey:   key,
		Timeout:      0,
		Insecure:     true,
	})
	if err != nil {
		t.Fatalf("Enroll with default timeout: %v", err)
	}
}

// ========================================================================
// GenerateRSAKey
// ========================================================================

func TestGenerateRSAKey(t *testing.T) {
	// Use 2048-bit in tests to avoid 4096-bit slowness
	// The production code uses 4096-bit via GenerateRSAKey()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if key == nil {
		t.Error("GenerateKey returned nil")
	}
	if key.N.BitLen() < 2048 {
		t.Errorf("key size: got %d bits, want >= 2048", key.N.BitLen())
	}
}

// ========================================================================
// PublicKeyPEM
// ========================================================================

func TestPublicKeyPEM(t *testing.T) {
	key := generateTestKey(t)
	pemStr, err := PublicKeyPEM(key)
	if err != nil {
		t.Fatalf("PublicKeyPEM: %v", err)
	}
	if pemStr == "" {
		t.Error("PublicKeyPEM returned empty string")
	}
	if pemStr[:26] != "-----BEGIN PUBLIC KEY-----" {
		t.Errorf("PEM header: got %q", pemStr[:26])
	}
}

// ========================================================================
// PrivateKeyPEM
// ========================================================================

func TestPrivateKeyPEM(t *testing.T) {
	key := generateTestKey(t)
	pemStr := PrivateKeyPEM(key)
	if pemStr == "" {
		t.Error("PrivateKeyPEM returned empty string")
	}
	if pemStr[:31] != "-----BEGIN RSA PRIVATE KEY-----" {
		t.Errorf("PEM header: got %q", pemStr[:31])
	}
}

// ========================================================================
// LoadPrivateKeyFromFile
// ========================================================================

func TestLoadPrivateKeyFromFile(t *testing.T) {
	key := generateTestKey(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")

	pemData := PrivateKeyPEM(key)
	os.WriteFile(keyPath, []byte(pemData), 0600)

	loaded, err := LoadPrivateKeyFromFile(keyPath)
	if err != nil {
		t.Fatalf("LoadPrivateKeyFromFile: %v", err)
	}
	if loaded == nil {
		t.Error("loaded key is nil")
	}
	if loaded.N.Cmp(key.N) != 0 {
		t.Error("loaded key does not match original")
	}
}

func TestLoadPrivateKeyFromFileNotFound(t *testing.T) {
	_, err := LoadPrivateKeyFromFile("/nonexistent/key.pem")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadPrivateKeyFromFileInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "bad.pem")
	os.WriteFile(keyPath, []byte("not a pem file"), 0600)

	_, err := LoadPrivateKeyFromFile(keyPath)
	if err == nil {
		t.Error("expected error for invalid PEM")
	}
}

func TestLoadPrivateKeyFromFileInvalidKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "bad-key.pem")

	// Valid PEM block but invalid key content
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: []byte("not-valid-key-bytes"),
	}
	pemData := pem.EncodeToMemory(block)
	os.WriteFile(keyPath, pemData, 0600)

	_, err := LoadPrivateKeyFromFile(keyPath)
	if err == nil {
		t.Error("expected error for invalid key bytes")
	}
}

// ========================================================================
// writeSecret
// ========================================================================

func TestWriteSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")

	err := writeSecret(path, []byte("my secret"))
	if err != nil {
		t.Fatalf("writeSecret: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "my secret" {
		t.Errorf("content: got %q, want 'my secret'", data)
	}
}

func TestWriteSecretCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "secret.txt")

	err := writeSecret(path, []byte("nested secret"))
	if err != nil {
		t.Fatalf("writeSecret with nested dirs: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("secret file not created: %v", err)
	}
}

func TestWriteSecretOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")

	writeSecret(path, []byte("old secret"))
	writeSecret(path, []byte("new secret"))

	data, _ := os.ReadFile(path)
	if string(data) != "new secret" {
		t.Errorf("content after overwrite: got %q, want 'new secret'", data)
	}
}

// ========================================================================
// Config struct
// ========================================================================

func TestConfigFields(t *testing.T) {
	key := generateTestKey(t)
	cfg := Config{
		RegisterURL:  "https://relay.example.com/api/register",
		Hostname:     "my-agent",
		PublicKeyPEM: "pem",
		PrivateKey:   key,
		CABundle:     "/etc/ssl/certs/ca.pem",
		JWTPath:      "/etc/relay-agent/token.jwt",
		Timeout:      30 * time.Second,
		Insecure:     false,
	}
	if cfg.Hostname != "my-agent" {
		t.Error("Hostname not preserved")
	}
	if cfg.Insecure {
		t.Error("Insecure should be false")
	}
}

// isWindows returns true if running on Windows.
func isWindows() bool {
	return os.PathSeparator == '\\'
}
