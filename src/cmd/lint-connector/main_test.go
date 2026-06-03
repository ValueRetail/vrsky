package main

import (
	"go/parser"
	"go/token"
	"testing"
)

// parse folds a source snippet into pkgInfo via analyzeFile.
func parse(t *testing.T, src string) pkgInfo {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var info pkgInfo
	analyzeFile(f, &info)
	return info
}

func TestAnalyzeFile_DetectsSDKAndRun(t *testing.T) {
	info := parse(t, `package main
import "github.com/ValueRetail/vrsky/pkg/sdk"
func main() { _ = sdk.RunConsumer(nil, "x", nil) }`)
	if !info.importsSDK {
		t.Error("expected importsSDK")
	}
	if !info.runCalled {
		t.Error("expected runCalled for sdk.RunConsumer")
	}
	if info.importsMessaging {
		t.Error("did not expect importsMessaging")
	}
}

func TestAnalyzeFile_AliasedSDKImport(t *testing.T) {
	info := parse(t, `package main
import vsdk "github.com/ValueRetail/vrsky/pkg/sdk"
func main() { _ = vsdk.RunProducer(nil, "x", nil) }`)
	if !info.importsSDK || !info.runCalled {
		t.Errorf("aliased import: importsSDK=%v runCalled=%v", info.importsSDK, info.runCalled)
	}
}

func TestAnalyzeFile_DetectsMessaging(t *testing.T) {
	info := parse(t, `package main
import (
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/messaging"
)
var _ = messaging.NewPublisher
func main() { sdk.RunConsumer(nil, "x", nil) }`)
	if !info.importsMessaging {
		t.Error("expected importsMessaging")
	}
}

func TestViolations(t *testing.T) {
	cases := []struct {
		name string
		info pkgInfo
		want int
	}{
		{"not a connector", pkgInfo{importsSDK: false}, 0},
		{"good connector", pkgInfo{importsSDK: true, runCalled: true}, 0},
		{"missing run", pkgInfo{importsSDK: true, runCalled: false}, 1},
		{"sdk bypass via messaging", pkgInfo{importsSDK: true, runCalled: true, importsMessaging: true}, 1},
		{"bypass suppressed", pkgInfo{importsSDK: true, runCalled: true, importsMessaging: true, suppressed: true}, 0},
		{"missing run AND bypass", pkgInfo{importsSDK: true, importsMessaging: true}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.info.violations("demo")
			if len(got) != tc.want {
				t.Errorf("violations = %d (%v), want %d", len(got), got, tc.want)
			}
		})
	}
}
