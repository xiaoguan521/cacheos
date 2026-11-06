package orch

import "testing"

func TestEngineArchNormalizesSupportedHostNames(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{"amd64", "amd64"},
		{"x86_64", "amd64"},
		{"arm64", "arm64"},
		{"aarch64", "arm64"},
		{"loong64", ""},
	} {
		if got := engineArch(tc.raw); got != tc.want {
			t.Errorf("engineArch(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestEngineVersionListIsStableAndDeduplicated(t *testing.T) {
	if got := appendEngineVersion("1.0.0, 1.1.0", "1.1.0"); got != "1.0.0, 1.1.0" {
		t.Fatalf("duplicate version list = %q", got)
	}
	if got := appendEngineVersion(" 1.0.0, ,", "1.1.0"); got != "1.0.0,1.1.0" {
		t.Fatalf("normalized version list = %q", got)
	}
}

func TestDecodeEngineUpgradeRejectsUnsafeVersion(t *testing.T) {
	if _, err := decodeEngineUpgrade(`{"version":"1.2.3;rm -rf /"}`); err == nil {
		t.Fatal("unsafe persisted version was accepted")
	}
	req, err := decodeEngineUpgrade(`{"version":"1.2.3-4","switchover":true}`)
	if err != nil || req.Version != "1.2.3-4" || !req.Switchover {
		t.Fatalf("decode valid upgrade = %#v, %v", req, err)
	}
}
