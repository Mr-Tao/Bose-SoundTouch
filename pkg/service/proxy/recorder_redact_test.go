package proxy

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecorder_Redaction(t *testing.T) {
	// Disable async for testing
	t.Setenv("RECORDER_ASYNC", "false")

	tmpDir, err := os.MkdirTemp("", "recorder-redact-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRecorder(tmpDir)
	r.Redact = true // Enable redaction

	req := httptest.NewRequest("GET", "http://example.com/api/test", nil)
	req.Header.Set("Authorization", "Bearer sensitive-token")
	req.Header.Set("X-Custom", "safe-value")

	w := httptest.NewRecorder()
	w.Header().Set("X-Bose-Token", "sensitive-bose-token")
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.WriteString("hello")
	res := w.Result()
	res.Request = req

	err = r.Record("test", req, res)
	if err != nil {
		t.Fatalf("Failed to record: %v", err)
	}

	// Find the recorded file
	var recordedFile string
	err = filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".http") {
			recordedFile = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Error walking temp dir: %v", err)
	}

	if recordedFile == "" {
		t.Fatal("No recorded .http file found")
	}

	content, err := os.ReadFile(recordedFile)
	if err != nil {
		t.Fatalf("Failed to read recorded file: %v", err)
	}

	contentStr := string(content)

	// Check for redaction in request headers
	if strings.Contains(contentStr, "sensitive-token") {
		t.Errorf("Recorded file contains sensitive Authorization header value:\n%s", contentStr)
	}
	if !strings.Contains(contentStr, "Authorization: [REDACTED]") {
		t.Errorf("Recorded file does not contain redacted Authorization header:\n%s", contentStr)
	}

	// Check for redaction in response headers
	if strings.Contains(contentStr, "sensitive-bose-token") {
		t.Errorf("Recorded file contains sensitive X-Bose-Token header value:\n%s", contentStr)
	}
	if !strings.Contains(contentStr, "X-Bose-Token: [REDACTED]") {
		t.Errorf("Recorded file does not contain redacted X-Bose-Token header:\n%s", contentStr)
	}

	// Check that non-sensitive headers are NOT redacted
	if !strings.Contains(contentStr, "X-Custom: safe-value") {
		t.Errorf("Recorded file missing non-sensitive header or it was incorrectly redacted:\n%s", contentStr)
	}
}

func TestRecorder_NoOptionalRedactionStillProtectsCredentialHeaders(t *testing.T) {
	// Disable async for testing
	t.Setenv("RECORDER_ASYNC", "false")

	tmpDir, err := os.MkdirTemp("", "recorder-no-redact-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRecorder(tmpDir)
	r.Redact = false // Disable redaction

	req := httptest.NewRequest("GET", "http://example.com/api/test", nil)
	requestSecrets := map[string]string{
		"Authorization":       "Bearer request-authorization-secret",
		"Proxy-Authorization": "Basic request-proxy-secret",
		"Cookie":              "session=request-cookie-secret",
		"X-Application-Key":   "request-app-key-secret",
		"X-Provider-Token":    "request-token-secret",
		"X-Client-Secret":     "request-client-secret",
		"Credentials":         "request-credentials-secret",
	}
	for header, value := range requestSecrets {
		req.Header.Set(header, value)
	}
	req.Header.Set("X-Trace-ID", "useful-request-header")

	w := httptest.NewRecorder()
	responseSecrets := map[string]string{
		"Set-Cookie":            "session=response-cookie-secret",
		"X-Bose-Token":          "response-bose-token-secret",
		"X-Api_Key":             "response-api-key-secret",
		"X-Upstream-Secret":     "response-provider-secret",
		"Application-Token":     "response-app-token-secret",
		"X-Provider-Credential": "response-credential-secret",
	}
	for header, value := range responseSecrets {
		w.Header().Set(header, value)
	}
	w.Header().Set("X-Request-ID", "useful-response-header")
	_, _ = w.WriteString("hello")
	res := w.Result()
	res.Request = req

	err = r.Record("test", req, res)
	if err != nil {
		t.Fatalf("Failed to record: %v", err)
	}

	// Find the recorded file
	var recordedFile string
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() && strings.HasSuffix(path, ".http") {
			recordedFile = path
		}
		return nil
	})

	content, _ := os.ReadFile(recordedFile)
	contentStr := string(content)

	for header, secret := range requestSecrets {
		if strings.Contains(contentStr, secret) {
			t.Errorf("recorded request persisted %s with Redact=false:\n%s", header, contentStr)
		}
		if !strings.Contains(strings.ToLower(contentStr), strings.ToLower(header)+": [redacted]") {
			t.Errorf("recorded request omitted redacted %s header:\n%s", header, contentStr)
		}
	}
	for header, secret := range responseSecrets {
		if strings.Contains(contentStr, secret) {
			t.Errorf("recorded response persisted %s with Redact=false:\n%s", header, contentStr)
		}
		if !strings.Contains(strings.ToLower(contentStr), strings.ToLower(header)+": [redacted]") {
			t.Errorf("recorded response omitted redacted %s header:\n%s", header, contentStr)
		}
	}
	for _, value := range []string{"useful-request-header", "useful-response-header"} {
		if !strings.Contains(contentStr, value) {
			t.Errorf("recording lost non-sensitive header %q:\n%s", value, contentStr)
		}
	}
}
