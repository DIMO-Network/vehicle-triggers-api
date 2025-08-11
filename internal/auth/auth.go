package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/ethereum/go-ethereum/common"
	jwtware "github.com/gofiber/contrib/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// UserJwtKey is the Fiber context key for the developer license JWT.
	UserJwtKey = "developer_license_token"
)

// Token is the token for the user.
type Token struct {
	jwt.RegisteredClaims
	CustomDexClaims
}

// CustomDexClaims is the custom claims for the token.
type CustomDexClaims struct {
	ProviderID      string         `json:"provider_id"`
	AtHash          string         `json:"at_hash"`
	EmailVerified   bool           `json:"email_verified"`
	EthereumAddress common.Address `json:"ethereum_address"`
}

type IdentityClient interface {
	IsDevLicense(ctx context.Context, ethAddr common.Address) (bool, error)
}

// Middleware is the middleware for Dex JWT authentication.
func Middleware(settings *config.Settings) fiber.Handler {
	return jwtware.New(jwtware.Config{
		JWKSetURLs: []string{settings.JWKKeySetURL},
		Claims:     &Token{},
		ContextKey: string(UserJwtKey),
	})
}

// NewDevLicenseValidator validates whether the jwt is coming from DIMO mobile or if it represents a valid developer license
func NewDevLicenseValidator(idSvc IdentityClient) fiber.Handler {
	return func(c *fiber.Ctx) error {
		token, err := GetDexJWT(c)
		if err != nil {
			return richerrors.Error{
				Code:        http.StatusInternalServerError,
				Err:         err,
				ExternalMsg: "failed to retrieve dex jwt",
			}
		}

		valid, err := idSvc.IsDevLicense(c.Context(), token.EthereumAddress)
		if err != nil {
			return fmt.Errorf("failed to check if dev license: %w", err)
		}

		if !valid {
			return richerrors.Error{
				Code:        http.StatusForbidden,
				Err:         fmt.Errorf("not a dev license: %s", token.EthereumAddress),
				ExternalMsg: "JWT is not from a developer license",
			}
		}
		return c.Next()
	}
}

// GetDexJWT returns the dex jwt from the context.
func GetDexJWT(c *fiber.Ctx) (*Token, error) {
	localValue := c.Locals(UserJwtKey)
	if localValue == nil {
		return nil, fmt.Errorf("no value found for user jwt key in context: %s", UserJwtKey)
	}
	v5Token, ok := localValue.(*jwt.Token)
	if !ok {
		return nil, fmt.Errorf("unexpected type for user jwt key in context: %T", localValue)
	}
	token, ok := v5Token.Claims.(*Token)
	if !ok {
		return nil, fmt.Errorf("unexpected type for user jwt key claims in context: %T", v5Token.Claims)
	}
	return token, nil
}
