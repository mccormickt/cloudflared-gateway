package controller

import (
	"context"
	"fmt"
	"strings"

	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

const (
	// annotationDNSManaged, when set to "false" on a Gateway, opts that Gateway
	// out of DNS management even while the controller-wide flag is on, so its DNS
	// can be managed by hand.
	annotationDNSManaged = "cloudflared-gateway.jan0ski.net/dns-managed"

	// ownerCommentPrefix tags DNS records this controller creates. The full
	// comment includes the owning Gateway's UID so records are only ever updated
	// or deleted by the Gateway that created them; records without this exact
	// comment are never touched.
	ownerCommentPrefix = "cloudflared-gateway:owner="

	// cfargotunnelSuffix is appended to a tunnel ID to form the CNAME target.
	cfargotunnelSuffix = ".cfargotunnel.com"
)

// ownerComment returns the ownership marker for records created for this Gateway.
func ownerComment(gw *gwapiv1.Gateway) string {
	return ownerCommentPrefix + string(gw.UID)
}

// gatewayDNSEnabled reports whether DNS management applies to this Gateway. It is
// on by default (when the controller flag is set) and only disabled by an
// explicit dns-managed="false" annotation.
func gatewayDNSEnabled(gw *gwapiv1.Gateway) bool {
	return !strings.EqualFold(gw.Annotations[annotationDNSManaged], "false")
}

// collectRouteHostnames returns the deduplicated, normalized set of hostnames
// served by the Gateway's attached HTTP/gRPC/TLS routes. TCPRoutes are
// port-based and carry no hostnames, so they contribute nothing.
func collectRouteHostnames(httpRoutes []gwapiv1.HTTPRoute, grpcRoutes []gwapiv1.GRPCRoute, tlsRoutes []gwapiv1alpha2.TLSRoute) []string {
	seen := map[string]bool{}
	var out []string
	add := func(hostnames []gwapiv1.Hostname) {
		for _, h := range hostnames {
			name := cfclient.NormalizeDNSName(string(h))
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	for i := range httpRoutes {
		add(httpRoutes[i].Spec.Hostnames)
	}
	for i := range grpcRoutes {
		add(grpcRoutes[i].Spec.Hostnames)
	}
	for i := range tlsRoutes {
		add(tlsRoutes[i].Spec.Hostnames)
	}
	return out
}

// matchZone returns the zone whose name is the longest DNS suffix of hostname.
// e.g. for "a.b.example.com" with zones "example.com" and "b.example.com", the
// latter wins.
func matchZone(hostname string, zones []cfclient.Zone) (cfclient.Zone, bool) {
	var best cfclient.Zone
	found := false
	for _, z := range zones {
		if z.Name == "" {
			continue
		}
		if hostname == z.Name || strings.HasSuffix(hostname, "."+z.Name) {
			if !found || len(z.Name) > len(best.Name) {
				best, found = z, true
			}
		}
	}
	return best, found
}

// planDNSChanges diffs the desired records for a single zone against the zone's
// current records, restricting mutations to records this controller owns
// (matched by ownerComment). It returns the change set plus the names of any
// desired records that collide with a record owned by someone else (never
// clobbered).
func planDNSChanges(desired, current []cfclient.DNSRecord, ownerComment string) (cfclient.DNSChanges, []string) {
	// Index current records by name. When a name somehow has both an owned and an
	// unowned record, prefer the owned one for matching.
	currentByName := map[string]cfclient.DNSRecord{}
	for _, r := range current {
		if existing, ok := currentByName[r.Name]; ok && existing.Comment == ownerComment {
			continue
		}
		currentByName[r.Name] = r
	}

	var changes cfclient.DNSChanges
	var conflicts []string
	desiredNames := map[string]bool{}

	for _, d := range desired {
		desiredNames[d.Name] = true
		cur, ok := currentByName[d.Name]
		switch {
		case !ok:
			changes.Creates = append(changes.Creates, d)
		case cur.Comment != ownerComment:
			conflicts = append(conflicts, d.Name)
		case cur.Content != d.Content || cur.Proxied != d.Proxied:
			d.ID = cur.ID
			changes.Updates = append(changes.Updates, d)
		}
	}

	// Prune owned records in this zone whose name is no longer desired.
	for _, r := range current {
		if r.Comment == ownerComment && !desiredNames[r.Name] {
			changes.Deletes = append(changes.Deletes, r)
		}
	}
	return changes, conflicts
}

// reconcileDNS ensures a proxied CNAME (hostname → <tunnelID>.cfargotunnel.com)
// exists for each attached route hostname, and prunes owned records for
// hostnames no longer served across every accessible zone. It is a no-op unless
// DNS management is enabled for the Gateway.
//
// Work is best-effort per zone: a failure in one zone does not stop the others,
// and the first error is returned so the Gateway requeues.
func (r *GatewayReconciler) reconcileDNS(ctx context.Context, gw *gwapiv1.Gateway, tunnelID string, hostnames []string) error {
	if !r.ManageDNS || !gatewayDNSEnabled(gw) || tunnelID == "" {
		return nil
	}
	logger := log.FromContext(ctx)

	zones, err := r.CloudflareClient.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("listing zones: %w", err)
	}

	owner := ownerComment(gw)
	target := cfclient.NormalizeDNSName(tunnelID + cfargotunnelSuffix)

	// Group desired records by the zone that should hold them.
	desiredByZone := map[string][]cfclient.DNSRecord{}
	for _, h := range hostnames {
		name := cfclient.NormalizeDNSName(h)
		if name == "" {
			continue
		}
		// Wildcard hostnames are skipped: a proxied wildcard CNAME requires an
		// Enterprise plan, and Cloudflare rejects the whole transactional batch
		// otherwise — which would deny DNS to every other hostname in the zone.
		// Manage wildcard DNS by hand.
		if strings.HasPrefix(name, "*.") {
			logger.Info("Skipping DNS for wildcard hostname; manage it manually", "hostname", name)
			continue
		}
		zone, ok := matchZone(name, zones)
		if !ok {
			logger.Info("No Cloudflare zone for hostname; skipping DNS record", "hostname", name)
			continue
		}
		desiredByZone[zone.ID] = append(desiredByZone[zone.ID], cfclient.DNSRecord{
			Name:    name,
			Type:    "CNAME",
			Content: target,
			Proxied: true,
			Comment: owner,
		})
	}

	var firstErr error
	for _, zone := range zones {
		current, err := r.CloudflareClient.ListDNSRecords(ctx, zone.ID)
		if err != nil {
			logger.Error(err, "Failed to list DNS records", "zone", zone.Name)
			if firstErr == nil {
				firstErr = fmt.Errorf("listing DNS records in zone %q: %w", zone.Name, err)
			}
			continue
		}

		changes, conflicts := planDNSChanges(desiredByZone[zone.ID], current, owner)
		for _, name := range conflicts {
			logger.Info("DNS record exists and is not managed by this Gateway; leaving it untouched", "hostname", name, "zone", zone.Name)
			if firstErr == nil {
				firstErr = fmt.Errorf("DNS record %q in zone %q is not managed by Gateway %s/%s", name, zone.Name, gw.Namespace, gw.Name)
			}
		}
		if changes.Empty() {
			continue
		}
		if err := r.CloudflareClient.ApplyDNSChanges(ctx, zone.ID, changes); err != nil {
			logger.Error(err, "Failed to apply DNS changes", "zone", zone.Name)
			if firstErr == nil {
				firstErr = fmt.Errorf("applying DNS changes in zone %q: %w", zone.Name, err)
			}
			continue
		}
		logger.Info("Applied DNS changes", "zone", zone.Name,
			"created", len(changes.Creates), "updated", len(changes.Updates), "deleted", len(changes.Deletes))
	}
	return firstErr
}

// teardownDNS deletes every DNS record owned by this Gateway across all zones.
// Used during finalizer cleanup; best-effort, returning the first error.
func (r *GatewayReconciler) teardownDNS(ctx context.Context, gw *gwapiv1.Gateway) error {
	if !r.ManageDNS {
		return nil
	}
	logger := log.FromContext(ctx)

	zones, err := r.CloudflareClient.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("listing zones: %w", err)
	}

	owner := ownerComment(gw)
	var firstErr error
	for _, z := range zones {
		current, err := r.CloudflareClient.ListDNSRecords(ctx, z.ID)
		if err != nil {
			logger.Error(err, "Cleanup: failed to list DNS records", "zone", z.Name)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var changes cfclient.DNSChanges
		for _, rec := range current {
			if rec.Comment == owner {
				changes.Deletes = append(changes.Deletes, rec)
			}
		}
		if changes.Empty() {
			continue
		}
		if err := r.CloudflareClient.ApplyDNSChanges(ctx, z.ID, changes); err != nil {
			logger.Error(err, "Cleanup: failed to delete DNS records", "zone", z.Name)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		logger.Info("Cleanup: deleted DNS records", "zone", z.Name, "count", len(changes.Deletes))
	}
	return firstErr
}
