package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func TestRequireBearerProtocolErrors(t *testing.T) {
	srv := httptest.NewServer(RequireBearer("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unauthenticated request reached the handler")
	})))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		opts []connect.ClientOption
	}{
		{name: "connect"},
		{name: "grpc", opts: []connect.ClientOption{connect.WithGRPC()}},
		{name: "grpc-web", opts: []connect.ClientOption{connect.WithGRPCWeb()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := yccv1connect.NewSessionServiceClient(srv.Client(), srv.URL, tc.opts...)
			_, err := client.ListModes(context.Background(), connect.NewRequest(&v1.ListModesRequest{}))
			if connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("unary error = %v, want unauthenticated", err)
			}

			stream, err := client.Subscribe(context.Background(), connect.NewRequest(&v1.SubscribeRequest{}))
			if err == nil {
				defer stream.Close()
				if stream.Receive() {
					t.Fatal("unauthenticated stream returned a message")
				}
				err = stream.Err()
			}
			if connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("stream error = %v, want unauthenticated", err)
			}
		})
	}
}
