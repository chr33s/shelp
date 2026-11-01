#!/usr/bin/env bash
set -euo pipefail
for variable in DEVELOPER_ID_CERTIFICATE_BASE64 DEVELOPER_ID_CERTIFICATE_PASSWORD \
  APPLE_ID APPLE_TEAM_ID APPLE_APP_SPECIFIC_PASSWORD; do
  if [[ -z "${!variable:-}" ]]; then
    printf 'Missing required signing secret: %s\n' "$variable" >&2
    exit 1
  fi
done
if [[ $(uname -s) != Darwin ]]; then
  printf 'Developer ID signing requires a macOS runner.\n' >&2
  exit 1
fi

umask 077
keychain_password=$(openssl rand -hex 32)
# Preserve the search list, including paths containing spaces. codesign consults
# it for the certificate chain even when --keychain selects the identity.
original_keychains=()
search_list=$(security list-keychains -d user)
while IFS= read -r entry; do
  entry="${entry#*\"}"
  entry="${entry%\"*}"
  [[ -z "$entry" ]] || original_keychains+=("$entry")
done <<< "$search_list"
signing_temp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/shelp-signing.XXXXXX")
keychain="$signing_temp/signing.keychain-db"
cleanup() {
  security list-keychains -d user -s "${original_keychains[@]}" >/dev/null 2>&1 || true
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$signing_temp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s' "$DEVELOPER_ID_CERTIFICATE_BASE64" | base64 --decode > "$signing_temp/certificate.p12"
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 21600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security list-keychains -d user -s "$keychain" "${original_keychains[@]}"
security import "$signing_temp/certificate.p12" -k "$keychain" \
  -P "$DEVELOPER_ID_CERTIFICATE_PASSWORD" -f pkcs12 -T /usr/bin/codesign -T /usr/bin/security
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$keychain_password" "$keychain" >/dev/null
rm "$signing_temp/certificate.p12"
identities=$(security find-identity -v -p codesigning "$keychain")
# Select a valid Developer ID Application identity belonging to the notarizing
# team. An optional codesign name/fingerprint selector can narrow the match.
# Always sign by fingerprint and reject ambiguous matches.
selector=${DEVELOPER_ID_APPLICATION:-}
signing_identity=''
identity_pattern='^[[:space:]]*[0-9]+\)[[:space:]]+([[:xdigit:]]{40})[[:space:]]+"(Developer ID Application: [^"]+)"[[:space:]]*$'
selector_hash=$(printf '%s' "$selector" | tr '[:lower:]' '[:upper:]')
while IFS= read -r entry; do
  if [[ "$entry" =~ $identity_pattern ]]; then
    fingerprint=${BASH_REMATCH[1]}
    identity_name=${BASH_REMATCH[2]}
    [[ "$identity_name" == *" ($APPLE_TEAM_ID)" ]] || continue
    if [[ -z "$selector" || "$fingerprint" == "$selector_hash" || "$identity_name" == *"$selector"* ]]; then
      if [[ -n "$signing_identity" && "$signing_identity" != "$fingerprint" ]]; then
        printf 'Signing configuration matches multiple Developer ID Application identities for APPLE_TEAM_ID; set DEVELOPER_ID_APPLICATION to a full name or SHA-1 fingerprint.\n' >&2
        exit 1
      fi
      signing_identity=$fingerprint
    fi
  fi
done <<< "$identities"
if [[ -z "$signing_identity" ]]; then
  printf 'The imported PKCS#12 has no valid Developer ID Application identity matching APPLE_TEAM_ID and the optional DEVELOPER_ID_APPLICATION selector.\n' >&2
  printf 'Check that it contains the Developer ID Application certificate and matching private key, and that its certificate chain is trusted.\n' >&2
  printf '%s\n' "$identities" >&2
  exit 1
fi
xcrun notarytool store-credentials shelp-notary --keychain "$keychain" \
  --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_SPECIFIC_PASSWORD"

for arch in arm64 amd64; do
  directory="dist/shelp-darwin-$arch"
  codesign --force --sign "$signing_identity" --keychain "$keychain" \
    --identifier com.github.chr33s.shelp \
    --timestamp --options runtime "$directory/shelp"
  codesign --verify --strict --verbose=2 "$directory/shelp"
  archive="$signing_temp/shelp-darwin-$arch.zip"
  ditto -c -k --keepParent "$directory" "$archive"
  result="$signing_temp/notarization-$arch.json"
  xcrun notarytool submit "$archive" --keychain "$keychain" \
    --keychain-profile shelp-notary --wait --timeout 20m --output-format json > "$result"
  status=$(plutil -extract status raw -o - "$result")
  if [[ "$status" != Accepted ]]; then
    printf 'Apple notarization for %s was not accepted: %s\n' "$arch" "$status" >&2
    cat "$result" >&2
    exit 1
  fi
  printf 'Apple accepted notarization for %s.\n' "$arch"
done
# Standalone executables and ZIP files cannot have tickets stapled. Gatekeeper
# retrieves the ticket for the signed binary online; tar packaging preserves it.
