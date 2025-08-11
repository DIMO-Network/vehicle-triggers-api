package identity

import (
	"github.com/DIMO-Network/cloudevent"
	"github.com/ethereum/go-ethereum/common"
)

// IdentityResponse is a generic response from the Identity API.
type IdentityResponse[T any] struct {
	Data   T               `json:"data"`
	Errors []IdentityError `json:"errors"`
}

// IdentityError is an error from the Identity API.
type IdentityError struct {
	Message string   `json:"message"`
	Path    []string `json:"path"`
}

type SharedVehiclesResponse struct {
	Vehicles VehicleResponse `json:"vehicles"`
}

type VehicleResponse struct {
	Nodes []DIDNode `json:"nodes"`
}
type DIDNode struct {
	TokenDID cloudevent.ERC721DID `json:"tokenDID"`
}

type DeveloperLicenseResponse struct {
	DeveloperLicense DeveloperLicense `json:"developerLicense"`
}

type DeveloperLicense struct {
	ClientID common.Address `json:"clientId"`
	TokenID  int            `json:"tokenId"`
}
