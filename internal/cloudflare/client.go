package cloudflare

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
)

//go:generate mockgen -destination mock_client.go -package cloudflare github.com/mccormickt/cloudflared-gateway/internal/cloudflare APIClient

// ErrTunnelNotFound is returned when no matching tunnel exists.
var ErrTunnelNotFound = errors.New("tunnel not found")

// APIClient defines the Cloudflare tunnel operations needed by the controller.
// It traffics in this package's domain types, not SDK types.
type APIClient interface {
	CreateTunnel(ctx context.Context, name string, secret []byte) (Tunnel, error)
	GetTunnelByName(ctx context.Context, name string) (Tunnel, error)
	DeleteTunnel(ctx context.Context, id string) error
	UpdateTunnelConfiguration(ctx context.Context, tunnelID string, ingress []IngressRule) error
	AccountID() string
}

type client struct {
	api       *cf.Client
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

	api := cf.NewClient(option.WithAPIToken(token))
	return &client{api: api, accountID: accountID}, nil
}

func (c *client) AccountID() string {
	return c.accountID
}

// CreateTunnel creates a new remotely-managed Cloudflare tunnel.
func (c *client) CreateTunnel(ctx context.Context, name string, secret []byte) (Tunnel, error) {
	tunnel, err := c.api.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID:    cf.F(c.accountID),
		Name:         cf.F(name),
		ConfigSrc:    cf.F(zero_trust.TunnelCloudflaredNewParamsConfigSrcCloudflare),
		TunnelSecret: cf.F(base64.StdEncoding.EncodeToString(secret)),
	})
	if err != nil {
		return Tunnel{}, fmt.Errorf("creating tunnel: %w", err)
	}
	return Tunnel{ID: tunnel.ID, Name: tunnel.Name}, nil
}

// GetTunnelByName finds a non-deleted tunnel by name.
// Returns ErrTunnelNotFound if no matching tunnel is found.
func (c *client) GetTunnelByName(ctx context.Context, name string) (Tunnel, error) {
	iter := c.api.ZeroTrust.Tunnels.Cloudflared.ListAutoPaging(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cf.F(c.accountID),
		Name:      cf.F(name),
		IsDeleted: cf.F(false),
	})
	for iter.Next() {
		tunnel := iter.Current()
		if tunnel.Name == name {
			return Tunnel{ID: tunnel.ID, Name: tunnel.Name}, nil
		}
	}
	if err := iter.Err(); err != nil {
		return Tunnel{}, fmt.Errorf("listing tunnels: %w", err)
	}
	return Tunnel{}, ErrTunnelNotFound
}

// DeleteTunnel deletes a Cloudflare tunnel by ID.
func (c *client) DeleteTunnel(ctx context.Context, id string) error {
	if _, err := c.api.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, id, zero_trust.TunnelCloudflaredDeleteParams{
		AccountID: cf.F(c.accountID),
	}); err != nil {
		return fmt.Errorf("deleting tunnel %s: %w", id, err)
	}
	return nil
}

// UpdateTunnelConfiguration pushes ingress rules to a tunnel's configuration.
func (c *client) UpdateTunnelConfiguration(ctx context.Context, tunnelID string, ingress []IngressRule) error {
	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, tunnelID, zero_trust.TunnelCloudflaredConfigurationUpdateParams{
		AccountID: cf.F(c.accountID),
		Config: cf.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{
			Ingress: cf.F(toV7Ingress(ingress)),
		}),
	})
	if err != nil {
		return fmt.Errorf("updating tunnel %s configuration: %w", tunnelID, err)
	}
	return nil
}

// toV7Ingress translates the controller's domain ingress rules into the v7 SDK
// configuration-update params. This is the single seam coupling the package to
// a specific SDK version.
func toV7Ingress(rules []IngressRule) []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	out := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(rules))
	for _, r := range rules {
		ing := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
			Service: cf.F(r.Service),
		}
		if r.Hostname != "" {
			ing.Hostname = cf.F(r.Hostname)
		}
		if r.Path != "" {
			ing.Path = cf.F(r.Path)
		}
		if r.OriginRequest != nil {
			ing.OriginRequest = cf.F(toV7OriginRequest(r.OriginRequest))
		}
		out = append(out, ing)
	}
	return out
}

func toV7OriginRequest(o *OriginRequest) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest {
	var r zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest
	if o.ConnectTimeout != nil {
		r.ConnectTimeout = cf.F(int64(o.ConnectTimeout.Seconds()))
	}
	if o.NoHappyEyeballs != nil {
		r.NoHappyEyeballs = cf.F(*o.NoHappyEyeballs)
	}
	if o.KeepAliveConnections != nil {
		r.KeepAliveConnections = cf.F(int64(*o.KeepAliveConnections))
	}
	if o.KeepAliveTimeout != nil {
		r.KeepAliveTimeout = cf.F(int64(o.KeepAliveTimeout.Seconds()))
	}
	if o.HTTPHostHeader != nil {
		r.HTTPHostHeader = cf.F(*o.HTTPHostHeader)
	}
	if o.OriginServerName != nil {
		r.OriginServerName = cf.F(*o.OriginServerName)
	}
	if o.NoTLSVerify != nil {
		r.NoTLSVerify = cf.F(*o.NoTLSVerify)
	}
	if o.DisableChunkedEncoding != nil {
		r.DisableChunkedEncoding = cf.F(*o.DisableChunkedEncoding)
	}
	if o.ProxyType != nil {
		r.ProxyType = cf.F(*o.ProxyType)
	}
	if o.HTTP2Origin != nil {
		r.HTTP2Origin = cf.F(*o.HTTP2Origin)
	}
	if o.Access != nil {
		r.Access = cf.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequestAccess{
			AUDTag:   cf.F(o.Access.AudTag),
			TeamName: cf.F(o.Access.TeamName),
			Required: cf.F(o.Access.Required),
		})
	}
	return r
}
