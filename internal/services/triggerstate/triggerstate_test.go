package triggerstate

import (
	"math/big"
	"testing"

	"github.com/DIMO-Network/cloudevent"
	"github.com/ethereum/go-ethereum/common"
)

func TestKey(t *testing.T) {
	did := cloudevent.ERC721DID{
		ChainID:         137,
		ContractAddress: common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF"),
		TokenID:         big.NewInt(42),
	}
	got := Key("abc-123-uuid", did)
	// triggerID kept as-is, DID's colons replaced.
	want := "abc-123-uuid.did_erc721_137_0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF_42"
	if got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"plain":          "plain",
		"with:colons":    "with_colons",
		"spaces here":    "spaces_here",
		"wild*card":      "wild_card",
		"angle>brackets": "angle_brackets",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
