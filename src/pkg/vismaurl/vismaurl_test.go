package vismaurl

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	const okBase = "https://salesorder.visma.net/api/v3"
	tests := []struct {
		name         string
		base, token  string
		allowPrivate bool
		wantErr      string // substring; empty = accepted
	}{
		{"real defaults", okBase, "", false, ""},
		{"real defaults, explicit token url", okBase, DefaultTokenURL, false, ""},
		{"another real service host", "https://api.finance.visma.net/v1", DefaultTokenURL, false, ""},
		{"both unset is the incomplete-config case, not judged here", "", "", false, ""},
		{"base http", "http://salesorder.visma.net/api/v3", "", false, "base_url"},
		{"base loopback", "https://127.0.0.1/v3", "", false, "base_url"},
		{"base ipv6 loopback", "https://[::1]/v3", "", false, "base_url"},
		{"base private 10/8", "https://10.1.2.3/v3", "", false, "base_url"},
		{"base private 172.16/12", "https://172.20.0.1/v3", "", false, "base_url"},
		{"base private 192.168/16", "https://192.168.0.1/v3", "", false, "base_url"},
		{"base metadata", "https://169.254.169.254/latest", "", false, "base_url"},
		{"base azure wire server", "https://168.63.129.16/", "", false, "base_url"},
		{"base userinfo", "https://u:p@salesorder.visma.net/api/v3", "", false, "base_url"},
		{"base not a url", "salesorder.visma.net", "", false, "base_url"},
		{"token http", okBase, "http://connect.visma.com/connect/token", false, "token_url"},
		{"token loopback", okBase, "https://127.0.0.1:8443/token", false, "token_url"},
		{"token metadata", okBase, "https://169.254.169.254/token", false, "token_url"},
		{"token private", okBase, "https://10.0.0.7/token", false, "token_url"},
		{"token ipv6 loopback", okBase, "https://[::1]/token", false, "token_url"},
		{"token userinfo", okBase, "https://u:p@connect.visma.com/connect/token", false, "token_url"},
		{"dev flag: http mock", "http://visma-mock:8080/api", "http://visma-mock:8080/token", true, ""},
		{"dev flag: loopback mock", "http://127.0.0.1:9000/api", "http://127.0.0.1:9000/token", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Check(tt.base, tt.token, tt.allowPrivate)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Check = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Check = nil, want an error naming %s", tt.wantErr)
			case tt.wantErr != "" && !strings.HasPrefix(err.Error(), tt.wantErr):
				t.Fatalf("error %q should start with %q", err, tt.wantErr)
			}
			if err != nil && tt.wantErr == "token_url" && strings.Contains(err.Error(), "base_url") {
				t.Errorf("a token_url error should not name base_url: %v", err)
			}
		})
	}
}
