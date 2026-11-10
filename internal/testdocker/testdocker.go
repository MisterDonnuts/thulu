package testdocker

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

func Client(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		//i18n:ignore test helper output
		t.Skipf("docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		//i18n:ignore test helper output
		t.Skipf("docker daemon not reachable: %v", err)
	}
	return cli
}

func RequireImage(t *testing.T, cli *client.Client, image, hint string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := cli.ImageInspectWithRaw(ctx, image); err != nil {
		//i18n:ignore test helper output
		t.Skipf("%s not present (%s): %v", image, hint, err)
	}
}
