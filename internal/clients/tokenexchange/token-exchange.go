package tokenexchange

import (
	"context"
	"fmt"

	"github.com/DIMO-Network/cloudevent"
	pb "github.com/DIMO-Network/token-exchange-api/pkg/grpc"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client for the Token Exchange API GRPC server
type Client struct {
	client pb.TokenExchangeServiceClient
}

// New creates a new instance of Client with the specified server address,
// vehicle contract address, aftermarket contract address, and chain ID
func New(settings *config.Settings) (*Client, error) {
	conn, err := grpc.NewClient(settings.TokenExchangeGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Token Exchange API gRPC server: %w", err)
	}
	client := pb.NewTokenExchangeServiceClient(conn)
	return &Client{
		client: client,
	}, nil
}

// HasVehiclePermissions checks if the given developer license has privileges 1,3,4 for the vehicle.
func (c *Client) HasVehiclePermissions(ctx context.Context, assetDid cloudevent.ERC721DID, devLicense common.Address, permissions []string) (bool, error) {
	req := pb.AccessCheckRequest{
		Asset:      assetDid.String(),
		Grantee:    devLicense.String(),
		Privileges: permissions,
	}

	resp, err := c.client.AccessCheck(ctx, &req)
	if err != nil {
		return false, fmt.Errorf("failed to check access: %w", err)
	}
	return resp.HasAccess, nil
}
