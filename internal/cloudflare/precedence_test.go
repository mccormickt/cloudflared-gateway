package cloudflare

import (
	"testing"
	"time"
)

// TestMorePrecedent_TieBreakTiers checks each Gateway API tie-break dimension in
// isolation: two rules differing in exactly one tier, the more-specific ranking
// first.
func TestMorePrecedent_TieBreakTiers(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	tests := []struct {
		name string
		more BuiltRule // expected higher precedence
		less BuiltRule
	}{
		{
			name: "exact hostname beats wildcard",
			more: BuiltRule{Specificity: MatchSpecificity{HostnameExact: true, HostnameLen: 5}},
			less: BuiltRule{Specificity: MatchSpecificity{HostnameExact: false, HostnameLen: 12}},
		},
		{
			name: "wildcard hostname beats no hostname",
			more: BuiltRule{Specificity: MatchSpecificity{HostnameExact: false, HostnameLen: 12}},
			less: BuiltRule{Specificity: MatchSpecificity{HostnameExact: false, HostnameLen: 0}},
		},
		{
			name: "longer matching hostname wins",
			more: BuiltRule{Specificity: MatchSpecificity{HostnameExact: true, HostnameLen: 20}},
			less: BuiltRule{Specificity: MatchSpecificity{HostnameExact: true, HostnameLen: 10}},
		},
		{
			name: "exact path beats prefix",
			more: BuiltRule{Specificity: MatchSpecificity{PathKind: pathExact}},
			less: BuiltRule{Specificity: MatchSpecificity{PathKind: pathPrefix, PrefixLen: 99}},
		},
		{
			name: "prefix beats regex",
			more: BuiltRule{Specificity: MatchSpecificity{PathKind: pathPrefix}},
			less: BuiltRule{Specificity: MatchSpecificity{PathKind: pathRegex}},
		},
		{
			name: "regex beats none",
			more: BuiltRule{Specificity: MatchSpecificity{PathKind: pathRegex}},
			less: BuiltRule{Specificity: MatchSpecificity{PathKind: pathNone}},
		},
		{
			name: "longer prefix literal wins",
			more: BuiltRule{Specificity: MatchSpecificity{PathKind: pathPrefix, PrefixLen: 8}},
			less: BuiltRule{Specificity: MatchSpecificity{PathKind: pathPrefix, PrefixLen: 4}},
		},
		{
			name: "method match wins",
			more: BuiltRule{Specificity: MatchSpecificity{MethodMatch: true}},
			less: BuiltRule{Specificity: MatchSpecificity{MethodMatch: false}},
		},
		{
			name: "more header matches wins",
			more: BuiltRule{Specificity: MatchSpecificity{HeaderCount: 2}},
			less: BuiltRule{Specificity: MatchSpecificity{HeaderCount: 1}},
		},
		{
			name: "more query matches wins",
			more: BuiltRule{Specificity: MatchSpecificity{QueryCount: 3}},
			less: BuiltRule{Specificity: MatchSpecificity{QueryCount: 0}},
		},
		{
			name: "oldest route wins",
			more: BuiltRule{RouteCreated: t0},
			less: BuiltRule{RouteCreated: t1},
		},
		{
			name: "alphabetical namespace wins on equal age",
			more: BuiltRule{RouteCreated: t0, RouteNamespace: "aaa"},
			less: BuiltRule{RouteCreated: t0, RouteNamespace: "bbb"},
		},
		{
			name: "alphabetical name wins on equal namespace",
			more: BuiltRule{RouteCreated: t0, RouteNamespace: "ns", RouteName: "aaa"},
			less: BuiltRule{RouteCreated: t0, RouteNamespace: "ns", RouteName: "bbb"},
		},
		{
			name: "earlier rule index wins",
			more: BuiltRule{RouteCreated: t0, RouteNamespace: "ns", RouteName: "r", RuleIndex: 0},
			less: BuiltRule{RouteCreated: t0, RouteNamespace: "ns", RouteName: "r", RuleIndex: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !morePrecedent(tt.more, tt.less) {
				t.Errorf("expected `more` to outrank `less`")
			}
			if morePrecedent(tt.less, tt.more) {
				t.Errorf("expected `less` NOT to outrank `more` (asymmetry)")
			}
		})
	}
}

// TestSortByPrecedence_Orders verifies a shuffled mix sorts into the expected
// precedence order, labelled via Service for readable assertions.
func TestSortByPrecedence_Orders(t *testing.T) {
	t0 := time.Unix(1000, 0)

	label := func(s string, spec MatchSpecificity) BuiltRule {
		return BuiltRule{
			IngressRule:  IngressRule{Service: s},
			RouteCreated: t0, RouteNamespace: "ns", RouteName: "r",
			Specificity: spec,
		}
	}

	exactHost := func(k pathKind, prefixLen int) MatchSpecificity {
		return MatchSpecificity{HostnameExact: true, HostnameLen: 11, PathKind: k, PrefixLen: prefixLen}
	}

	rules := []BuiltRule{
		label("none", exactHost(pathNone, 0)),
		label("exact", exactHost(pathExact, 7)),
		label("prefix-long", exactHost(pathPrefix, 8)),
		label("prefix-short", exactHost(pathPrefix, 4)),
		label("regex", exactHost(pathRegex, 0)),
		label("wildcard-host", MatchSpecificity{HostnameExact: false, HostnameLen: 8, PathKind: pathExact}),
	}

	SortByPrecedence(rules)

	want := []string{"exact", "prefix-long", "prefix-short", "regex", "none", "wildcard-host"}
	for i, w := range want {
		if rules[i].Service != w {
			t.Errorf("position %d: got %q, want %q (full order: %v)", i, rules[i].Service, w, services(rules))
		}
	}
}

func services(rules []BuiltRule) []string {
	out := make([]string, len(rules))
	for i := range rules {
		out[i] = rules[i].Service
	}
	return out
}
