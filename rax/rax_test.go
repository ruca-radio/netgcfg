package rax

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// synthetic.cfg is a self-contained RAX/Broadcom config backup built by the
// test-fixture generator. It contains no real device credentials, so it can be
// committed and used to pin the codec against regressions.
const syntheticPath = "testdata/synthetic.cfg"

// The user's real device backup, if present in the repo root. This test skips
// quietly when the file is absent so the suite is not tied to local data.
const liveConfigPath = "../NETGEAR_RAX29.cfg"

func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(syntheticPath))
	if err != nil {
		t.Fatalf("read test fixture: %v", err)
	}
	return b
}

func TestLZWKnownVectors(t *testing.T) {
	cases := []struct {
		plain string
		want  string
	}{
		{"TOBEORNOTTOBEORTOBEORNOT", "801509e422293ca44e2795205048342e0b0784c040"},
		{"AAAAABAAAAACAAAAAD", "8010605022140e04438310489010"},
	}
	for _, tc := range cases {
		got := lzwCompress([]byte(tc.plain))
		if hex.EncodeToString(got) != tc.want {
			t.Errorf("lzwCompress(%q) = %s, want %s", tc.plain, hex.EncodeToString(got), tc.want)
		}
		dec, err := lzwDecompress(got)
		if err != nil {
			t.Fatalf("lzwDecompress(%q): %v", tc.plain, err)
		}
		if string(dec) != tc.plain {
			t.Errorf("round-trip %q -> %q", tc.plain, dec)
		}
	}
}

func TestDecrypt(t *testing.T) {
	fixture := loadFixture(t)
	c, err := Decrypt(fixture)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if c.model != "RAX30" {
		t.Errorf("model = %q, want %q", c.model, "RAX30")
	}
	if c.firmware != "V1.0.0.0" {
		t.Errorf("firmware = %q, want %q", c.firmware, "V1.0.0.0")
	}
	if c.firmwareMajor != "V1.0" {
		t.Errorf("firmwareMajor = %q, want %q", c.firmwareMajor, "V1.0")
	}
	if len(c.configXML) == 0 {
		t.Fatal("empty config payload")
	}
	if c.configXML[len(c.configXML)-1] != 0 {
		t.Error("payload does not end in a NUL terminator")
	}
	if !bytes.HasPrefix(c.configXML, []byte("<?xml")) {
		t.Errorf("payload does not start with an XML declaration: %q", c.configXML[:24])
	}
}

// TestEncryptRoundTripRaw verifies decrypt→encrypt is byte-identical when the
// payload is left untouched. This proves the LZW codec, container layout, and
// JAMCRC all match the device output exactly.
func TestEncryptRoundTripRaw(t *testing.T) {
	fixture := loadFixture(t)
	c, err := Decrypt(fixture)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	reenc, err := c.Encrypt()
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !bytes.Equal(reenc, fixture) {
		t.Fatalf("re-encoded file differs from fixture (%d vs %d bytes)", len(reenc), len(fixture))
	}
}

// TestJSONRoundTrip verifies the parsed (non-raw) JSON path round-trips the
// configuration semantics: decrypt → JSON → encrypt → decrypt → JSON is stable.
func TestJSONRoundTrip(t *testing.T) {
	fixture := loadFixture(t)
	c, err := Decrypt(fixture)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	json1, err := c.ToJSON(false)
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}

	c2, err := FromJSON(json1)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	reenc, err := c2.Encrypt()
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	c3, err := Decrypt(reenc)
	if err != nil {
		t.Fatalf("Decrypt re-encoded: %v", err)
	}
	json2, err := c3.ToJSON(false)
	if err != nil {
		t.Fatalf("ToJSON re-encoded: %v", err)
	}

	if !bytes.Equal(json1, json2) {
		t.Fatal("JSON round-trip changed the configuration")
	}
}

// TestJSONRoundTripRaw verifies the raw JSON path is byte-identical end to end.
func TestJSONRoundTripRaw(t *testing.T) {
	fixture := loadFixture(t)
	c, err := Decrypt(fixture)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	json1, err := c.ToJSON(true)
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	c2, err := FromJSON(json1)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	reenc, err := c2.Encrypt()
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !bytes.Equal(reenc, fixture) {
		t.Fatalf("raw JSON round-trip should be byte-identical (%d vs %d bytes)", len(reenc), len(fixture))
	}
}

// TestLiveConfigRoundTripRaw is a belt-and-braces check against a real device
// backup when one is available locally. It is skipped in CI/clean checkouts.
func TestLiveConfigRoundTripRaw(t *testing.T) {
	b, err := os.ReadFile(filepath.Clean(liveConfigPath))
	if err != nil {
		t.Skipf("live config not present: %v", err)
	}
	c, err := Decrypt(b)
	if err != nil {
		t.Fatalf("Decrypt live config: %v", err)
	}
	reenc, err := c.Encrypt()
	if err != nil {
		t.Fatalf("Encrypt live config: %v", err)
	}
	if !bytes.Equal(reenc, b) {
		t.Fatalf("live re-encode differs (%d vs %d bytes)", len(reenc), len(b))
	}
}
