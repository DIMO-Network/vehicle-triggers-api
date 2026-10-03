package tokenexchange

import (
	"context"
	"net"
	"testing"

	pb "github.com/DIMO-Network/token-exchange-api/pkg/grpc"
	"github.com/DIMO-Network/token-exchange-api/pkg/signercheck"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type signerCheckServer struct {
	pb.UnimplementedTokenExchangeServiceServer
	isSigner bool
}

func (s *signerCheckServer) SignerCheck(context.Context, *pb.SignerCheckRequest) (*pb.SignerCheckResponse, error) {
	return &pb.SignerCheckResponse{IsSigner: s.isSigner}, nil
}

func TestClientSignerChecker(t *testing.T) {
	for isSigner, want := range map[bool]signercheck.Result{true: signercheck.Allowed, false: signercheck.Denied} {
		lis := bufconn.Listen(1 << 20)
		server := grpc.NewServer()
		pb.RegisterTokenExchangeServiceServer(server, &signerCheckServer{isSigner: isSigner})
		go func() { _ = server.Serve(lis) }()
		t.Cleanup(server.Stop)
		conn, err := grpc.NewClient("passthrough:///bufconn",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		client := &Client{client: pb.NewTokenExchangeServiceClient(conn)}
		got, err := client.SignerChecker().Check(context.Background(),
			common.HexToAddress("0x299671D2b32ED62Cc61ce65D8f2b9e4f78486B37"),
			common.HexToAddress("0x71efD5d71a597eB6BEC28DFDB05a49283a3e20c5"))

		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}
