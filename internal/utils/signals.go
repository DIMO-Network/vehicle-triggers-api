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
		if i == 0 {
			// lower-case the entire first segment (OBD → obd)
			parts[i] = strings.ToLower(p)
		} else {
			// upper-case only the first letter of later segments
			runes := []rune(p)
			runes[0] = unicode.ToUpper(runes[0])
			parts[i] = string(runes)
		}
	}
	return strings.Join(parts, "")
}
