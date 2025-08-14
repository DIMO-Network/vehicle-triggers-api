package tests

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func RandomAddr(t *testing.T) common.Address {
	walletPrivateKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	return crypto.PubkeyToAddress(walletPrivateKey.PublicKey)
}
