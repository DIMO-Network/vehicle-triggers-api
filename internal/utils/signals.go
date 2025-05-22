package utils

import (
	"strings"
	"unicode"
)

// NormalizeSignalName turns a raw VSS name like "Vehicle.Powertrain.TractionBattery.Charging.IsCharging"
// into the exact Kafka key: "powertrainTractionBatteryChargingIsCharging"
func NormalizeSignalName(vss string) string {
	parts := strings.Split(vss, ".")
	// drop the leading "Vehicle" if present
	if len(parts) > 0 && parts[0] == "Vehicle" {
		parts = parts[1:]
	}
	for i, p := range parts {
		if p == "" {
			continue
		}
		// lower-case first letter of the first segment
		// upper-case first letter of every later segment
		runes := []rune(p)
		first := runes[0]
		if i == 0 {
			runes[0] = unicode.ToLower(first)
		} else {
			runes[0] = unicode.ToUpper(first)
		}
		parts[i] = string(runes)
	}
	return strings.Join(parts, "")
}
