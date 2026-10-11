package chart

import (
	"bytes"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func TestGatewayAPIBundle(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart tests")
	}
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "administrator managed", true: "bundled"}[enabled], func(t *testing.T) {
			args := []string{"template", "test", "../../charts/cloudflared-gateway", "--include-crds", "--kube-version", "1.35.0"}
			if enabled {
				args = append(args, "--set", "gatewayAPI.enabled=true")
			}
			out, err := exec.CommandContext(t.Context(), "helm", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, out)
			}
			roles := map[string]rbacv1.ClusterRole{}
			found := map[string]bool{}
			for _, doc := range bytes.Split(out, []byte("\n---")) {
				var resource unstructured.Unstructured
				if err := yaml.Unmarshal(doc, &resource.Object); err != nil {
					t.Fatal(err)
				}
				found[resource.GetKind()+"/"+resource.GetName()] = true
				switch resource.GetKind() {
				case "Job":
					t.Fatal("chart must not run a privileged installer Job")
				case "ClusterRole":
					var role rbacv1.ClusterRole
					if err := yaml.Unmarshal(doc, &role); err != nil {
						t.Fatal(err)
					}
					roles[role.Name] = role
				case "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding":
					if resource.GetAnnotations()["helm.sh/resource-policy"] != "keep" {
						t.Fatal("uninstall must not remove shared safe-upgrade protections")
					}
				}
			}
			for _, name := range []string{
				"CustomResourceDefinition/gateways.gateway.networking.k8s.io",
				"CustomResourceDefinition/xbackends.gateway.networking.x-k8s.io",
				"ValidatingAdmissionPolicy/safe-upgrades.gateway.networking.k8s.io",
				"ValidatingAdmissionPolicyBinding/safe-upgrades.gateway.networking.k8s.io",
			} {
				if found[name] != enabled {
					t.Errorf("%s present=%v, want %v", name, found[name], enabled)
				}
			}
			if len(roles) != 1 {
				t.Fatalf("expected only the controller role, found %d", len(roles))
			}
			manifest, err := os.ReadFile("../../config/rbac/role.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var generated rbacv1.ClusterRole
			if err := yaml.Unmarshal(manifest, &generated); err != nil {
				t.Fatal(err)
			}
			roles[generated.Name] = generated
			for name, role := range roles {
				for _, check := range []struct {
					group, resource, verb string
					want                  bool
				}{
					{"", "secrets", "watch", true},
					{"apps", "deployments", "watch", true},
					{"apiextensions.k8s.io", "customresourcedefinitions", "get", true},
					{"apiextensions.k8s.io", "customresourcedefinitions", "create", false},
					{"apiextensions.k8s.io", "customresourcedefinitions", "update", false},
					{"apiextensions.k8s.io", "customresourcedefinitions", "patch", false},
					{"apiextensions.k8s.io", "customresourcedefinitions", "delete", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicies", "create", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicies", "update", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicies", "patch", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicies", "delete", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicybindings", "create", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicybindings", "update", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicybindings", "patch", false},
					{"admissionregistration.k8s.io", "validatingadmissionpolicybindings", "delete", false},
				} {
					allowed := false
					for _, rule := range role.Rules {
						if (slices.Contains(rule.APIGroups, check.group) || slices.Contains(rule.APIGroups, "*")) &&
							(slices.Contains(rule.Resources, check.resource) || slices.Contains(rule.Resources, "*")) &&
							(slices.Contains(rule.Verbs, check.verb) || slices.Contains(rule.Verbs, "*")) {
							allowed = true
						}
					}
					if allowed != check.want {
						t.Errorf("%s: %s %s.%s allowed=%v, want %v", name, check.verb, check.resource, check.group, allowed, check.want)
					}
				}
			}
		})
	}
	for _, dns := range []bool{false, true} {
		for _, backends := range []bool{false, true} {
			t.Run("dns="+strconv.FormatBool(dns)+"/backends="+strconv.FormatBool(backends), func(t *testing.T) {
				out, err := exec.CommandContext(t.Context(), "helm", "template", "test", "../../charts/cloudflared-gateway",
					"--show-only", "templates/deployment.yaml", "--set", "dns.enabled="+strconv.FormatBool(dns),
					"--set", "experimental.backends.enabled="+strconv.FormatBool(backends)).CombinedOutput()
				if err != nil {
					t.Fatalf("helm template: %v\n%s", err, out)
				}
				var deployment appsv1.Deployment
				if err := yaml.Unmarshal(out, &deployment); err != nil {
					t.Fatal(err)
				}
				for name, enabled := range map[string]bool{"ENABLE_DNS_MANAGEMENT": dns, "ENABLE_EXPERIMENTAL_BACKENDS": backends} {
					found := false
					for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
						if env.Name == name {
							found = true
							if env.Value != "true" {
								t.Errorf("%s=%q, want true", name, env.Value)
							}
						}
					}
					if found != enabled {
						t.Errorf("%s present=%v, want %v", name, found, enabled)
					}
				}
			})
		}
	}
	for _, tc := range []struct {
		name, version, value string
		wantError            bool
	}{
		{"bundle minimum", "1.30.0", "gatewayAPI.enabled=true", false},
		{"bundle unsupported", "1.29.0", "gatewayAPI.enabled=true", true},
		{"external bundle", "1.28.0", "gatewayAPI.enabled=false", false},
		{"retired installer", "1.35.0", "experimental.installGatewayAPICRDs=true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.CommandContext(t.Context(), "helm", "template", "test", "../../charts/cloudflared-gateway", "--kube-version", tc.version, "--set", tc.value).CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("helm template error=%v, want error=%v\n%s", err, tc.wantError, out)
			}
		})
	}
}
