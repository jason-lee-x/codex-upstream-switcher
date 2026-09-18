package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigureDefaultsAndNormalizesBaseURL(t *testing.T) {
	if errConfigure := configure([]byte(`{"config_yaml":"ZW5hYmxlZDogdHJ1ZQo="}`)); errConfigure != nil {
		t.Fatalf("configure() default: %v", errConfigure)
	}
	if got := currentConfig(); !got.Enabled || got.BaseURL != defaultBaseURL {
		t.Fatalf("default config = %#v, want enabled with %q", got, defaultBaseURL)
	}

	configYAML := []byte("enabled: true\nbase-url: https://relay.example.test/backend-api/codex///\n")
	request := lifecycleRequest{ConfigYAML: configYAML}
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errConfigure := configure(rawRequest); errConfigure != nil {
		t.Fatalf("configure() custom: %v", errConfigure)
	}
	if got := currentConfig().BaseURL; got != "https://relay.example.test/backend-api/codex" {
		t.Fatalf("normalized base URL = %q", got)
	}
}

func TestConfigureRejectsUnsafeBaseURL(t *testing.T) {
	for _, raw := range []string{
		"base-url: file:///tmp/codex\n",
		"base-url: https://relay.example.test/path?token=secret\n",
		"base-url: https://user:pass@relay.example.test/path\n",
	} {
		request := lifecycleRequest{ConfigYAML: []byte(raw)}
		encoded, errMarshal := json.Marshal(request)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if errConfigure := configure(encoded); errConfigure == nil {
			t.Errorf("configure(%q) succeeded, want validation error", raw)
		}
	}
}

func TestParseAuthInjectsBaseURLAndPreservesOAuthMaterial(t *testing.T) {
	const idToken = "eyJhbGciOiJub25lIn0.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOns" +
		"iY2hhdGdwdF9wbGFuX3R5cGUiOiJQbHVzIn19.signature"
	raw := []byte(`{"type":"codex","email":"jason@example.com","access_token":"access-1","refresh_token":"refresh-1","id_token":"` + idToken + `","account_id":"acct-1"}`)
	configureRequest := lifecycleRequest{ConfigYAML: []byte("enabled: true\nbase-url: https://relay.example.test/backend-api/codex\n")}
	configureRaw, errMarshal := json.Marshal(configureRequest)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errConfigure := configure(configureRaw); errConfigure != nil {
		t.Fatal(errConfigure)
	}

	parseRequest, errMarshal := json.Marshal(authParseRequest{
		Provider: "codex",
		Path:     "/var/lib/cpa/auth/codex-jason.json",
		FileName: "codex-jason.json",
		RawJSON:  raw,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	responseRaw, errParse := parseAuth(parseRequest)
	if errParse != nil {
		t.Fatal(errParse)
	}
	response := decodeAuthResponse(t, responseRaw)
	if !response.Handled {
		t.Fatal("auth.parse returned Handled=false")
	}
	if response.Auth.Provider != providerID {
		t.Fatalf("provider = %q", response.Auth.Provider)
	}
	if response.Auth.FileName != "codex-jason.json" || response.Auth.Label != "jason@example.com" {
		t.Fatalf("file/label = %q/%q", response.Auth.FileName, response.Auth.Label)
	}
	if string(response.Auth.StorageJSON) != string(raw) {
		t.Fatalf("StorageJSON changed: %s", response.Auth.StorageJSON)
	}
	if got := response.Auth.Attributes["base_url"]; got != "https://relay.example.test/backend-api/codex" {
		t.Fatalf("base_url = %q", got)
	}
	if got := response.Auth.Attributes["plan_type"]; got != "Plus" {
		t.Fatalf("plan_type = %q", got)
	}
	if got := response.Auth.Metadata["refresh_token"]; got != "refresh-1" {
		t.Fatalf("refresh_token = %#v", got)
	}
}

func TestParseAuthLeavesNonOAuthAndNonCodexCredentialsToCPA(t *testing.T) {
	if response := parseAuthForTest(t, "claude", []byte(`{"type":"claude","api_key":"key"}`)); response.Handled {
		t.Fatal("non-Codex credential was handled")
	}
	if response := parseAuthForTest(t, "codex", []byte(`{"type":"codex","api_key":"key"}`)); response.Handled {
		t.Fatal("non-OAuth Codex credential was handled")
	}
}

func TestParseAuthCanBeDisabled(t *testing.T) {
	request := lifecycleRequest{ConfigYAML: []byte("enabled: false\nbase-url: https://relay.example.test/codex\n")}
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errConfigure := configure(rawRequest); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	response := parseAuthForTest(t, "codex", []byte(`{"type":"codex","access_token":"token"}`))
	if response.Handled {
		t.Fatal("disabled plugin handled a credential")
	}
}

func parseAuthForTest(t *testing.T, provider string, raw []byte) authParseResponse {
	t.Helper()
	request, errMarshal := json.Marshal(authParseRequest{Provider: provider, FileName: "credential.json", RawJSON: raw})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	responseRaw, errParse := parseAuth(request)
	if errParse != nil {
		t.Fatal(errParse)
	}
	return decodeAuthResponse(t, responseRaw)
}

func decodeAuthResponse(t *testing.T, raw []byte) authParseResponse {
	t.Helper()
	var envelopeValue struct {
		OK     bool             `json:"ok"`
		Result json.RawMessage  `json:"result"`
	}
	if errDecode := json.Unmarshal(raw, &envelopeValue); errDecode != nil {
		t.Fatalf("decode envelope: %v (%s)", errDecode, raw)
	}
	if !envelopeValue.OK {
		t.Fatalf("unexpected error envelope: %s", raw)
	}
	var response authParseResponse
	if errDecode := json.Unmarshal(envelopeValue.Result, &response); errDecode != nil {
		t.Fatalf("decode auth response: %v", errDecode)
	}
	return response
}

func TestResolvePlanTypeAcceptsPaddedJWTPayload(t *testing.T) {
	claims := `{"https://api.openai.com/auth":{"chatgpt_plan_type":"team"}}`
	encoded := base64.URLEncoding.EncodeToString([]byte(claims))
	metadata := map[string]any{"id_token": "header." + encoded + ".signature"}
	if got := resolvePlanType(metadata); got != "team" {
		t.Fatalf("resolvePlanType() = %q", got)
	}
	if strings.Contains(encoded, "-") {
		// This assertion only documents that URL-safe payloads are accepted;
		// the decoder itself is covered by the value assertion above.
		t.Logf("URL-safe payload: %s", encoded)
	}
}
