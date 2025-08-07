package auth

import (
	"context"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/identity"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
)

// EnsureDeveloperLicenseExists verifies the clientID against identity API.
// If valid and not already in our DB, it inserts a new DeveloperLicense record.
func EnsureDeveloperLicenseExists(ctx context.Context, clientID string, identityClient *identity.Client, logger zerolog.Logger) error {
	valid, _, err := identityClient.VerifyDeveloperLicense(ctx, clientID)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to verify developer license with identity API",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}
	if !valid {
		return richerrors.Error{
			ExternalMsg: "Invalid developer license",
			Code:        fiber.StatusUnauthorized,
		}
	}

	return nil
}
