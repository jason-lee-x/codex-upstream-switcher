package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"unsafe"

	"gopkg.in/yaml.v3"
)

const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 1
	pluginID             = "codex-upstream-switcher"
	providerID           = "codex"
	defaultBaseURL       = "https://codex-relay.oaifree.com/backend-api/codex"
)

// version is replaced by the release workflow with -ldflags. Keeping a usable
// development value makes locally built plugins self-describing.
var version = "0.1.0"

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type pluginConfig struct {
	Enabled bool
	BaseURL string
}

type rawPluginConfig struct {
	Enabled       *bool  `yaml:"enabled"`
	BaseURL       string `yaml:"base-url"`
	BaseURLSnake  string `yaml:"base_url"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      metadata           `json:"metadata"`
	Capabilities  registrationCaps   `json:"capabilities"`
}

type metadata struct {
	Name            string       `json:"Name"`
	Version         string       `json:"Version"`
	Author          string       `json:"Author"`
	GitHubRepository string      `json:"GitHubRepository"`
	ConfigFields    []configField `json:"ConfigFields,omitempty"`
}

type configField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type registrationCaps struct {
	AuthProvider bool `json:"auth_provider"`
}

type authParseRequest struct {
	Provider string                 `json:"Provider"`
	Path     string                 `json:"Path"`
	FileName string                 `json:"FileName"`
	RawJSON  []byte                 `json:"RawJSON"`
	Host     hostConfigSummary      `json:"Host"`
}

type hostConfigSummary struct {
	AuthDir string `json:"AuthDir"`
}

type authParseResponse struct {
	Handled bool     `json:"Handled"`
	Auth    authData `json:"Auth,omitempty"`
}

type authData struct {
	Provider        string            `json:"Provider"`
	ID              string            `json:"ID,omitempty"`
	FileName        string            `json:"FileName,omitempty"`
	Label           string            `json:"Label,omitempty"`
	StorageJSON     []byte            `json:"StorageJSON,omitempty"`
	Metadata        map[string]any    `json:"Metadata,omitempty"`
	Attributes      map[string]string `json:"Attributes,omitempty"`
	Disabled        bool              `json:"Disabled,omitempty"`
	Prefix          string            `json:"Prefix,omitempty"`
	ProxyURL        string            `json:"ProxyURL,omitempty"`
}

var runtimeState = struct {
	sync.RWMutex
	config pluginConfig
}{
	config: pluginConfig{Enabled: true, BaseURL: defaultBaseURL},
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration()), nil
	case "auth.identifier":
		return okEnvelope(map[string]string{"identifier": providerID}), nil
	case "auth.parse":
		return parseAuth(request)
	case "auth.login.start", "auth.login.poll", "auth.refresh":
		// AuthProvider's current ABI has no Handled field for these operations.
		// Report them as unsupported instead of pretending to own OAuth login or
		// refresh. CPA's explicit Codex route and native Codex executor do not
		// call these methods for this plugin.
		return nil, fmt.Errorf("%s is intentionally unsupported; use CPA's native Codex lifecycle", method)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(request []byte) error {
	var lifecycle lifecycleRequest
	if len(request) > 0 {
		if errDecode := json.Unmarshal(request, &lifecycle); errDecode != nil {
			return fmt.Errorf("decode lifecycle request: %w", errDecode)
		}
	}

	cfg := pluginConfig{Enabled: true, BaseURL: defaultBaseURL}
	if len(lifecycle.ConfigYAML) > 0 {
		var decoded rawPluginConfig
		if errDecode := yaml.Unmarshal(lifecycle.ConfigYAML, &decoded); errDecode != nil {
			return fmt.Errorf("decode plugin config: %w", errDecode)
		}
		// Host always supplies enabled, but retaining the default makes the
		// plugin safe when invoked by an older host that omits it.
		if decoded.Enabled != nil {
			cfg.Enabled = *decoded.Enabled
		}
		decodedBaseURL := decoded.BaseURL
		if strings.TrimSpace(decodedBaseURL) == "" {
			decodedBaseURL = decoded.BaseURLSnake
		}
		if strings.TrimSpace(decodedBaseURL) != "" {
			cfg.BaseURL = decodedBaseURL
		}
	}
	if !cfg.Enabled {
		cfg.BaseURL = ""
	} else {
		validated, errValidate := validateBaseURL(cfg.BaseURL)
		if errValidate != nil {
			return errValidate
		}
		cfg.BaseURL = validated
	}

	runtimeState.Lock()
	runtimeState.config = cfg
	runtimeState.Unlock()
	return nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             pluginID,
			Version:          version,
			Author:           "jason-lee-x",
			GitHubRepository: "https://github.com/jason-lee-x/codex-upstream-switcher",
			ConfigFields: []configField{
				{
					Name:        "base-url",
					Type:        "string",
					Description: "Codex Responses upstream base URL. The default routes OAuth traffic through the configured relay.",
				},
			},
		},
		Capabilities: registrationCaps{AuthProvider: true},
	}
}

func parseAuth(request []byte) ([]byte, error) {
	var req authParseRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, fmt.Errorf("decode auth.parse request: %w", errDecode)
	}
	if !strings.EqualFold(strings.TrimSpace(req.Provider), providerID) {
		return okEnvelope(authParseResponse{Handled: false}), nil
	}

	var metadata map[string]any
	if errDecode := json.Unmarshal(req.RawJSON, &metadata); errDecode != nil || metadata == nil {
		return okEnvelope(authParseResponse{Handled: false}), nil
	}
	if !isOAuthCredential(metadata) {
		return okEnvelope(authParseResponse{Handled: false}), nil
	}

	cfg := currentConfig()
	if !cfg.Enabled || strings.TrimSpace(cfg.BaseURL) == "" {
		return okEnvelope(authParseResponse{Handled: false}), nil
	}

	attributes := map[string]string{"base_url": cfg.BaseURL}
	if planType := resolvePlanType(metadata); planType != "" {
		attributes["plan_type"] = planType
	}

	label := providerID
	if email := stringValue(metadata["email"]); email != "" {
		label = email
	}

	return okEnvelope(authParseResponse{
		Handled: true,
		Auth: authData{
			Provider:    providerID,
			FileName:    req.FileName,
			Label:       label,
			StorageJSON: append([]byte(nil), req.RawJSON...),
			Metadata:    metadata,
			Attributes:  attributes,
		},
	}), nil
}

func currentConfig() pluginConfig {
	runtimeState.RLock()
	cfg := runtimeState.config
	runtimeState.RUnlock()
	return cfg
}

func isOAuthCredential(metadata map[string]any) bool {
	for _, key := range []string{"access_token", "refresh_token", "id_token", "accessToken", "refreshToken", "idToken"} {
		if stringValue(metadata[key]) != "" {
			return true
		}
	}
	return false
}

func resolvePlanType(metadata map[string]any) string {
	if planType := stringValue(metadata["plan_type"]); planType != "" {
		return planType
	}
	idToken := stringValue(metadata["id_token"])
	if idToken == "" {
		idToken = stringValue(metadata["idToken"])
	}
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		// JWT payloads are occasionally padded by non-standard issuers.
		payload, errDecode = base64.URLEncoding.DecodeString(parts[1])
		if errDecode != nil {
			return ""
		}
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	authClaims, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return ""
	}
	return stringValue(authClaims["chatgpt_plan_type"])
}

func stringValue(value any) string {
	valueString, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(valueString)
}

func validateBaseURL(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return "", errors.New("base-url must not be empty when the plugin is enabled")
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil || parsed == nil || parsed.Host == "" {
		return "", fmt.Errorf("base-url must be an absolute URL: %q", raw)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("base-url scheme must be http or https: %q", raw)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("base-url must not contain userinfo, query, or fragment: %q", raw)
	}
	return value, nil
}

func okEnvelope(value any) []byte {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return errorEnvelope("encode_result", errMarshal.Error())
	}
	raw, errEnvelope := json.Marshal(envelope{OK: true, Result: result})
	if errEnvelope != nil {
		return errorEnvelope("encode_envelope", errEnvelope.Error())
	}
	return raw
}

func errorEnvelope(code, message string) []byte {
	raw, errMarshal := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"failed to encode plugin error"}}`)
	}
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
