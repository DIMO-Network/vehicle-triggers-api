package controllers

import (
	"errors"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v4"
	"strings"
)

// ExtractDeveloperLicenseFromToken extracts the "aud" field from a JWT
func ExtractDeveloperLicenseFromToken(tokenString string) (string, error) {
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return "", errors.New("error parsing token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("invalid claims type")
	}

	developerLicense, ok := claims["aud"].(string)
	if !ok {
		return "", errors.New("developer license not found in JWT")
	}

	return developerLicense, nil
}

// JWTMiddleware is a Fiber middleware for extracting and using the "aud" field from a JWT
func JWTMiddleware(c *fiber.Ctx) error {
	tokenString := c.Get("Authorization")
	if tokenString == "" || !strings.HasPrefix(tokenString, "Bearer ") {
		return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized")
	}

	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	developerLicense, err := ExtractDeveloperLicenseFromToken(tokenString)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).SendString("Unauthorized: " + err.Error())
	}

	c.Locals("developer_license", developerLicense)

	return c.Next()
}
