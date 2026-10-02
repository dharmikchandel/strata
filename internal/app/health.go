package app

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// CheckHealth asks the server listening on listenAddr whether it is serving.
// It backs `strata -healthcheck`, which container runtimes run periodically.
//
// listenAddr is the server's own listen setting, so a wildcard host
// ("0.0.0.0:7070", ":7070") is turned into loopback: the check runs inside the
// same container as the server.
func CheckHealth(ctx context.Context, listenAddr string, timeout time.Duration) error {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("healthcheck: bad listen address %q: %w", listenAddr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	conn, err := grpc.NewClient(net.JoinHostPort(host, port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("healthcheck: server status is %s", resp.Status)
	}
	return nil
}
