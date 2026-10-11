package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

func TestBooleanFlagEnabled(t *testing.T) {
	for _, feature := range []struct{ flag, env string }{
		{"enable-experimental-backends", "ENABLE_EXPERIMENTAL_BACKENDS"},
		{"enable-dns-management", "ENABLE_DNS_MANAGEMENT"},
	} {
		for _, tc := range []struct {
			name    string
			env     *string
			args    []string
			want    bool
			wantErr bool
		}{
			{name: "unset"},
			{name: "enabled", env: new("true"), want: true},
			{name: "disabled", env: new("false")},
			{name: "malformed", env: new("tru"), wantErr: true},
			{name: "empty", env: new(""), wantErr: true},
			{name: "flag overrides enabled env", env: new("true"), args: []string{"--" + feature.flag + "=false"}},
			{name: "flag overrides disabled env", env: new("false"), args: []string{"--" + feature.flag}, want: true},
			{name: "flag overrides malformed env", env: new("tru"), args: []string{"--" + feature.flag + "=false"}},
		} {
			t.Run(feature.flag+"/"+tc.name, func(t *testing.T) {
				t.Setenv(feature.env, "")
				if tc.env == nil {
					if err := os.Unsetenv(feature.env); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Setenv(feature.env, *tc.env)
				}
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool(feature.flag, false, "")
				if err := fs.Parse(tc.args); err != nil {
					t.Fatal(err)
				}
				got, err := booleanFlagEnabled(fs, feature.flag, feature.env)
				if got != tc.want || (err != nil) != tc.wantErr {
					t.Fatalf("got (%v, %v), want (%v, error=%v)", got, err, tc.want, tc.wantErr)
				}
				if err != nil && !strings.Contains(err.Error(), feature.env+" must be a boolean") {
					t.Fatalf("missing actionable error: %v", err)
				}
			})
		}
	}
}
