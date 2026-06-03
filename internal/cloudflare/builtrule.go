package cloudflare

import "time"

// pathKind classifies an HTTPRoute path match for Gateway API precedence
// ranking. Higher values rank higher (more specific): Exact > Prefix > Regex >
// None. The precedence of RegularExpression matches is implementation-defined by
// the Gateway API spec; ranking it between Prefix and None is this controller's
// chosen convention.
type pathKind int

const (
	pathNone   pathKind = iota // hostname-only / no path match
	pathRegex                  // RegularExpression
	pathPrefix                 // PathPrefix
	pathExact                  // Exact
)

// MatchSpecificity captures the Gateway API tie-break dimensions of the match
// that produced an ingress rule. Cloudflare can only match on hostname and path,
// so MethodMatch/HeaderCount/QueryCount do not change the emitted rule — they
// only affect precedence ranking and unsupported-match reporting.
type MatchSpecificity struct {
	HostnameExact bool // hostname has no leading wildcard
	HostnameLen   int  // length of the matching hostname (wildcard label trimmed)
	PathKind      pathKind
	PrefixLen     int  // char count of the literal path before regex encoding
	MethodMatch   bool // an HTTPRoute method match is present (always true for gRPC)
	HeaderCount   int
	QueryCount    int
}

// BuiltRule is the builder-stage carrier for an ingress rule: a Cloudflare
// IngressRule plus the provenance and specificity needed to (a) attach policy by
// route identity instead of positional index and (b) sort rules into Gateway API
// precedence order. It is projected back to a plain IngressRule (via
// ToIngressRules) only just before the configuration is pushed to Cloudflare, so
// the SDK boundary in client.go is unaffected.
type BuiltRule struct {
	IngressRule

	RouteKind      string // "HTTPRoute" | "GRPCRoute" | "TLSRoute" | "TCPRoute"
	RouteNamespace string
	RouteName      string
	RouteCreated   time.Time // route creationTimestamp, for the oldest-route tie-break
	RuleIndex      int       // index of the source rule within route.Spec.Rules
	Specificity    MatchSpecificity

	// UnsupportedMatch is true when the source match used a dimension Cloudflare
	// cannot enforce (method, header, or query-param match). The rule is still
	// emitted (degraded to hostname+path) but the route's status reflects this.
	UnsupportedMatch bool
}

// ToIngressRules projects builder-stage rules down to the plain IngressRule slice
// pushed to the Cloudflare configuration API.
func ToIngressRules(rules []BuiltRule) []IngressRule {
	out := make([]IngressRule, len(rules))
	for i := range rules {
		out[i] = rules[i].IngressRule
	}
	return out
}
