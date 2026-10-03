package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/fibercommon"
	"github.com/DIMO-Network/token-exchange-api/pkg/signercheck"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

var (
	license = common.HexToAddress("0x299671D2b32ED62Cc61ce65D8f2b9e4f78486B37")
	signer  = common.HexToAddress("0x71efD5d71a597eB6BEC28DFDB05a49283a3e20c5")
)

type fakeChecker struct {
	result signercheck.Result
	calls  int
}

func (f *fakeChecker) Check(context.Context, common.Address, common.Address) (signercheck.Result, error) {
	f.calls++
	return f.result, nil
}

func TestTokenReadsSignerAddressClaim(t *testing.T) {
	var tok Token
	require.NoError(t, json.Unmarshal([]byte(`{
		"ethereum_address": "0x299671d2b32ed62cc61ce65d8f2b9e4f78486b37",
		"signer_address": "0x71efd5d71a597eb6bec28dfdb05a49283a3e20c5"
	}`), &tok))
	require.Equal(t, license, tok.EthereumAddress)
	require.Equal(t, signer, tok.SignerAddress)

	var legacy Token
	require.NoError(t, json.Unmarshal([]byte(`{"ethereum_address": "0x299671d2b32ed62cc61ce65d8f2b9e4f78486b37"}`), &legacy))
	require.Equal(t, common.Address{}, legacy.SignerAddress)
}

func injectToken(claims CustomDexClaims, issuedAt time.Time) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tok := &Token{CustomDexClaims: claims}
		if !issuedAt.IsZero() {
			tok.IssuedAt = jwt.NewNumericDate(issuedAt)
		}
		c.Locals(UserJwtKey, &jwt.Token{Claims: tok})
		return c.Next()
	}
}

func TestSignerTokenInfo(t *testing.T) {
	issued := time.Unix(1_800_000_000, 0)
	app := fiber.New()
	var got signercheck.TokenInfo
	app.Get("/", injectToken(CustomDexClaims{EthereumAddress: license, SignerAddress: signer}, issued), func(c *fiber.Ctx) error {
		var err error
		got, err = SignerTokenInfo(c)
		require.NoError(t, err)
		return c.SendStatus(fiber.StatusOK)
	})
	_, err := app.Test(httptest.NewRequest("GET", "/", nil), -1)
	require.NoError(t, err)
	require.Equal(t, signercheck.TokenInfo{License: license, Signer: signer.Hex(), IssuedAt: issued}, got)
}

func TestSignerCheckOnWebhooksAPI(t *testing.T) {
	serve := func(mode signercheck.Mode, checker *fakeChecker, claims CustomDexClaims) (int, fibercommon.CodedResponse) {
		app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error { return fibercommon.ErrorHandler(c, err) }})
		app.Get("/", injectToken(claims, time.Time{}), signercheck.Middleware(signercheck.Config{
			Service: "vehicle-triggers-api-test",
			Mode:    mode,
			Checker: checker,
			Token:   SignerTokenInfo,
			Logger:  zerolog.Nop(),
		}), func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })
		resp, err := app.Test(httptest.NewRequest("GET", "/", nil), -1)
		require.NoError(t, err)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var body fibercommon.CodedResponse
		if resp.StatusCode != fiber.StatusOK {
			require.NoError(t, json.Unmarshal(raw, &body))
		}
		return resp.StatusCode, body
	}

	status, body := serve(signercheck.ModeEnforce, &fakeChecker{result: signercheck.Denied}, CustomDexClaims{EthereumAddress: license, SignerAddress: signer})
	require.Equal(t, fiber.StatusForbidden, status)
	require.Equal(t, signercheck.MessageDenied, body.Message)

	status, _ = serve(signercheck.ModeLog, &fakeChecker{result: signercheck.Denied}, CustomDexClaims{EthereumAddress: license, SignerAddress: signer})
	require.Equal(t, fiber.StatusOK, status, "log mode never refuses")

	noClaim := &fakeChecker{result: signercheck.Denied}
	status, _ = serve(signercheck.ModeEnforce, noClaim, CustomDexClaims{EthereumAddress: license})
	require.Equal(t, fiber.StatusOK, status)
	require.Zero(t, noClaim.calls, "a token without signer_address isn't checked")
}
