package e2e_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

type mockAuthServer struct {
	server                      *httptest.Server
	signer                      jose.Signer
	jwks                        jose.JSONWebKey
	defaultClaims               Token
	VehicleContractAddress      string
	ManufacturerContractAddress string
}

func setupAuthServer(t *testing.T) *mockAuthServer {
	t.Helper()

	// Generate RSA key
	sk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	// Generate key ID
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("Failed to generate key ID: %v", err)
	}
	keyID := hex.EncodeToString(b)

	// Create JWK
	jwk := jose.JSONWebKey{
		Key:       sk.Public(),
		KeyID:     keyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}

	// Create signer
	sig, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       sk,
	}, &jose.SignerOptions{
		ExtraHeaders: map[jose.HeaderKey]any{
			"kid": keyID,
		},
	})
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}
	mockServer := &mockAuthServer{
		signer: sig,
		jwks:   jwk,
	}
	mockServer.defaultClaims.Issuer = "https://auth.dimo.zone"
	mockServer.defaultClaims.ProviderID = "web3"
	mockServer.defaultClaims.Subject = "CioweGQ4NDhBM2Y3NTAxOTc5MDY5RTFEQkMxN2YzNjYzNzlmMzQxODdFQTYSBHdlYjM"
	mockServer.defaultClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(1 * time.Hour))
	mockServer.defaultClaims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-1 * time.Hour))
	mockServer.defaultClaims.AtHash = "MOcfynR2IuZAuy11gKHmDA"
	mockServer.defaultClaims.EmailVerified = false

	// Create test server with only JWKS endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/keys" {
			http.NotFound(w, r)
			return
		}
		err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{jwk},
		})
		if err != nil {
			http.Error(w, "Failed to encode JWKS", http.StatusInternalServerError)
		}
	}))

	mockServer.server = server
	return mockServer
}

func (m *mockAuthServer) Sign(token Token) (string, error) {
	b, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("failed to marshal claims: %w", err)
	}

	out, err := m.signer.Sign(b)
	if err != nil {
		return "", fmt.Errorf("failed to sign claims: %w", err)
	}

	tokenStr, err := out.CompactSerialize()
	if err != nil {
		return "", fmt.Errorf("failed to serialize token: %w", err)
	}

	return tokenStr, nil
}

func (m *mockAuthServer) CreateToken(t *testing.T, devAddress common.Address) (string, error) {
	token := m.defaultClaims
	token.EthereumAddress = devAddress.String()
	token.Audience = []string{devAddress.String()}

	tokenStr, err := m.Sign(token)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenStr, nil
}

func (m *mockAuthServer) URL() string {
	return m.server.URL
}

func (m *mockAuthServer) Close() {
	m.server.Close()
}

// Token is the token for the user.
type Token struct {
	jwt.RegisteredClaims
	CustomDexClaims
}

// CustomDexClaims is the custom claims for the token.
type CustomDexClaims struct {
	ProviderID      string `json:"provider_id"`
	AtHash          string `json:"at_hash"`
	EmailVerified   bool   `json:"email_verified"`
	EthereumAddress string `json:"ethereum_address"`
}
