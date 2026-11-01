package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSigningIdentitySelection(t *testing.T) {
	const fingerprint = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	const name = "Developer ID Application: Example Corp (TEAM123)"
	const valid = "  1) " + fingerprint + " \"" + name + "\"\n     1 valid identities found\n"
	tests := []struct{ name, selector, identities, failure string }{
		{"automatic team selection", "", valid, ""},
		{"automatic wrong team", "", strings.ReplaceAll(valid, "TEAM123", "OTHER"), "no valid Developer ID Application identity"},
		{"explicit wrong team", fingerprint, strings.ReplaceAll(valid, "TEAM123", "OTHER"), "no valid Developer ID Application identity"},
		{"automatic wrong certificate type", "", strings.ReplaceAll(valid, "Developer ID Application:", "Apple Development:"), "no valid Developer ID Application identity"},
		{"automatic ambiguity", "", valid + "  2) 1111111111111111111111111111111111111111 \"Developer ID Application: Another Corp (TEAM123)\"\n", "matches multiple"},
		{"automatic ignores other teams", "", valid + "  2) 1111111111111111111111111111111111111111 \"Developer ID Application: Example Corp (OTHER)\"\n", ""},
		{"full name", name, valid, ""},
		{"fingerprint", fingerprint, valid, ""},
		{"lowercase fingerprint", strings.ToLower(fingerprint), valid, ""},
		{"name substring", "Example Corp", valid, ""},
		{"team substring", "TEAM123", valid, ""},
		{"missing", "Other Corp", valid, "no valid Developer ID Application identity"},
		{"wrong certificate type", fingerprint, strings.ReplaceAll(valid, "Developer ID Application:", "Apple Development:"), "no valid Developer ID Application identity"},
		{"invalid certificate", fingerprint, strings.ReplaceAll(valid, "\"\n", "\" (CSSMERR_TP_CERT_EXPIRED)\n"), "no valid Developer ID Application identity"},
		{"ambiguous", "Example Corp", valid + "  2) 1111111111111111111111111111111111111111 \"Developer ID Application: Example Corp (TEAM123)\"\n", "matches multiple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/usr/bin/env bash
set -eu
case "${0##*/}" in
 uname) echo Darwin ;;
 security)
  case "$1" in
   list-keychains) if [[ $# == 3 ]]; then printf '    "/original/keychain with spaces"\n'; fi ;;
   find-identity) printf '%s' "$TEST_IDENTITIES" ;;
   delete-keychain) touch "$TEST_DIR/cleaned" ;;
  esac ;;
 codesign) printf '%s\n' "$*" >> "$TEST_DIR/signed" ;;
 xcrun) printf '%s\n' "$*" >> "$TEST_DIR/notarized" ;;
 plutil) echo Accepted ;;
 ditto) : ;;
esac
`
			for _, command := range []string{"uname", "security", "codesign", "xcrun", "plutil", "ditto"} {
				if err := os.WriteFile(filepath.Join(dir, command), []byte(mock), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "sign-macos-release.sh")
			// Keep credentials entirely synthetic; all Apple commands are mocked.
			cmd.Env = []string{
				"PATH=" + dir + ":/usr/bin:/bin", "RUNNER_TEMP=" + dir,
				"TEST_DIR=" + dir, "TEST_IDENTITIES=" + tt.identities,
				"DEVELOPER_ID_CERTIFICATE_BASE64=dGVzdA==", "DEVELOPER_ID_CERTIFICATE_PASSWORD=test",
				"APPLE_ID=test@example.invalid", "APPLE_TEAM_ID=TEAM123", "APPLE_APP_SPECIFIC_PASSWORD=test",
			}
			if tt.selector != "" {
				cmd.Env = append(cmd.Env, "DEVELOPER_ID_APPLICATION="+tt.selector)
			}
			output, err := cmd.CombinedOutput()
			if tt.failure != "" {
				if err == nil || !strings.Contains(string(output), tt.failure) {
					t.Fatalf("expected %q, got %v: %s", tt.failure, err, output)
				}
				for _, file := range []string{"signed", "notarized"} {
					if _, err := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(err) {
						t.Fatalf("unexpected %s operation", file)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("%v: %s", err, output)
				}
				signed, err := os.ReadFile(filepath.Join(dir, "signed"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(signed), "--sign "+fingerprint+" --keychain") != 2 {
					t.Fatalf("expected both architectures signed by fingerprint: %s", signed)
				}
				if strings.Count(string(output), "Apple accepted notarization") != 2 {
					t.Fatalf("expected both architectures notarized: %s", output)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "cleaned")); err != nil {
				t.Fatalf("keychain was not cleaned up: %v", err)
			}
		})
	}
}
