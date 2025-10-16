package tokenexchange

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DIMO-Network/cloudevent"
	pb "github.com/DIMO-Network/token-exchange-api/pkg/grpc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/patrickmn/go-cache"
)

// Cache provides token exchange cache functionality.
type Cache struct {
	cache               *cache.Cache
	tokenExchangeClient *Client
}

// New creates a new token cache instance.
func NewCache(defaultExpiration, cleanupInterval time.Duration, tokenExchangeClient *Client) *Cache {
	return &Cache{
		cache:               cache.New(defaultExpiration, cleanupInterval),
		tokenExchangeClient: tokenExchangeClient,
	}
}

// HasVehiclePermissions checks if the given developer license has privileges 1,3,4 for the vehicle.
func (c *Cache) HasVehiclePermissions(ctx context.Context, assetDid cloudevent.ERC721DID, devLicense common.Address, permissions []string) (bool, error) {
	req := pb.AccessCheckRequest{
		Asset:      assetDid.String(),
		Grantee:    devLicense.String(),
		Privileges: permissions,
	}
	cacheKey := accessRequestCacheKey(&req)

	if hasAccess, found := c.cache.Get(cacheKey); found {
		return hasAccess.(bool), nil
	}

	hasAccess, err := c.tokenExchangeClient.HasVehiclePermissions(ctx, assetDid, devLicense, permissions)
	if err != nil {
		return false, fmt.Errorf("failed to check access: %w", err)
	}
	c.cache.Set(cacheKey, hasAccess, 0)
	return hasAccess, nil
}

// accessRequestCacheKey creates a key for the access request.
// Warning: this only works for request without events.
func accessRequestCacheKey(request *pb.AccessCheckRequest) string {
	slices.Sort(request.Privileges)
	return request.GetAsset() + ":" + request.GetGrantee() + ":" + strings.Join(request.GetPrivileges(), ",")
}
