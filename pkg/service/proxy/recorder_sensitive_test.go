package proxy

import (
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecorderBoundaryNeverPersistsCredentialInteractions(t *testing.T) {
	t.Setenv("RECORDER_ASYNC", "false")

	const (
		querySentinel          = "boundary-query-secret-91d27a"
		requestHeaderSentinel  = "boundary-request-header-6f12c4"
		requestBodySentinel    = "boundary-request-body-d3888e"
		responseHeaderSentinel = "boundary-response-header-e4a006"
		responseBodySentinel   = "boundary-response-body-258b31"
	)

	baseDir := t.TempDir()
	recorder := NewRecorder(baseDir)
	paths := []string{
		"/bmx/tunein/v1/token",
		"/core02/svc-bmx-adapter-orion/prod/orion/token",
		"/customer/account/marge-a/password",
		"/oauth/device/device-a/music/musicprovider/15/token/cs3",
		"/streaming/device/device-a/streaming_token",
		"/api/mgmt/spotify/confirm",
		"/setup/settings",
	}

	for _, requestPath := range paths {
		req := &http.Request{
			Method: http.MethodPost,
			URL:    &url.URL{Path: requestPath, RawQuery: "code=" + querySentinel},
			Header: http.Header{"Authorization": {"Bearer " + requestHeaderSentinel}},
			Body:   io.NopCloser(strings.NewReader(requestBodySentinel)),
		}
		res := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Credentials": {responseHeaderSentinel}, "Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(responseBodySentinel)),
		}
		if err := recorder.Record("self", req, res); err != nil {
			t.Fatalf("Record(%q): %v", requestPath, err)
		}
	}

	var recorded strings.Builder
	interactions := filepath.Join(baseDir, "interactions")
	err := filepath.WalkDir(interactions, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		recorded.Write(data)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk recorder output: %v", err)
	}

	for _, sentinel := range []string{querySentinel, requestHeaderSentinel, requestBodySentinel, responseHeaderSentinel, responseBodySentinel} {
		if strings.Contains(recorded.String(), sentinel) {
			t.Fatalf("recorder boundary persisted sentinel %q", sentinel)
		}
	}
}
