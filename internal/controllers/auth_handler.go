package controllers

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"
)

func AuthMiddleware(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		log.Error().Msg("Authorization header missing")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Authorization header missing",
		})
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		log.Error().Msg("Invalid Authorization header format")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid Authorization header format",
		})
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		log.Error().Msg("Token missing")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Token missing",
		})
	}

	// Log the token for debugging only
	log.Debug().Str("token", token).Msg("Token extracted from Authorization header")

	c.Locals("token", token)

	return c.Next()
}
