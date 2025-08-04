package controllers

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/gateways"

	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v4"
	"github.com/rs/zerolog"
)

// JWTMiddleware extracts the "ethereum_address" from the JWT,
// verifies it against the identity API, and stores it in the request context.
func JWTMiddleware(store db.Store, identityAPI gateways.IdentityAPI, logger zerolog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tokenString := c.Get("Authorization")
		if tokenString == "" || !strings.HasPrefix(tokenString, "Bearer ") {
			return richerrors.Error{
				ExternalMsg: "Authorization header missing or malformed",
				Code:        fiber.StatusUnauthorized,
			}
		}
		// Remove the "Bearer " prefix.
		tokenString = strings.TrimPrefix(tokenString, "Bearer ")

		// Extract the ethereum address (developer license) from the JWT.
		developerLicenseStr, err := ExtractDeveloperLicenseFromToken(tokenString)
		if err != nil {
			return richerrors.Error{
				ExternalMsg: "Failed to extract developer license",
				Err:         err,
				Code:        fiber.StatusUnauthorized,
			}
		}

		// Verify that this developer license exists on identity API and in our DB.
		if err := auth.EnsureDeveloperLicenseExists(developerLicenseStr, identityAPI, store, logger); err != nil {
			return fmt.Errorf("failed to ensure developer license exists: %w", err)
		}

		// Remove "0x" prefix and decode the hex string.
		licenseHex := strings.TrimPrefix(developerLicenseStr, "0x")
		developerLicenseBytes, err := hex.DecodeString(licenseHex)
		if err != nil {
			return richerrors.Error{
				ExternalMsg: "Invalid developer license format",
				Err:         err,
				Code:        fiber.StatusUnauthorized,
			}
		}

		// Store the decoded developer license bytes in the request context.
		c.Locals("developer_license_address", developerLicenseBytes)
		return c.Next()
	}
}

// ExtractDeveloperLicenseFromToken extracts the "ethereum_address" field from the JWT.
func ExtractDeveloperLicenseFromToken(tokenString string) (string, error) {
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		fmt.Println("DEBUG: Error parsing token:", err)
		return "", err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		fmt.Println("DEBUG: Invalid claims type")
		return "", errors.New("invalid claims type")
	}

	ethAddress, ok := claims["ethereum_address"].(string)
	if !ok {
		fmt.Println("DEBUG: Ethereum address not found in JWT claims")
		return "", errors.New("ethereum address not found in JWT")
	}

	fmt.Printf("DEBUG: Raw ethereum address extracted: %s\n", ethAddress)
	return ethAddress, nil
}
