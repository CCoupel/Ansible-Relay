// Package enrollment gère l'enregistrement de l'agent auprès du relay server.
//
// Flow MVP (§8 ARCHITECTURE.md) :
//  1. POST /api/register { hostname, public_key_pem }
//  2. Serveur retourne { token_encrypted: base64(RSA-encrypt(JWT)) }
//  3. Agent déchiffre avec sa clef privée RSA → JWT
//  4. JWT stocké sur disque (mode 0600) pour les reconnexions
//
// Sécurité :
//   - TLS vérifié obligatoire (InsecureSkipVerify=false)
//   - Échec déchiffrement RSA → erreur fatale, pas de fallback
//   - JWT et clef privée créés avec os.OpenFile(O_CREATE, 0600) atomique
package enrollment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Config contient les paramètres nécessaires à l'enrollment.
type Config struct {
	// RegisterURL est le endpoint d'enregistrement (https://relay.example.com/api/register).
	RegisterURL string
	// Hostname identifie l'agent auprès du serveur.
	Hostname string
	// PublicKeyPEM est la clef publique RSA PEM de l'agent.
	PublicKeyPEM string
	// PrivateKey est la clef privée RSA pour déchiffrer le JWT retourné.
	PrivateKey *rsa.PrivateKey
	// CABundle est le chemin vers un CA bundle PEM custom (vide = store système).
	CABundle string
	// JWTPath est le chemin de stockage du JWT (ex: /etc/relay-agent/token.jwt).
	JWTPath string
	// Timeout de la requête HTTP (défaut : 30s).
	Timeout time.Duration
	// Insecure désactive la vérification TLS (tests uniquement).
	Insecure bool
}

// enrollRequest est le corps de POST /api/register.
type enrollRequest struct {
	Hostname     string `json:"hostname"`
	PublicKeyPEM string `json:"public_key_pem"`
}

// enrollResponse est la réponse de POST /api/register.
type enrollResponse struct {
	TokenEncrypted    string `json:"token_encrypted"`
	ServerPublicKeyPEM string `json:"server_public_key_pem"`
}

// Enroll enregistre l'agent et retourne le JWT déchiffré.
//
// L'agent effectue un POST /api/register avec son hostname et sa clef publique.
// Le serveur retourne un JWT chiffré avec la clef publique de l'agent.
// L'agent le déchiffre avec sa clef privée et le stocke sur disque (0600).
//
// Retourne une erreur si :
//   - La requête HTTP échoue ou retourne != 200
//   - Le déchiffrement RSA échoue (jamais de fallback token brut)
//   - L'écriture du fichier JWT échoue
func Enroll(ctx context.Context, cfg Config) (string, error) {
	if cfg.PrivateKey == nil {
		return "", errors.New("enrollment: private key is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	// Build TLS-verified HTTP client
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.Insecure, //nolint:gosec // tests only, guarded by flag
	}
	if !cfg.Insecure && cfg.CABundle != "" {
		pool, err := loadCABundle(cfg.CABundle)
		if err != nil {
			return "", fmt.Errorf("enrollment: load CA bundle: %w", err)
		}
		tlsCfg.RootCAs = pool
	}
	client := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}

	// Build and send POST /api/register
	body, err := json.Marshal(enrollRequest{
		Hostname:     cfg.Hostname,
		PublicKeyPEM: cfg.PublicKeyPEM,
	})
	if err != nil {
		return "", fmt.Errorf("enrollment: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.RegisterURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("enrollment: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("enrollment: POST %s: %w", cfg.RegisterURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return "", fmt.Errorf("enrollment: server rejected enrollment: HTTP %d %v", resp.StatusCode, errBody)
	}

	var result enrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("enrollment: decode response: %w", err)
	}

	// Decode base64 ciphertext
	ciphertext, err := base64.StdEncoding.DecodeString(result.TokenEncrypted)
	if err != nil {
		return "", fmt.Errorf("enrollment: decode base64 token: %w", err)
	}

	// Decrypt RSA-OAEP SHA-256 — matches server encryptWithPublicKey() (rsa.EncryptOAEP).
	// Failure is fatal, no plaintext fallback (§HAUT-1bis).
	jwtBytes, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, cfg.PrivateKey, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("enrollment: RSA-OAEP decrypt failed (key/token mismatch): %w", err)
	}
	jwt := string(jwtBytes)

	// Persist JWT with atomic 0600 creation
	if cfg.JWTPath != "" {
		if err := writeSecret(cfg.JWTPath, jwtBytes); err != nil {
			return "", fmt.Errorf("enrollment: persist JWT: %w", err)
		}
	}

	return jwt, nil
}

// writeSecret écrit content dans path avec mode 0600 de façon atomique.
// Le fichier est créé directement avec O_CREATE et perm 0600 — pas de fenêtre
// TOCTOU entre création et chmod (§HAUT-4).
func writeSecret(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// loadCABundle charge un fichier PEM et retourne un pool de CAs.
func loadCABundle(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no valid certificates in %s", path)
	}
	return pool, nil
}

// LoadPrivateKeyFromFile charge une clef privée RSA PEM depuis le disque.
func LoadPrivateKeyFromFile(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8
		k, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse private key: PKCS1: %v, PKCS8: %v", err, err2)
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not RSA")
		}
		return rsaKey, nil
	}
	return key, nil
}
