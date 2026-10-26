package tc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestRedisPing(t *testing.T) {
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	ep, err := c.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	conn, err := net.DialTimeout("tcp", ep, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", ep, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "+PONG\r\n" {
		t.Fatalf("PING reply %q err %v", buf[:n], err)
	}
}
