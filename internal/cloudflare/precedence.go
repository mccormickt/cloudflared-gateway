package cloudflare

import "sort"

// SortByPrecedence orders builder-stage rules into Gateway API match precedence,
// most-specific first. Cloudflare evaluates ingress rules first-match-wins, so
// emitting them in this order makes the more-specific match win — matching
// Gateway API's most-specific-wins semantics. The sort is stable, so rules that
// tie on every dimension keep their build order.
func SortByPrecedence(rules []BuiltRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		return morePrecedent(rules[i], rules[j])
	})
}

// morePrecedent reports whether a should be evaluated before b. It implements
// the Gateway API HTTPRoute tie-break order (apis/v1 httproute_types.go):
// hostname (exact > wildcard, then longer) → path class (Exact > Prefix > Regex
// > None) → longer prefix literal → method match → more header matches → more
// query-param matches → oldest route → alphabetical {namespace}/{name} → earlier
// rule index.
func morePrecedent(a, b BuiltRule) bool {
	as, bs := a.Specificity, b.Specificity

	// Hostname precedence.
	if as.HostnameExact != bs.HostnameExact {
		return as.HostnameExact
	}
	if as.HostnameLen != bs.HostnameLen {
		return as.HostnameLen > bs.HostnameLen
	}

	// Path class, then prefix/exact literal length.
	if as.PathKind != bs.PathKind {
		return as.PathKind > bs.PathKind
	}
	if as.PrefixLen != bs.PrefixLen {
		return as.PrefixLen > bs.PrefixLen
	}

	// Method, then header and query-param match counts.
	if as.MethodMatch != bs.MethodMatch {
		return as.MethodMatch
	}
	if as.HeaderCount != bs.HeaderCount {
		return as.HeaderCount > bs.HeaderCount
	}
	if as.QueryCount != bs.QueryCount {
		return as.QueryCount > bs.QueryCount
	}

	// Cross-route tie-breaks: oldest route, then alphabetical namespace/name.
	if !a.RouteCreated.Equal(b.RouteCreated) {
		return a.RouteCreated.Before(b.RouteCreated)
	}
	if a.RouteNamespace != b.RouteNamespace {
		return a.RouteNamespace < b.RouteNamespace
	}
	if a.RouteName != b.RouteName {
		return a.RouteName < b.RouteName
	}

	// Within a route, earlier rule wins.
	return a.RuleIndex < b.RuleIndex
}
