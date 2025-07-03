package controllers

import (
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/DIMO-Network/vehicle-events-api/internal/gateways"
	"strings"

	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/auth"
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
			fmt.Println("DEBUG: Authorization header missing or malformed")
			return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized")
		}
		// Remove the "Bearer " prefix.
		tokenString = strings.TrimPrefix(tokenString, "Bearer ")

		// Extract the ethereum address (developer license) from the JWT.
		developerLicenseStr, err := ExtractDeveloperLicenseFromToken(tokenString)
		if err != nil {
			fmt.Printf("DEBUG: Error extracting developer license: %s\n", err.Error())
			return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: " + err.Error())
		}

		// Verify that this developer license exists on identity API and in our DB.
		if err := auth.EnsureDeveloperLicenseExists(developerLicenseStr, identityAPI, store, logger); err != nil {
			return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: " + err.Error())
		}

		// Remove "0x" prefix and decode the hex string.
		licenseHex := strings.TrimPrefix(developerLicenseStr, "0x")
		developerLicenseBytes, err := hex.DecodeString(licenseHex)
		if err != nil {
			fmt.Printf("DEBUG: Error decoding license hex: %s\n", err.Error())
			return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: invalid developer license format")
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
