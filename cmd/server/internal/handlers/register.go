package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
)

// RegisterRequest represents agent enrollment request
type RegisterRequest struct {
	Hostname     string `json:"hostname"`
	PublicKeyPEM string `json:"public_key_pem"`
}

// RegisterResponse returns encrypted JWT and server public key
type RegisterResponse struct {
	TokenEncrypted      string `json:"token_encrypted"`
	ServerPublicKeyPEM  string `json:"server_public_key_pem"`
}

// RegisterAgent enrolls a relay-agent
func RegisterAgent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// TODO: Implement agent enrollment
	// 1. Validate request
	// 2. Check authorized_keys
	// 3. Issue JWT
	// 4. Encrypt with agent public key
	// 5. Store agent record
	
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"register endpoint - to be implemented"}`)
}

// TODO: Add AdminAuthorize, TokenRefresh handlers
