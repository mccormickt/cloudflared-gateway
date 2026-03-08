package cloudflare

import (
	"context"
	"os"

	cf "github.com/cloudflare/cloudflare-go"
)

//go:generate mockgen -destination mock_cloudflare_test.go -package cloudflare_test github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare APIClient

const (
	TunnelSecret   = "AQIDBAUGBwgBAgMEBQYHCAECAwQFBgcIAQIDBAUGBwg="
	ContainerImage = "cloudflare/cloudflared:latest"
)

// client is a wrapper around the Cloudflare API client.
type client struct {
	client *cf.API
	// AccountID is the account ID for the account that the API key is associated with.
	AccountID string
	// APIKey is the API key used to authenticate requests to the Cloudflare API.
	APIKey string
	// APIEmail is the email address associated with the API key.
	Email string
}

type APIClient interface {
	CreateTunnel(ctx context.Context, name string) (cf.Tunnel, error)
	ListTunnels(ctx context.Context) ([]cf.Tunnel, error)
	DeleteTunnel(ctx context.Context, id string) error
	UpdateTunnel(ctx context.Context, params cf.TunnelUpdateParams) (cf.Tunnel, error)
	GetTunnel(ctx context.Context, id string) (cf.Tunnel, error)
}

// NewClient returns a new Cloudflare API client.
func NewClient(accountID, apiKey, email string) APIClient {
	// Construct a new API object using a global API key
	api, err := cf.New(os.Getenv("CLOUDFLARE_API_KEY"), os.Getenv("CLOUDFLARE_API_EMAIL"))
	if err != nil {
		return nil
	}

	return &client{
		client:    api,
		AccountID: accountID,
		APIKey:    apiKey,
		Email:     email,
	}
}

// NewClientFromEnv returns a new Cloudflare API client using environment variables.
func NewClientFromEnv() APIClient {
	return NewClient(
		os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		os.Getenv("CLOUDFLARE_API_KEY"),
		os.Getenv("CLOUDFLARE_API_EMAIL"),
	)
}

// CreateTunnel creates a new Cloudflare Tunnel
func (c *client) CreateTunnel(ctx context.Context, name string) (cf.Tunnel, error) {
	// Tunnel doesn't already exit, create a new one for the Gateway
	return c.client.CreateTunnel(ctx, cf.AccountIdentifier(c.AccountID), cf.TunnelCreateParams{
		Name:      name,
		Secret:    TunnelSecret,
		ConfigSrc: "cloudflare",
	})
}

// ListTunnels returns a list of Tunnels for the account
func (c *client) ListTunnels(ctx context.Context) ([]cf.Tunnel, error) {
	tunnels, _, err := c.client.ListTunnels(ctx, cf.AccountIdentifier(c.AccountID), cf.TunnelListParams{})
	return tunnels, err
}

// DeleteTunnel deletes a Cloudflare Tunnel
func (c *client) DeleteTunnel(ctx context.Context, id string) error {
	return c.client.DeleteTunnel(ctx, cf.AccountIdentifier(c.AccountID), id)
}

// UpdateTunnel updates a Cloudflare Tunnel
func (c *client) UpdateTunnel(ctx context.Context, params cf.TunnelUpdateParams) (cf.Tunnel, error) {
	return c.client.UpdateTunnel(ctx, cf.AccountIdentifier(c.AccountID), params)
}

// GetTunnel returns a Cloudflare Tunnel
func (c *client) GetTunnel(ctx context.Context, id string) (cf.Tunnel, error) {
	return c.client.GetTunnel(ctx, cf.AccountIdentifier(c.AccountID), id)
}
