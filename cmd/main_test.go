package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

func TestExperimentalBackendsEnabled(t *testing.T) {
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
		{name: "flag overrides enabled env", env: new("true"), args: []string{"--enable-experimental-backends=false"}},
		{name: "flag overrides disabled env", env: new("false"), args: []string{"--enable-experimental-backends"}, want: true},
		{name: "flag overrides malformed env", env: new("tru"), args: []string{"--enable-experimental-backends=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENABLE_EXPERIMENTAL_BACKENDS", "")
			if tc.env == nil {
				if err := os.Unsetenv("ENABLE_EXPERIMENTAL_BACKENDS"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("ENABLE_EXPERIMENTAL_BACKENDS", *tc.env)
			}
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.Bool("enable-experimental-backends", false, "")
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			got, err := experimentalBackendsEnabled(fs)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("got (%v, %v), want (%v, error=%v)", got, err, tc.want, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "ENABLE_EXPERIMENTAL_BACKENDS must be a boolean") {
				t.Fatalf("missing actionable error: %v", err)
			}
		})
	}
}
