package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/gofiber/fiber/v2"
)

// verifyWebhookURL verifies that the target URL is valid and returns the verification token.
// It sends a POST request to the target URL with a dummy payload and verifies that the response contains the expected verification token.
func verifyWebhookURL(ctx context.Context, targetURL string, verificationToken string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewBuffer([]byte(`{"verification": "test"}`)))
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to verify webhook URL",
			Err:         fmt.Errorf("failed to create verification request: %w", err),
			Code:        fiber.StatusInternalServerError,
		}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to call target URL",
			Err:         fmt.Errorf("failed to call target URL: %w", err),
			Code:        fiber.StatusBadRequest,
		}
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return richerrors.Error{
			ExternalMsg: fmt.Sprintf("Target URL did not return status 200 (got %d)", resp.StatusCode),
			Code:        fiber.StatusBadRequest,
		}
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to read response from target URL",
			Err:         fmt.Errorf("failed to read response from target URL: %w", err),
			Code:        fiber.StatusInternalServerError,
		}
	}

	responseToken := strings.TrimSpace(string(bodyBytes))
	if responseToken != verificationToken {
		err := fmt.Errorf("verification token mismatch. Expected '%s', got '%s'", verificationToken, responseToken)
		return richerrors.Error{
			ExternalMsg: err.Error(),
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}

	return nil
}

func validateTargetURL(targetURL string) error {
	parsedURL, err := url.ParseRequestURI(targetURL)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Invalid webhook URL",
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}
	if parsedURL.Scheme != "https" {
		return richerrors.Error{
			ExternalMsg: "Webhook URL must be HTTPS",
			Code:        fiber.StatusBadRequest,
		}
	}

	return nil
}

func validateServiceAndMetricNameAndCondition(serviceName string, metricName string, condition string) error {
	switch serviceName {
	case triggersrepo.ServiceSignal:
		signalDef, err := signals.GetSignalDefinition(metricName)
		if err != nil {
			return richerrors.Error{
				ExternalMsg: fmt.Sprintf("Unknown signal name: '%s'", metricName),
				Code:        fiber.StatusBadRequest,
			}
		}
		return validateCondition(serviceName, condition, signalDef.ValueType)
	case triggersrepo.ServiceEvent:
		return validateCondition(serviceName, condition, "")
	default:
		return richerrors.Error{
			ExternalMsg: fmt.Sprintf("Invalid service: %s", serviceName),
			Code:        fiber.StatusBadRequest,
		}
	}
}

func validateCondition(serviceName, condition, valueType string) error {
	_, err := celcondition.PrepareCondition(serviceName, condition, valueType)
	if err != nil {
		err := fmt.Errorf("invalid CEL condition: %w", err)
		return richerrors.Error{
			ExternalMsg: err.Error(),
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}
	return nil
}

func validateCoolDownPeriod(coolDownPeriod int) error {
	if coolDownPeriod < 0 {
		return richerrors.Error{
			ExternalMsg: "Cool down period must be greater than or equal to 0",
			Code:        fiber.StatusBadRequest,
		}
	}
	return nil
}

// validateStatus validates the status of the webhook.
// It must be either "enabled" or "disabled".
func validateStatus(status string) error {
	if status != "enabled" && status != "disabled" {
		return richerrors.Error{
			ExternalMsg: fmt.Sprintf("Invalid status, must be 'enabled' or 'disabled', got '%s'", status),
			Code:        fiber.StatusBadRequest,
		}
	}
	return nil
}
