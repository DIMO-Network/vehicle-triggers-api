package utils

import (
	"testing"
)

func TestNormalizeSignalName(t *testing.T) {
	tests := []struct {
		raw      string
		expected string
	}{
		{"Vehicle.Powertrain.CombustionEngine.IsRunning", "powertrainCombustionEngineIsRunning"},
		{"Vehicle.Powertrain.TractionBattery.CurrentPower", "powertrainTractionBatteryCurrentPower"},
		{"Vehicle.Powertrain.TractionBattery.Charging.IsCharging", "powertrainTractionBatteryChargingIsCharging"},
		{"Vehicle.TraveledDistance", "traveledDistance"},
		{"Vehicle.Powertrain.TractionBattery.StateOfCharge.Current", "powertrainTractionBatteryStateOfChargeCurrent"},
		{"Vehicle.Powertrain.FuelSystem.RelativeLevel", "powertrainFuelSystemRelativeLevel"},
		{"Vehicle.Powertrain.FuelSystem.AbsoluteLevel", "powertrainFuelSystemAbsoluteLevel"},
		{"Vehicle.Chassis.Axle.Row1.Wheel.Left.Tire.Pressure", "chassisAxleRow1WheelLeftTirePressure"},
		{"Vehicle.Chassis.Axle.Row1.Wheel.Right.Tire.Pressure", "chassisAxleRow1WheelRightTirePressure"},
		{"Vehicle.Chassis.Axle.Row2.Wheel.Left.Tire.Pressure", "chassisAxleRow2WheelLeftTirePressure"},
		{"Vehicle.Chassis.Axle.Row2.Wheel.Right.Tire.Pressure", "chassisAxleRow2WheelRightTirePressure"},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got := NormalizeSignalName(tc.raw)
			if got != tc.expected {
				t.Errorf("NormalizeSignalName(%q) = %q; want %q", tc.raw, got, tc.expected)
			}
		})
	}
}
