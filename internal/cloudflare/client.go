package cloudflare

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/dns"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

// dnsBatchChunkSize bounds how many record mutations are sent in a single
// dns_records/batch request. Each batch is transactional; chunking splits large
// change sets into several atomic batches, mirroring external-dns's Cloudflare
// provider behavior.
const dnsBatchChunkSize = 100

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

	// DNS record management (used only when DNS management is enabled).
	// ListZones returns the account's zones for longest-suffix hostname matching.
	ListZones(ctx context.Context) ([]Zone, error)
	// ListDNSRecords returns the CNAME records in a zone, so the caller can diff
	// the desired set and identify the records it owns (by comment).
	ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error)
	// ApplyDNSChanges applies a planned change set to a zone via the
	// transactional batch endpoint, chunked as needed.
	ApplyDNSChanges(ctx context.Context, zoneID string, changes DNSChanges) error
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

// ListZones returns all zones on the account, used to match a hostname to the
// zone that should hold its record (longest DNS suffix wins).
func (c *client) ListZones(ctx context.Context) ([]Zone, error) {
	iter := c.api.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(c.accountID)}),
	})
	var out []Zone
	for iter.Next() {
		z := iter.Current()
		out = append(out, Zone{ID: z.ID, Name: NormalizeDNSName(z.Name)})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("listing zones: %w", err)
	}
	return out, nil
}

// ListDNSRecords returns the CNAME records in a zone. Names and targets are
// normalized so they compare cleanly against desired records. Ownership and
// conflict decisions are left to the caller (which filters by comment).
func (c *client) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	iter := c.api.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Type:   cf.F(dns.RecordListParamsTypeCNAME),
	})
	var out []DNSRecord
	for iter.Next() {
		r := iter.Current()
		out = append(out, DNSRecord{
			ID:      r.ID,
			Name:    NormalizeDNSName(r.Name),
			Type:    "CNAME",
			Content: NormalizeDNSName(r.Content),
			Proxied: r.Proxied,
			Comment: r.Comment,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("listing DNS records for zone %s: %w", zoneID, err)
	}
	return out, nil
}

// ApplyDNSChanges applies a change set to a zone using the transactional batch
// endpoint, splitting into multiple atomic batches when the change set exceeds
// dnsBatchChunkSize.
func (c *client) ApplyDNSChanges(ctx context.Context, zoneID string, changes DNSChanges) error {
	if changes.Empty() {
		return nil
	}
	for _, chunk := range chunkDNSChanges(changes, dnsBatchChunkSize) {
		if _, err := c.api.DNS.Records.Batch(ctx, toV7BatchParams(zoneID, chunk)); err != nil {
			return fmt.Errorf("applying DNS batch for zone %s: %w", zoneID, err)
		}
	}
	return nil
}

// chunkDNSChanges splits a change set so each chunk holds at most size total
// mutations. Deletes are ordered before updates and creates so a rename within
// one batch frees the old name before the new record is posted.
func chunkDNSChanges(changes DNSChanges, size int) []DNSChanges {
	if size <= 0 {
		return []DNSChanges{changes}
	}
	var out []DNSChanges
	cur := DNSChanges{}
	n := 0
	flush := func() {
		if !cur.Empty() {
			out = append(out, cur)
			cur = DNSChanges{}
			n = 0
		}
	}
	for _, r := range changes.Deletes {
		cur.Deletes = append(cur.Deletes, r)
		if n++; n >= size {
			flush()
		}
	}
	for _, r := range changes.Updates {
		cur.Updates = append(cur.Updates, r)
		if n++; n >= size {
			flush()
		}
	}
	for _, r := range changes.Creates {
		cur.Creates = append(cur.Creates, r)
		if n++; n >= size {
			flush()
		}
	}
	flush()
	return out
}

// toV7BatchParams translates a domain change set into v7 batch params. This is
// part of the single SDK-coupling seam, alongside toV7Ingress.
func toV7BatchParams(zoneID string, changes DNSChanges) dns.RecordBatchParams {
	params := dns.RecordBatchParams{ZoneID: cf.F(zoneID)}
	if len(changes.Deletes) > 0 {
		deletes := make([]dns.RecordBatchParamsDelete, 0, len(changes.Deletes))
		for _, rec := range changes.Deletes {
			deletes = append(deletes, dns.RecordBatchParamsDelete{ID: cf.F(rec.ID)})
		}
		params.Deletes = cf.F(deletes)
	}
	if len(changes.Updates) > 0 {
		patches := make([]dns.BatchPatchUnionParam, 0, len(changes.Updates))
		for _, rec := range changes.Updates {
			patches = append(patches, dns.BatchPatchCNAMERecordParam{
				ID:               cf.F(rec.ID),
				CNAMERecordParam: cnameRecordParam(rec),
			})
		}
		params.Patches = cf.F(patches)
	}
	if len(changes.Creates) > 0 {
		posts := make([]dns.RecordBatchParamsPostUnion, 0, len(changes.Creates))
		for _, rec := range changes.Creates {
			posts = append(posts, cnameRecordParam(rec))
		}
		params.Posts = cf.F(posts)
	}
	return params
}

// cnameRecordParam builds the v7 CNAME record body shared by posts and patches.
// Proxied CNAMEs must use automatic TTL (1).
func cnameRecordParam(rec DNSRecord) dns.CNAMERecordParam {
	p := dns.CNAMERecordParam{
		Name:    cf.F(rec.Name),
		Type:    cf.F(dns.CNAMERecordTypeCNAME),
		TTL:     cf.F(dns.TTL1),
		Content: cf.F(rec.Content),
		Proxied: cf.F(rec.Proxied),
	}
	if rec.Comment != "" {
		p.Comment = cf.F(rec.Comment)
	}
	return p
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

// durationSeconds converts a duration to whole seconds for the v7 API, which
// only accepts integer seconds. A positive sub-second duration floors to 1s
// rather than 0 so a user-specified timeout is never silently dropped to "no
// timeout".
func durationSeconds(d time.Duration) int64 {
	s := int64(d.Seconds())
	if s == 0 && d > 0 {
		s = 1
	}
	return s
}

func toV7OriginRequest(o *OriginRequest) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest {
	var r zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest
	if o.ConnectTimeout != nil {
		r.ConnectTimeout = cf.F(durationSeconds(*o.ConnectTimeout))
	}
	if o.TLSTimeout != nil {
		r.TLSTimeout = cf.F(durationSeconds(*o.TLSTimeout))
	}
	if o.TCPKeepAlive != nil {
		r.TCPKeepAlive = cf.F(durationSeconds(*o.TCPKeepAlive))
	}
	if o.MatchSNIToHost != nil {
		r.MatchSnItoHost = cf.F(*o.MatchSNIToHost)
	}
	if o.NoHappyEyeballs != nil {
		r.NoHappyEyeballs = cf.F(*o.NoHappyEyeballs)
	}
	if o.KeepAliveConnections != nil {
		r.KeepAliveConnections = cf.F(int64(*o.KeepAliveConnections))
	}
	if o.KeepAliveTimeout != nil {
		r.KeepAliveTimeout = cf.F(durationSeconds(*o.KeepAliveTimeout))
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
