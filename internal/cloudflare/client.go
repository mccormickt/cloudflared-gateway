package cloudflare

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	cf "github.com/cloudflare/cloudflare-go"
)

//go:generate mockgen -destination mock_client.go -package cloudflare github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare APIClient

// ErrTunnelNotFound is returned when no matching tunnel exists.
var ErrTunnelNotFound = errors.New("tunnel not found")

// APIClient defines the Cloudflare tunnel operations needed by the controller.
type APIClient interface {
	CreateTunnel(ctx context.Context, name string, secret []byte) (cf.Tunnel, error)
	GetTunnelByName(ctx context.Context, name string) (cf.Tunnel, error)
	DeleteTunnel(ctx context.Context, id string) error
	UpdateTunnelConfiguration(ctx context.Context, tunnelID string, ingress []cf.UnvalidatedIngressRule) error
	AccountID() string
}

type client struct {
	api       *cf.API
	accountID string
}

// NewClientFromEnv creates a new Cloudflare API client from environment variables.
// Requires CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN.
func NewClientFromEnv() (APIClient, error) {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	if accountID == "" {
		return nil, fmt.Errorf("CLOUDFLARE_ACCOUNT_ID is required")
	}

	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("CLOUDFLARE_API_TOKEN is required")
	}

	api, err := cf.NewWithAPIToken(token)
	if err != nil {
		return nil, fmt.Errorf("creating Cloudflare API client: %w", err)
	}

	return &client{api: api, accountID: accountID}, nil
}

func (c *client) AccountID() string {
	return c.accountID
}

func (c *client) rc() *cf.ResourceContainer {
	return cf.AccountIdentifier(c.accountID)
}

// CreateTunnel creates a new Cloudflare tunnel with the given name and secret.
func (c *client) CreateTunnel(ctx context.Context, name string, secret []byte) (cf.Tunnel, error) {
	tunnel, err := c.api.CreateTunnel(ctx, c.rc(), cf.TunnelCreateParams{
		Name:      name,
		Secret:    base64.StdEncoding.EncodeToString(secret),
		ConfigSrc: "cloudflare",
	})
	if err != nil {
		return cf.Tunnel{}, fmt.Errorf("creating tunnel: %w", err)
	}
	return tunnel, nil
}

// GetTunnelByName finds a tunnel by name, filtering out deleted tunnels.
// Returns ErrTunnelNotFound if no matching tunnel is found.
func (c *client) GetTunnelByName(ctx context.Context, name string) (cf.Tunnel, error) {
	isDeleted := false
	tunnels, _, err := c.api.ListTunnels(ctx, c.rc(), cf.TunnelListParams{
		Name:      name,
		IsDeleted: &isDeleted,
	})
	if err != nil {
		return cf.Tunnel{}, fmt.Errorf("listing tunnels: %w", err)
	}

	for i := range tunnels {
		if tunnels[i].Name == name {
			return tunnels[i], nil
		}
	}
	return cf.Tunnel{}, ErrTunnelNotFound
}

// DeleteTunnel deletes a Cloudflare tunnel by ID.
func (c *client) DeleteTunnel(ctx context.Context, id string) error {
	if err := c.api.DeleteTunnel(ctx, c.rc(), id); err != nil {
		return fmt.Errorf("deleting tunnel %s: %w", id, err)
	}
	return nil
}

// UpdateTunnelConfiguration pushes ingress rules to a tunnel's configuration.
func (c *client) UpdateTunnelConfiguration(ctx context.Context, tunnelID string, ingress []cf.UnvalidatedIngressRule) error {
	_, err := c.api.UpdateTunnelConfiguration(ctx, c.rc(), cf.TunnelConfigurationParams{
		TunnelID: tunnelID,
		Config: cf.TunnelConfiguration{
			Ingress: ingress,
		},
	})
	if err != nil {
		return fmt.Errorf("updating tunnel %s configuration: %w", tunnelID, err)
	}
	return nil
}
