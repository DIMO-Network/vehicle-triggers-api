package controllers

import (
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v4"
	"strings"
)

// ExtractDeveloperLicenseFromToken extracts the "ethereum_address" field from a JWT.
func ExtractDeveloperLicenseFromToken(tokenString string) (string, error) {
	// Parsing the token without validating its signature
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		fmt.Println("DEBUG: Error parsing token:", err)
		return "", fmt.Errorf("error parsing token")
	}

	// Assert the type of the claims to access the data
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

	// Debug: Print the raw ethereum address extracted from the token
	fmt.Printf("DEBUG: Raw ethereum address extracted: %s\n", ethAddress)

	return ethAddress, nil
}

// JWTMiddleware is a Fiber middleware for extracting the "ethereum_address" field from a JWT,
// decoding it from hex, printing it for debugging, and storing it in the request context.
func JWTMiddleware(c *fiber.Ctx) error {
	// Get the Authorization header from the request
	tokenString := c.Get("Authorization")
	if tokenString == "" || !strings.HasPrefix(tokenString, "Bearer ") {
		fmt.Println("DEBUG: Authorization header missing or does not start with 'Bearer '")
		return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized")
	}

	// Trim the "Bearer " prefix
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	// Extract the ethereum_address from the JWT
	developerLicenseStr, err := ExtractDeveloperLicenseFromToken(tokenString)
	if err != nil {
		fmt.Printf("DEBUG: Error extracting developer license: %s\n", err.Error())
		return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: " + err.Error())
	}

	// Remove the "0x" prefix if present
	licenseHex := strings.TrimPrefix(developerLicenseStr, "0x")
	fmt.Printf("DEBUG: License hex after trimming 0x: %s\n", licenseHex)

	// Decode the hex string into bytes
	developerLicenseBytes, err := hex.DecodeString(licenseHex)
	if err != nil {
		fmt.Printf("DEBUG: Error decoding license hex: %s\n", err.Error())
		return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: invalid developer license format")
	}

	// Debug: Print the decoded developer license
	fmt.Printf("DEBUG: Decoded Developer License (bytes): %v\n", developerLicenseBytes)
	fmt.Printf("DEBUG: Developer License (hex): 0x%s\n", hex.EncodeToString(developerLicenseBytes))

	// Store the decoded developer license bytes in the request context
	c.Locals("developer_license_address", developerLicenseBytes)
	return c.Next()
}
