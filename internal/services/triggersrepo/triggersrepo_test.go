package triggersrepo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"math/big"
	"net/http"
	"slices"
	"testing"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/tests"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Alert when vehicle speed exceeds 20 kph",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("success", func(t *testing.T) {
		trigger, err := repo.CreateTrigger(ctx, baseReq)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		assert.NotEmpty(t, trigger.ID)
		assert.Equal(t, baseReq.Service, trigger.Service)
		assert.Equal(t, baseReq.MetricName, trigger.MetricName)
		assert.Equal(t, baseReq.Condition, trigger.Condition)
		assert.Equal(t, baseReq.TargetURI, trigger.TargetURI)
		assert.Equal(t, baseReq.Status, trigger.Status)
		assert.Equal(t, baseReq.Description, trigger.Description.String)
		assert.Equal(t, baseReq.CooldownPeriod, trigger.CooldownPeriod)
		assert.Equal(t, baseReq.DeveloperLicenseAddress.Bytes(), trigger.DeveloperLicenseAddress)
	})

	t.Run("allow duplicates triggers", func(t *testing.T) {
		// Create first trigger
		trigger1, err := repo.CreateTrigger(ctx, baseReq)
		require.NoError(t, err)
		// This should succeed since UUIDs are auto-generated
		trigger2, err := repo.CreateTrigger(ctx, baseReq)
		require.NoError(t, err)
		assert.NotEqual(t, trigger1.ID, trigger2.ID)
	})

	t.Run("Missing Metric Name", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.MetricName = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Developer License Address", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.DeveloperLicenseAddress = common.Address{}
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Service", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.Service = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Condition", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.Condition = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Target URI", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.TargetURI = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

	t.Run("Missing Status", func(t *testing.T) {
		reqCopy := baseReq
		reqCopy.Status = ""
		_, err := repo.CreateTrigger(ctx, reqCopy)
		require.Error(t, err)
		require.ErrorIs(t, err, ValidationError)
	})

}

func TestGetTriggersByDeveloperLicense(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("success with multiple triggers", func(t *testing.T) {
		devAddress1 := randomAddr(t)
		// Create test triggers for devAddress1
		req1 := baseReq
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"
		req1.DeveloperLicenseAddress = devAddress1

		req2 := baseReq
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"
		req2.CooldownPeriod = 15
		req2.DeveloperLicenseAddress = devAddress1

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Test getting triggers by developer license
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, devAddress1)
		require.NoError(t, err)
		require.Len(t, triggers, 2)

		// Verify all triggers belong to the correct developer license
		for _, trigger := range triggers {
			assert.Equal(t, devAddress1.Bytes(), trigger.DeveloperLicenseAddress)
		}
	})

	t.Run("success with single trigger", func(t *testing.T) {
		// Create a trigger for devAddress2
		req := baseReq
		req.DeveloperLicenseAddress = randomAddr(t)
		req.MetricName = "fuel_level"
		req.Condition = "valueNumber < 10"
		req.TargetURI = "https://example.com/webhook3"
		req.Description = "Low fuel alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Test getting triggers by developer license
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, req.DeveloperLicenseAddress)
		require.NoError(t, err)
		require.Len(t, triggers, 1)

		// Verify the trigger belongs to the correct developer license
		assert.Equal(t, req.DeveloperLicenseAddress.Bytes(), triggers[0].DeveloperLicenseAddress)
		assert.Equal(t, trigger.ID, triggers[0].ID)
	})

	t.Run("empty result for non-existent developer license", func(t *testing.T) {
		nonExistentAddress := randomAddr(t)
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, nonExistentAddress)
		require.NoError(t, err)
		require.Len(t, triggers, 0)
	})

	t.Run("empty result for zero address", func(t *testing.T) {
		zeroAddress := common.Address{}
		triggers, err := repo.GetTriggersByDeveloperLicense(ctx, zeroAddress)
		require.NoError(t, err)
		require.Len(t, triggers, 0)
	})

	t.Run("isolation between different developer licenses", func(t *testing.T) {
		// Create triggers for different developer licenses
		req1 := baseReq
		req1.DeveloperLicenseAddress = randomAddr(t)
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook4"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = randomAddr(t)
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook5"
		req2.Description = "High engine temperature alert"

		_, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)

		_, err = repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)

		// Verify devAddress1 only gets its own triggers
		triggers1, err := repo.GetTriggersByDeveloperLicense(ctx, req1.DeveloperLicenseAddress)
		require.NoError(t, err)
		assert.Len(t, triggers1, 1)
		for _, trigger := range triggers1 {
			assert.Equal(t, req1.DeveloperLicenseAddress.Bytes(), trigger.DeveloperLicenseAddress)
		}

		// Verify devAddress2 only gets its own triggers
		triggers2, err := repo.GetTriggersByDeveloperLicense(ctx, req2.DeveloperLicenseAddress)
		require.NoError(t, err)
		assert.Len(t, triggers2, 1)
		for _, trigger := range triggers2 {
			assert.Equal(t, req2.DeveloperLicenseAddress.Bytes(), trigger.DeveloperLicenseAddress)
		}
	})
}

func TestGetTriggerByIDAndDeveloperLicense(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("existing trigger", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)

		trigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, createdTrigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, trigger)
		assert.Equal(t, createdTrigger.ID, trigger.ID)
		assert.Equal(t, devAddress.Bytes(), trigger.DeveloperLicenseAddress)
	})

	t.Run("non-existent trigger", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)
		nonExistentTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, uuid.New().String(), devAddress)
		require.Error(t, err)
		assert.Nil(t, nonExistentTrigger)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("wrong developer license", func(t *testing.T) {
		devAddress1 := randomAddr(t)
		devAddress2 := randomAddr(t)

		req := baseReq
		req.DeveloperLicenseAddress = devAddress1

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)

		// Try to get the trigger with a different developer license
		wrongTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, createdTrigger.ID, devAddress2)
		require.Error(t, err)
		assert.Nil(t, wrongTrigger)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("empty trigger id", func(t *testing.T) {
		devAddress := randomAddr(t)
		trigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, "", devAddress)
		require.Error(t, err)
		assert.Nil(t, trigger)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("zero address", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		createdTrigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, createdTrigger)

		// Try to get the trigger with zero address
		zeroAddress := common.Address{}
		trigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, createdTrigger.ID, zeroAddress)
		require.Error(t, err)
		assert.Nil(t, trigger)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("isolation between different developer licenses", func(t *testing.T) {
		devAddress1 := randomAddr(t)
		devAddress2 := randomAddr(t)

		// Create triggers for different developer licenses
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "High engine temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Verify devAddress1 can only access its own trigger
		retrievedTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress1)
		require.NoError(t, err)
		require.NotNil(t, retrievedTrigger1)
		assert.Equal(t, trigger1.ID, retrievedTrigger1.ID)
		assert.Equal(t, devAddress1.Bytes(), retrievedTrigger1.DeveloperLicenseAddress)

		// Verify devAddress1 cannot access devAddress2's trigger
		wrongTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress1)
		require.Error(t, err)
		assert.Nil(t, wrongTrigger1)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify devAddress2 can only access its own trigger
		retrievedTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress2)
		require.NoError(t, err)
		require.NotNil(t, retrievedTrigger2)
		assert.Equal(t, trigger2.ID, retrievedTrigger2.ID)
		assert.Equal(t, devAddress2.Bytes(), retrievedTrigger2.DeveloperLicenseAddress)

		// Verify devAddress2 cannot access devAddress1's trigger
		wrongTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress2)
		require.Error(t, err)
		assert.Nil(t, wrongTrigger2)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})
}

func TestUpdateTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("successful update", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Update the trigger
		trigger.Status = "Disabled"
		trigger.Description.String = "Updated speed alert"
		trigger.CooldownPeriod = 20

		err = repo.UpdateTrigger(ctx, trigger)
		require.NoError(t, err)

		// Verify the update
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, updatedTrigger)

		assert.Equal(t, "Disabled", updatedTrigger.Status)
		assert.Equal(t, "Updated speed alert", updatedTrigger.Description.String)
		assert.Equal(t, 20, updatedTrigger.CooldownPeriod)
		// other fields should be the same
		assert.Equal(t, trigger.ID, updatedTrigger.ID)
		assert.Equal(t, trigger.DeveloperLicenseAddress, updatedTrigger.DeveloperLicenseAddress)
		assert.Equal(t, trigger.Service, updatedTrigger.Service)
		assert.Equal(t, trigger.MetricName, updatedTrigger.MetricName)
		assert.Equal(t, trigger.Condition, updatedTrigger.Condition)
		assert.Equal(t, trigger.TargetURI, updatedTrigger.TargetURI)
	})

	t.Run("update non-existent trigger", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create a trigger with a different ID
		nonExistentTrigger := trigger
		nonExistentTrigger.ID = uuid.New().String()

		err = repo.UpdateTrigger(ctx, nonExistentTrigger)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("update multiple fields", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Update multiple fields
		trigger.Status = "Disabled"
		trigger.Description.String = "Updated temperature alert"
		trigger.CooldownPeriod = 25
		trigger.Condition = "valueNumber > 35"
		trigger.TargetURI = "https://example.com/webhook3"

		err = repo.UpdateTrigger(ctx, trigger)
		require.NoError(t, err)

		// Verify all updates
		updatedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, updatedTrigger)

		assert.Equal(t, "Disabled", updatedTrigger.Status)
		assert.Equal(t, "Updated temperature alert", updatedTrigger.Description.String)
		assert.Equal(t, 25, updatedTrigger.CooldownPeriod)
		assert.Equal(t, "valueNumber > 35", updatedTrigger.Condition)
		assert.Equal(t, "https://example.com/webhook3", updatedTrigger.TargetURI)
		// Verify unchanged fields
		assert.Equal(t, trigger.ID, updatedTrigger.ID)
		assert.Equal(t, trigger.DeveloperLicenseAddress, updatedTrigger.DeveloperLicenseAddress)
		assert.Equal(t, trigger.Service, updatedTrigger.Service)
		assert.Equal(t, trigger.MetricName, updatedTrigger.MetricName)
	})
}

func TestDeleteTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("successful delete", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Verify trigger exists
		existingTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, existingTrigger)

		// Delete the trigger
		err = repo.DeleteTrigger(ctx, trigger.ID, devAddress)
		require.NoError(t, err)

		// Verify trigger is deleted
		deletedTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("delete non-existent trigger", func(t *testing.T) {
		devAddress := randomAddr(t)
		// Test deleting non-existent trigger (should not error)
		err := repo.DeleteTrigger(ctx, uuid.New().String(), devAddress)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("delete with wrong developer license", func(t *testing.T) {
		devAddress1 := randomAddr(t)
		devAddress2 := randomAddr(t)

		// Create a trigger for devAddress1
		req := baseReq
		req.DeveloperLicenseAddress = devAddress1
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"
		req.CooldownPeriod = 15

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Try to delete with wrong developer license
		err = repo.DeleteTrigger(ctx, trigger.ID, devAddress2)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify trigger still exists
		existingTrigger, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, devAddress1)
		require.NoError(t, err)
		assert.NotNil(t, existingTrigger)
	})

	t.Run("delete multiple triggers", func(t *testing.T) {
		devAddress := randomAddr(t)

		// Create multiple triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook3"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook4"
		req2.Description = "High engine temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Verify both triggers exist
		existingTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, existingTrigger1)

		existingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress)
		require.NoError(t, err)
		require.NotNil(t, existingTrigger2)

		// Delete first trigger
		err = repo.DeleteTrigger(ctx, trigger1.ID, devAddress)
		require.NoError(t, err)

		// Verify first trigger is deleted
		deletedTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger1)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify second trigger still exists
		remainingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress)
		require.NoError(t, err)
		assert.NotNil(t, remainingTrigger2)

		// Delete second trigger
		err = repo.DeleteTrigger(ctx, trigger2.ID, devAddress)
		require.NoError(t, err)

		// Verify second trigger is deleted
		deletedTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger2)
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("isolation between different developer licenses", func(t *testing.T) {
		devAddress1 := randomAddr(t)
		devAddress2 := randomAddr(t)

		// Create triggers for different developer licenses
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "battery"
		req1.Condition = "valueNumber < 20"
		req1.TargetURI = "https://example.com/webhook5"
		req1.Description = "Low battery alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "engine_temp"
		req2.Condition = "valueNumber > 100"
		req2.TargetURI = "https://example.com/webhook6"
		req2.Description = "High engine temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Delete trigger1
		err = repo.DeleteTrigger(ctx, trigger1.ID, devAddress1)
		require.NoError(t, err)

		// Verify trigger1 is deleted
		deletedTrigger1, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger1.ID, devAddress1)
		require.Error(t, err)
		assert.Nil(t, deletedTrigger1)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify trigger2 still exists
		remainingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress2)
		require.NoError(t, err)
		assert.NotNil(t, remainingTrigger2)

		// Try to delete trigger2 with wrong developer license
		err = repo.DeleteTrigger(ctx, trigger2.ID, devAddress1)
		require.Error(t, err)
		assert.ErrorIs(t, err, sql.ErrNoRows)

		// Verify trigger2 still exists
		stillExistingTrigger2, err := repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger2.ID, devAddress2)
		require.NoError(t, err)
		assert.NotNil(t, stillExistingTrigger2)
	})
}

func TestCreateVehicleSubscription(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("successful creation", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		vehicleTokenID := big.NewInt(12345)

		// Create vehicle subscription
		subscription, err := repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription)

		assert.Equal(t, vehicleTokenID.String(), subscription.VehicleTokenID.String())
		assert.Equal(t, trigger.ID, subscription.TriggerID)
		assert.NotZero(t, subscription.CreatedAt)
		assert.NotZero(t, subscription.UpdatedAt)
	})

	t.Run("create multiple subscriptions for same trigger", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		vehicleTokenID1 := big.NewInt(11111)
		vehicleTokenID2 := big.NewInt(12345)
		vehicleTokenID3 := big.NewInt(67890)

		// Create multiple subscriptions for the same trigger
		subscription1, err := repo.CreateVehicleSubscription(ctx, vehicleTokenID1, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription1)

		subscription2, err := repo.CreateVehicleSubscription(ctx, vehicleTokenID2, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription2)

		subscription3, err := repo.CreateVehicleSubscription(ctx, vehicleTokenID3, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription3)

		// Verify all subscriptions were created
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		slices.SortFunc(subscriptions, func(a, b *models.VehicleSubscription) int {
			return a.VehicleTokenID.Cmp(b.VehicleTokenID.Big)
		})

		assert.Equal(t, vehicleTokenID1.Int64(), subscriptions[0].VehicleTokenID.Int(nil).Int64())
		assert.Equal(t, vehicleTokenID2.Int64(), subscriptions[1].VehicleTokenID.Int(nil).Int64())
		assert.Equal(t, vehicleTokenID3.Int64(), subscriptions[2].VehicleTokenID.Int(nil).Int64())
		assert.Equal(t, trigger.ID, subscriptions[0].TriggerID)
		assert.Equal(t, trigger.ID, subscriptions[1].TriggerID)
		assert.Equal(t, trigger.ID, subscriptions[2].TriggerID)
		assert.NotZero(t, subscriptions[0].CreatedAt)
		assert.NotZero(t, subscriptions[1].CreatedAt)
		assert.NotZero(t, subscriptions[2].CreatedAt)
		assert.NotZero(t, subscriptions[0].UpdatedAt)
		assert.NotZero(t, subscriptions[1].UpdatedAt)
		assert.NotZero(t, subscriptions[2].UpdatedAt)
	})

	t.Run("create subscription for non-existent trigger", func(t *testing.T) {
		vehicleTokenID := big.NewInt(12345)
		nonExistentTriggerID := uuid.New().String()

		// Try to create subscription for non-existent trigger
		_, err := repo.CreateVehicleSubscription(ctx, vehicleTokenID, nonExistentTriggerID)
		require.Error(t, err)
		var richErr richerrors.Error
		require.ErrorAs(t, err, &richErr)
		require.Equal(t, http.StatusNotFound, richErr.Code)
	})

	t.Run("create subscription with zero vehicle token ID", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		zeroVehicleTokenID := big.NewInt(0)

		// Try to create subscription with zero vehicle token ID
		_, err = repo.CreateVehicleSubscription(ctx, zeroVehicleTokenID, trigger.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("create subscription with empty trigger ID", func(t *testing.T) {
		vehicleTokenID := big.NewInt(12345)
		emptyTriggerID := ""

		// Try to create subscription with empty trigger ID
		_, err := repo.CreateVehicleSubscription(ctx, vehicleTokenID, emptyTriggerID)
		require.Error(t, err)
		assert.ErrorIs(t, err, ValidationError)
	})

	t.Run("create duplicate subscription", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "battery"
		req.Condition = "valueNumber < 20"
		req.TargetURI = "https://example.com/webhook3"
		req.Description = "Low battery alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		vehicleTokenID := big.NewInt(12345)

		// Create first subscription
		subscription1, err := repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger.ID)
		require.NoError(t, err)
		require.NotNil(t, subscription1)

		// Try to create duplicate subscription
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger.ID)
		require.Error(t, err)
		assert.True(t, IsDuplicateKeyError(err))

		// Verify only one subscription exists
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)
		assert.Equal(t, vehicleTokenID.String(), subscriptions[0].VehicleTokenID.String())
		assert.Equal(t, trigger.ID, subscriptions[0].TriggerID)
	})
}

func TestGetVehicleSubscriptionsByTriggerID(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("successful retrieval", func(t *testing.T) {
		devAddress := randomAddr(t)

		// Create triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"
		req2.CooldownPeriod = 15

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Create vehicle subscriptions
		vehicleTokenID1 := big.NewInt(12345)
		vehicleTokenID2 := big.NewInt(67890)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID1, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID2, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID1, trigger2.ID)
		require.NoError(t, err)

		// Test getting subscriptions by trigger IDs
		subscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 2)

		subscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)

		// Verify subscriptions
		for _, sub := range subscriptions1 {
			assert.Equal(t, trigger1.ID, sub.TriggerID)
		}

		for _, sub := range subscriptions2 {
			assert.Equal(t, trigger2.ID, sub.TriggerID)
		}
	})

	t.Run("empty result for non-existent trigger ID", func(t *testing.T) {
		nonExistentTriggerID := uuid.New().String()

		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, nonExistentTriggerID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

}

func TestGetVehicleSubscriptionsByVehicleAndDeveloperLicense(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("successful retrieval for different developers", func(t *testing.T) {
		devAddress1 := randomAddr(t)

		devAddress2 := randomAddr(t)

		// Create triggers for different developers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"
		req2.CooldownPeriod = 15

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		vehicleTokenID := big.NewInt(12345)

		// Create vehicle subscriptions
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger2.ID)
		require.NoError(t, err)

		// Test getting subscriptions for devAddress1
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, vehicleTokenID, devAddress1)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)
		assert.Equal(t, trigger1.ID, subscriptions[0].TriggerID)
		assert.Equal(t, vehicleTokenID.String(), subscriptions[0].VehicleTokenID.String())

		// Test getting subscriptions for devAddress2
		subscriptions, err = repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, vehicleTokenID, devAddress2)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)
		assert.Equal(t, trigger2.ID, subscriptions[0].TriggerID)
		assert.Equal(t, vehicleTokenID.String(), subscriptions[0].VehicleTokenID.String())
	})

	t.Run("empty result for non-existent vehicle", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		vehicleTokenID := big.NewInt(12345)
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger.ID)
		require.NoError(t, err)

		// Test getting subscriptions for non-existent vehicle
		nonExistentVehicle := big.NewInt(99999)
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, nonExistentVehicle, devAddress)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("empty result for non-existent developer license", func(t *testing.T) {
		devAddress1 := randomAddr(t)
		devAddress2 := randomAddr(t)

		req := baseReq
		req.DeveloperLicenseAddress = devAddress1

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)
		vehicleTokenID := randTokenID(t)
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger.ID)
		require.NoError(t, err)

		// Test getting subscriptions for non-existent developer license
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, vehicleTokenID, devAddress2)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("multiple subscriptions for same vehicle and developer", func(t *testing.T) {
		devAddress := randomAddr(t)

		// Create multiple triggers for the same developer
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		req3 := baseReq
		req3.DeveloperLicenseAddress = devAddress
		req3.MetricName = "battery"
		req3.Condition = "valueNumber < 20"
		req3.TargetURI = "https://example.com/webhook3"
		req3.Description = "Low battery alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		trigger3, err := repo.CreateTrigger(ctx, req3)
		require.NoError(t, err)
		require.NotNil(t, trigger3)

		vehicleTokenID := randTokenID(t)

		// Create subscriptions for all triggers
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger2.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger3.ID)
		require.NoError(t, err)

		// Test getting all subscriptions for the vehicle and developer
		subscriptions, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, vehicleTokenID, devAddress)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		// Sort subscriptions for consistent ordering
		slices.SortFunc(subscriptions, func(a, b *models.VehicleSubscription) int {
			return a.CreatedAt.Compare(b.CreatedAt)
		})

		// Verify all subscriptions belong to the correct vehicle and developer
		require.Len(t, subscriptions, 3)
		assert.Equal(t, vehicleTokenID.String(), subscriptions[0].VehicleTokenID.String())
		assert.Equal(t, vehicleTokenID.String(), subscriptions[1].VehicleTokenID.String())
		assert.Equal(t, vehicleTokenID.String(), subscriptions[2].VehicleTokenID.String())
		assert.Equal(t, trigger1.ID, subscriptions[0].TriggerID)
		assert.Equal(t, trigger2.ID, subscriptions[1].TriggerID)
		assert.Equal(t, trigger3.ID, subscriptions[2].TriggerID)
	})

	t.Run("isolation between different developers", func(t *testing.T) {
		devAddress1 := randomAddr(t)
		devAddress2 := randomAddr(t)

		// Create triggers for different developers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress1
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress2
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		vehicleTokenID := randTokenID(t)

		// Create subscriptions for both triggers
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger2.ID)
		require.NoError(t, err)

		// Test getting subscriptions for devAddress1
		subscriptions1, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, vehicleTokenID, devAddress1)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 1)
		assert.Equal(t, trigger1.ID, subscriptions1[0].TriggerID)
		assert.Equal(t, vehicleTokenID.String(), subscriptions1[0].VehicleTokenID.String())

		// Test getting subscriptions for devAddress2
		subscriptions2, err := repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx, vehicleTokenID, devAddress2)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)
		assert.Equal(t, trigger2.ID, subscriptions2[0].TriggerID)
		assert.Equal(t, vehicleTokenID.String(), subscriptions2[0].VehicleTokenID.String())

		// Verify no cross-contamination
		assert.NotEqual(t, subscriptions1[0].TriggerID, subscriptions2[0].TriggerID)
	})
}

func TestDeleteVehicleSubscription(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("successful deletion", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		vehicleTokenID := randTokenID(t)

		// Create vehicle subscription
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger.ID)
		require.NoError(t, err)

		// Verify subscription exists
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 1)

		// Delete the subscription
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger.ID, vehicleTokenID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)

		// Verify subscription is deleted
		subscriptions, err = repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("delete non-existent subscription", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		nonExistentVehicleTokenID := randTokenID(t)

		// Test deleting non-existent subscription
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger.ID, nonExistentVehicleTokenID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)
	})

	t.Run("delete from non-existent trigger", func(t *testing.T) {
		vehicleTokenID := randTokenID(t)
		nonExistentTriggerID := uuid.New().String()

		// Test deleting from non-existent trigger
		deleted, err := repo.DeleteVehicleSubscription(ctx, nonExistentTriggerID, vehicleTokenID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)
	})

	t.Run("delete specific subscription from multiple", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create multiple vehicle subscriptions
		vehicleTokenID1 := randTokenID(t)
		vehicleTokenID2 := randTokenID(t)
		vehicleTokenID3 := randTokenID(t)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID1, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID2, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID3, trigger.ID)
		require.NoError(t, err)

		// Verify all subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		// Delete only one subscription
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger.ID, vehicleTokenID2)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)

		// Verify only the specific subscription was deleted
		remainingSubscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions, 2)

		// Verify the remaining subscriptions are correct
		remainingTokenIDs := make(map[string]bool)
		for _, sub := range remainingSubscriptions {
			remainingTokenIDs[sub.VehicleTokenID.String()] = true
		}

		assert.True(t, remainingTokenIDs[vehicleTokenID1.String()])
		assert.True(t, remainingTokenIDs[vehicleTokenID3.String()])
		assert.False(t, remainingTokenIDs[vehicleTokenID2.String()])
	})

	t.Run("isolation between different triggers", func(t *testing.T) {
		devAddress := randomAddr(t)

		// Create two triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		vehicleTokenID := randTokenID(t)

		// Create subscriptions for both triggers
		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID, trigger2.ID)
		require.NoError(t, err)

		// Verify both subscriptions exist
		subscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 1)

		subscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)

		// Delete subscription from trigger1 only
		deleted, err := repo.DeleteVehicleSubscription(ctx, trigger1.ID, vehicleTokenID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)

		// Verify trigger1 subscription is deleted
		remainingSubscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions1, 0)

		// Verify trigger2 subscription still exists
		remainingSubscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions2, 1)
		assert.Equal(t, vehicleTokenID.String(), remainingSubscriptions2[0].VehicleTokenID.String())
	})
}

func TestDeleteAllVehicleSubscriptionsForTrigger(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	baseReq := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: randomAddr(t),
	}

	t.Run("successful deletion of all subscriptions", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create multiple vehicle subscriptions
		vehicleTokenID1 := randTokenID(t)
		vehicleTokenID2 := randTokenID(t)
		vehicleTokenID3 := randTokenID(t)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID1, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID2, trigger.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID3, trigger.ID)
		require.NoError(t, err)

		// Verify subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 3)

		// Delete all subscriptions for the trigger
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(3), deleted)

		// Verify all subscriptions are deleted
		subscriptions, err = repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("delete from trigger with no subscriptions", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "temperature"
		req.Condition = "valueNumber > 30"
		req.TargetURI = "https://example.com/webhook2"
		req.Description = "Temperature alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Delete all subscriptions for trigger with no subscriptions
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)

		// Verify no subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})

	t.Run("delete from non-existent trigger", func(t *testing.T) {
		nonExistentTriggerID := uuid.New().String()

		// Delete all subscriptions for non-existent trigger
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, nonExistentTriggerID)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted)
	})

	t.Run("isolation between different triggers", func(t *testing.T) {
		devAddress := randomAddr(t)

		// Create two triggers
		req1 := baseReq
		req1.DeveloperLicenseAddress = devAddress
		req1.MetricName = "speed"
		req1.Condition = "valueNumber > 20"
		req1.TargetURI = "https://example.com/webhook1"
		req1.Description = "Speed alert"

		req2 := baseReq
		req2.DeveloperLicenseAddress = devAddress
		req2.MetricName = "temperature"
		req2.Condition = "valueNumber > 30"
		req2.TargetURI = "https://example.com/webhook2"
		req2.Description = "Temperature alert"

		trigger1, err := repo.CreateTrigger(ctx, req1)
		require.NoError(t, err)
		require.NotNil(t, trigger1)

		trigger2, err := repo.CreateTrigger(ctx, req2)
		require.NoError(t, err)
		require.NotNil(t, trigger2)

		// Create subscriptions for both triggers
		vehicleTokenID1 := randTokenID(t)
		vehicleTokenID2 := randTokenID(t)
		vehicleTokenID3 := randTokenID(t)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID1, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID2, trigger1.ID)
		require.NoError(t, err)

		_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenID3, trigger2.ID)
		require.NoError(t, err)

		// Verify subscriptions exist for both triggers
		subscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions1, 2)

		subscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions2, 1)

		// Delete all subscriptions for trigger1 only
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger1.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(2), deleted)

		// Verify trigger1 subscriptions are deleted
		remainingSubscriptions1, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger1.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions1, 0)

		// Verify trigger2 subscription still exists
		remainingSubscriptions2, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger2.ID)
		require.NoError(t, err)
		require.Len(t, remainingSubscriptions2, 1)
		assert.Equal(t, vehicleTokenID3.String(), remainingSubscriptions2[0].VehicleTokenID.String())
	})

	t.Run("delete large number of subscriptions", func(t *testing.T) {
		devAddress := randomAddr(t)
		req := baseReq
		req.DeveloperLicenseAddress = devAddress
		req.MetricName = "battery"
		req.Condition = "valueNumber < 20"
		req.TargetURI = "https://example.com/webhook3"
		req.Description = "Low battery alert"

		trigger, err := repo.CreateTrigger(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, trigger)

		// Create many vehicle subscriptions
		subscriptionCount := 10
		vehicleTokenIDs := make([]*big.Int, subscriptionCount)
		for i := 0; i < subscriptionCount; i++ {
			vehicleTokenIDs[i] = randTokenID(t)
			_, err = repo.CreateVehicleSubscription(ctx, vehicleTokenIDs[i], trigger.ID)
			require.NoError(t, err)
		}

		// Verify all subscriptions exist
		subscriptions, err := repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, subscriptionCount)

		// Delete all subscriptions for the trigger
		deleted, err := repo.DeleteAllVehicleSubscriptionsForTrigger(ctx, trigger.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(subscriptionCount), deleted)

		// Verify all subscriptions are deleted
		subscriptions, err = repo.GetVehicleSubscriptionsByTriggerID(ctx, trigger.ID)
		require.NoError(t, err)
		require.Len(t, subscriptions, 0)
	})
}

func TestGetWebhookOwner(t *testing.T) {
	t.Parallel()
	tc := tests.SetupTestContainer(t)

	repo := NewRepository(tc.DB)
	ctx := context.Background()

	devAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create a trigger
	req := CreateTriggerRequest{
		Service:                 "Telemetry",
		MetricName:              "speed",
		Condition:               "valueNumber > 20",
		TargetURI:               "https://example.com/webhook",
		Status:                  "Enabled",
		Description:             "Speed alert",
		CooldownPeriod:          10,
		DeveloperLicenseAddress: devAddress,
	}

	trigger, err := repo.CreateTrigger(ctx, req)
	require.NoError(t, err)

	// Get webhook owner
	owner, err := repo.GetWebhookOwner(ctx, trigger.ID)
	require.NoError(t, err)
	assert.Equal(t, devAddress, owner)

	// Test with non-existent trigger
	_, err = repo.GetWebhookOwner(ctx, uuid.New().String())
	require.Error(t, err)
}

func TestBigIntToDecimal(t *testing.T) {
	t.Parallel()
	// Test the helper function
	testCases := []struct {
		name     string
		input    *big.Int
		expected string
	}{
		{
			name:     "Zero",
			input:    big.NewInt(0),
			expected: "0",
		},
		{
			name:     "Positive number",
			input:    big.NewInt(12345),
			expected: "12345",
		},
		{
			name:     "Large number",
			input:    big.NewInt(999999999),
			expected: "999999999",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := bigIntToDecimal(tc.input)
			assert.Equal(t, tc.expected, result.String())
		})
	}
}
func randomAddr(t *testing.T) common.Address {
	addr := make([]byte, common.AddressLength)
	_, err := rand.Read(addr)
	if err != nil {
		t.Fatalf("couldn't create a test address: %v", err)
	}
	return common.Address(addr)
}

func randTokenID(t *testing.T) *big.Int {
	tokenID := make([]byte, 32)
	_, err := rand.Read(tokenID)
	if err != nil {
		t.Fatalf("couldn't create a test token ID: %v", err)
	}
	return new(big.Int).SetBytes(tokenID)
}
