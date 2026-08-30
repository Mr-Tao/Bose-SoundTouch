package handlers

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/proxy"
)

func sensitiveRecordingTestPaths() []string {
	sensitivePaths := []string{
		"/bmx/tunein/v1/token",
		"/core02/svc-bmx-adapter-orion/prod/orion/token",
		"/customer/account/account-id/password",
		"/oauth/device/device-id/music/musicprovider/15/token",
		"/oauth/device/device-id/music/musicprovider/20/token/cs1",
		"/oauth/device/device-id/music/musicprovider/15/token/cs3",
		"/oauth/account/account-id/music/musicprovider/15/token/cs",
		"/setup/settings",
		"/api/setup/settings",
	}

	for _, prefix := range []string{
		"/mgmt/spotify/",
		"/api/mgmt/spotify/",
		"/mgmt/amazon/",
		"/api/mgmt/amazon/",
	} {
		for _, action := range []string{"init", "confirm", "callback", "token"} {
			sensitivePaths = append(sensitivePaths, prefix+action)
		}
	}
	sensitivePaths = append(sensitivePaths,
		"/mgmt/spotify/start",
		"/api/mgmt/spotify/start",
	)

	streamingPaths := []string{
		"/streaming/account",
		"/streaming/account/login",
		"/streaming/account/account-id/full",
		"/streaming/account/account-id/sources",
		"/streaming/account/account-id/source",
		"/streaming/account/account-id/presets",
		"/streaming/account/account-id/presets/all",
		"/streaming/account/account-id/device/device-id/presets",
		"/streaming/account/account-id/device/device-id/presets/1",
		"/streaming/account/account-id/device/device-id/preset/1",
		"/streaming/account/account-id/device/device-id/recent",
		"/streaming/account/account-id/device/device-id/recents",
		"/streaming/device/device-id/streaming_token",
		"/accounts/account-id/full",
		"/accounts/account-id/sources",
		"/accounts/account-id/devices/device-id/presets",
		"/accounts/account-id/devices/device-id/presets/1",
		"/accounts/account-id/devices/device-id/preset/1",
		"/accounts/account-id/devices/device-id/recents",
	}
	for _, requestPath := range streamingPaths {
		sensitivePaths = append(sensitivePaths, requestPath, "/marge"+requestPath)
	}

	return sensitivePaths
}

func TestSensitiveRecordingPath(t *testing.T) {
	for _, requestPath := range sensitiveRecordingTestPaths() {
		requestPath := requestPath
		t.Run(requestPath, func(t *testing.T) {
			t.Parallel()
			if !isSensitiveRecordingPath(requestPath) {
				t.Errorf("expected %q to bypass recording", requestPath)
			}
		})
	}

	for _, requestPath := range []string{
		"/streaming/account/account-id/devices",
		"/marge/streaming/account/account-id/provider_settings",
		"/mgmt/accounts/account-id",
		"/api/mgmt/spotify/accounts",
		"/api/mgmt/spotify/prime",
		"/setup/logging-settings",
	} {
		requestPath := requestPath
		t.Run("non-sensitive "+requestPath, func(t *testing.T) {
			t.Parallel()
			if isSensitiveRecordingPath(requestPath) {
				t.Errorf("did not expect %q to bypass recording", requestPath)
			}
		})
	}
}

func TestRecordMiddlewareDoesNotPersistSensitiveRoutes(t *testing.T) {
	t.Setenv("RECORDER_ASYNC", "false")

	const (
		urlSentinel            = "recorder-url-surrogate-7c781e"
		requestSentinel        = "recorder-request-token-6ec2aa"
		responseHeaderSentinel = "recorder-response-header-c3491d"
		responseBodySentinel   = "recorder-response-body-533f1a"
	)

	tmpDir := t.TempDir()
	ds := datastore.NewDataStore(filepath.Join(tmpDir, "test.db"))
	server := NewServer(ds, nil, "http://localhost", false, false, true)
	server.SetRecorder(proxy.NewRecorder(tmpDir))

	handler := server.RecordMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/recording-control-") {
			_, _ = w.Write([]byte(`{"status":"recorded","path":"` + r.URL.Path + `"}`))
			return
		}
		w.Header().Set("Authorization", "Bearer "+responseHeaderSentinel)
		_, _ = w.Write([]byte(`{"token":"` + responseBodySentinel + `"}`))
	}))

	for _, requestPath := range sensitiveRecordingTestPaths() {
		req := httptest.NewRequest(http.MethodPost, requestPath+"?code="+urlSentinel, strings.NewReader(requestSentinel))
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	for _, requestPath := range []string{"/recording-control-one", "/recording-control-two"} {
		safeReq := httptest.NewRequest(http.MethodGet, requestPath, nil)
		handler.ServeHTTP(httptest.NewRecorder(), safeReq)
	}

	var recorded strings.Builder
	err := filepath.WalkDir(filepath.Join(tmpDir, "interactions"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		recorded.Write(content)
		return nil
	})
	if err != nil {
		t.Fatalf("read recorder output: %v", err)
	}

	output := recorded.String()
	for _, requestPath := range []string{"/recording-control-one", "/recording-control-two"} {
		if !strings.Contains(output, requestPath) {
			t.Errorf("control request %q was not recorded", requestPath)
		}
	}
	for _, sentinel := range []string{urlSentinel, requestSentinel, responseHeaderSentinel, responseBodySentinel} {
		if strings.Contains(output, sentinel) {
			t.Errorf("recorder output contains sensitive sentinel %q", sentinel)
		}
	}
}
