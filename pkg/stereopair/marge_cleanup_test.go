package stereopair

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func margeTestGroup(id string) *models.Group {
	return &models.Group{
		ID:             id,
		MasterDeviceID: "LEFT-ID",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "LEFT-ID", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "RIGHT-ID", Role: "RIGHT", IPAddress: "192.0.2.11"},
		}},
	}
}

func TestEnsureMargeNoGroupGenerationsFailsClosedOnDiscoveredGeneration(t *testing.T) {
	getCalls := 0
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/device/LEFT-ID/group"):
			getCalls++
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<group id="STALE1"><masterDeviceId>LEFT-ID</masterDeviceId><name>Old Pair</name><roles><groupRole><deviceId>LEFT-ID</deviceId><role>LEFT</role></groupRole><groupRole><deviceId>RIGHT-ID</deviceId><role>RIGHT</role></groupRole></roles></group>`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/group/STALE1"):
			deleteCalls++
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := EnsureMargeNoGroupGenerations(server.Client(), []GenerationRef{{
		DeviceID: "LEFT-ID", AccountID: "ACCOUNT1", MargeURL: server.URL + "/marge",
	}})
	if err == nil || !strings.Contains(err.Error(), "STALE1") {
		t.Fatalf("error = %v, want stale generation rejection", err)
	}
	if getCalls != 1 || deleteCalls != 0 {
		t.Fatalf("requests GET=%d DELETE=%d, want 1/0", getCalls, deleteCalls)
	}
}

func TestEnsureMargeNoGroupGenerationsAcceptsEmptyGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<group/>`))
	}))
	defer server.Close()

	err := EnsureMargeNoGroupGenerations(server.Client(), []GenerationRef{{
		DeviceID: "LEFT-ID", AccountID: "ACCOUNT1", MargeURL: server.URL,
	}})
	if err != nil {
		t.Fatalf("EnsureMargeNoGroupGenerations: %v", err)
	}
}

func TestEnsureMargeNoGroupGenerationsRejectsUnrelatedGroup(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
		}
		_, _ = w.Write([]byte(`<group id="OTHER"><masterDeviceId>OTHER-LEFT</masterDeviceId><roles><groupRole><deviceId>OTHER-LEFT</deviceId><role>LEFT</role></groupRole><groupRole><deviceId>OTHER-RIGHT</deviceId><role>RIGHT</role></groupRole></roles></group>`))
	}))
	defer server.Close()

	err := EnsureMargeNoGroupGenerations(server.Client(), []GenerationRef{{
		DeviceID: "LEFT-ID", AccountID: "ACCOUNT1", MargeURL: server.URL,
	}})
	if err == nil || !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("error = %v, want unrelated-group rejection", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("unsafe DELETE calls = %d, want 0", deleteCalls)
	}
}

func TestEnsureMargeNoGroupGenerationsRejectsMissingEndpoint(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	err := EnsureMargeNoGroupGenerations(server.Client(), []GenerationRef{{
		DeviceID: "LEFT-ID", AccountID: "ACCOUNT1", MargeURL: server.URL,
	}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error = %v, want fail-closed HTTP 404", err)
	}
}

func TestDeleteMargeGroupGenerationDoesNotHideConflict(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
			http.Error(w, "active generation conflicts with tombstone", http.StatusConflict)
			return
		}

		_, _ = w.Write([]byte(`<group id="PAIR1"><masterDeviceId>LEFT-ID</masterDeviceId><roles><groupRole><deviceId>LEFT-ID</deviceId><role>LEFT</role><ipAddress>192.0.2.10</ipAddress></groupRole><groupRole><deviceId>RIGHT-ID</deviceId><role>RIGHT</role><ipAddress>192.0.2.11</ipAddress></groupRole></roles></group>`))
	}))
	defer server.Close()

	err := DeleteMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("error = %v, want propagated HTTP 409", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("DELETE calls = %d, want 1", deleteCalls)
	}
}

func TestDeleteMargeGroupGenerationVerifiesExactGenerationIsGone(t *testing.T) {
	deleteCalls := 0
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			getCalls++
			_, _ = w.Write([]byte(`<group id="PAIR1"><masterDeviceId>LEFT-ID</masterDeviceId><roles><groupRole><deviceId>LEFT-ID</deviceId><role>LEFT</role><ipAddress>192.0.2.10</ipAddress></groupRole><groupRole><deviceId>RIGHT-ID</deviceId><role>RIGHT</role><ipAddress>192.0.2.11</ipAddress></groupRole></roles></group>`))
		}
	}))
	defer server.Close()

	err := DeleteMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	})
	if err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("error = %v, want failed postcondition", err)
	}
	if deleteCalls != 1 || getCalls != 2 {
		t.Fatalf("requests DELETE=%d GET=%d, want 1/2", deleteCalls, getCalls)
	}
}

func TestDeleteMargeGroupGenerationAcceptsVerifiedAbsenceAfter404(t *testing.T) {
	deleteSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteSeen = true
			http.NotFound(w, r)
			return
		}

		if deleteSeen {
			_, _ = w.Write([]byte(`<group/>`))
			return
		}

		_, _ = w.Write([]byte(`<group id="PAIR1"><masterDeviceId>LEFT-ID</masterDeviceId><roles><groupRole><deviceId>LEFT-ID</deviceId><role>LEFT</role><ipAddress>192.0.2.10</ipAddress></groupRole><groupRole><deviceId>RIGHT-ID</deviceId><role>RIGHT</role><ipAddress>192.0.2.11</ipAddress></groupRole></roles></group>`))
	}))
	defer server.Close()

	err := DeleteMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	})
	if err != nil {
		t.Fatalf("DeleteMargeGroupGeneration: %v", err)
	}
}

func TestDeleteMargeGroupGenerationRejectsUnrelatedGenerationBeforeDelete(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
		}
		_, _ = w.Write([]byte(`<group id="OTHER"><masterDeviceId>LEFT-ID</masterDeviceId><roles><groupRole><deviceId>LEFT-ID</deviceId><role>LEFT</role></groupRole><groupRole><deviceId>RIGHT-ID</deviceId><role>RIGHT</role></groupRole></roles></group>`))
	}))
	defer server.Close()

	err := DeleteMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	})
	if err == nil || !strings.Contains(err.Error(), "unrelated generation") {
		t.Fatalf("error = %v, want unrelated-generation rejection", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("DELETE calls = %d, want 0", deleteCalls)
	}
}

func TestDeleteMargeGroupGenerationRejectsSubstitutedMemberBeforeDelete(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
		}
		_, _ = w.Write([]byte(`<group id="PAIR1"><masterDeviceId>LEFT-ID</masterDeviceId><roles><groupRole><deviceId>LEFT-ID</deviceId><role>LEFT</role><ipAddress>192.0.2.10</ipAddress></groupRole><groupRole><deviceId>REAL-RIGHT-ID</deviceId><role>RIGHT</role><ipAddress>192.0.2.11</ipAddress></groupRole></roles></group>`))
	}))
	defer server.Close()

	submitted := margeTestGroup("PAIR1")
	submitted.Roles.Roles[1].DeviceID = "SUBSTITUTE-RIGHT-ID"
	err := DeleteMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: submitted,
	})
	if err == nil || !strings.Contains(err.Error(), "topology") {
		t.Fatalf("error = %v, want topology rejection", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("DELETE calls = %d, want 0", deleteCalls)
	}
}

func TestDeleteMargeGroupGenerationTreatsVerifiedAbsenceAsIdempotent(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
		}
		_, _ = w.Write([]byte(`<group/>`))
	}))
	defer server.Close()

	err := DeleteMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	})
	if err != nil {
		t.Fatalf("DeleteMargeGroupGeneration: %v", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("DELETE calls = %d, want 0", deleteCalls)
	}
}

func TestRenameMargeGroupGenerationUpdatesAndVerifiesExactGeneration(t *testing.T) {
	current := margeTestGroup("PAIR1")
	current.Name = "Old name"
	postCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			data, _ := xml.Marshal(current)
			_, _ = w.Write(data)
		case http.MethodPost:
			postCalls++
			var update models.Group
			if err := xml.NewDecoder(r.Body).Decode(&update); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			current = &update
			data, _ := xml.Marshal(current)
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ref := GenerationRef{
		MargeURL: server.URL + "/marge", AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	}
	if err := RenameMargeGroupGeneration(server.Client(), ref, "New name"); err != nil {
		t.Fatalf("RenameMargeGroupGeneration: %v", err)
	}
	if current.Name != "New name" || postCalls != 1 {
		t.Fatalf("current name = %q, POST calls = %d; want New name, 1", current.Name, postCalls)
	}
	if err := RenameMargeGroupGeneration(server.Client(), ref, "New name"); err != nil {
		t.Fatalf("idempotent RenameMargeGroupGeneration: %v", err)
	}
	if postCalls != 1 {
		t.Fatalf("idempotent retry POST calls = %d, want 1", postCalls)
	}
}

func TestRenameMargeGroupGenerationAcceptsVerifiedStateAfterErrorResponse(t *testing.T) {
	current := margeTestGroup("PAIR1")
	current.Name = "Old name"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			data, _ := xml.Marshal(current)
			_, _ = w.Write(data)
		case http.MethodPost:
			var update models.Group
			if err := xml.NewDecoder(r.Body).Decode(&update); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			current = &update
			http.Error(w, "response lost after commit", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := RenameMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	}, "New name")
	if err != nil {
		t.Fatalf("verified state after error response: %v", err)
	}
	if current.Name != "New name" {
		t.Fatalf("current name = %q, want New name", current.Name)
	}
}

func TestRenameMargeGroupGenerationRejectsUnrelatedTopology(t *testing.T) {
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalls++
		}
		_, _ = w.Write([]byte(`<group id="PAIR1"><name>Old</name><masterDeviceId>LEFT-ID</masterDeviceId><roles><groupRole><deviceId>LEFT-ID</deviceId><role>LEFT</role><ipAddress>192.0.2.10</ipAddress></groupRole><groupRole><deviceId>OTHER-RIGHT</deviceId><role>RIGHT</role><ipAddress>192.0.2.11</ipAddress></groupRole></roles></group>`))
	}))
	defer server.Close()

	err := RenameMargeGroupGeneration(server.Client(), GenerationRef{
		MargeURL: server.URL, AccountID: "ACCOUNT1", GroupID: "PAIR1", DeviceID: "LEFT-ID",
		ExpectedGroup: margeTestGroup("PAIR1"),
	}, "New name")
	if err == nil || !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("error = %v, want unrelated-topology rejection", err)
	}
	if postCalls != 0 {
		t.Fatalf("POST calls = %d, want 0", postCalls)
	}
}

func TestMargeGroupGenerationURLRejectsDotSegments(t *testing.T) {
	for _, ref := range []GenerationRef{
		{MargeURL: "http://example.test", AccountID: "..", GroupID: "PAIR1"},
		{MargeURL: "http://example.test", AccountID: "ACCOUNT1", GroupID: "."},
	} {
		if endpoint, err := MargeGroupGenerationURL(ref); err == nil {
			t.Fatalf("MargeGroupGenerationURL(%+v) = %q, want error", ref, endpoint)
		}
	}
}

func TestSameMargeBackendNormalizesDefaultPorts(t *testing.T) {
	for _, test := range []struct {
		name  string
		left  string
		right string
		same  bool
	}{
		{name: "http default", left: "http://aftertouch.test", right: "http://aftertouch.test:80/streaming", same: true},
		{name: "https default", left: "https://aftertouch.test/streaming", right: "https://aftertouch.test:443", same: true},
		{name: "non-default port", left: "https://aftertouch.test", right: "https://aftertouch.test:18443", same: false},
		{name: "different prefix", left: "http://aftertouch.test/marge", right: "http://aftertouch.test", same: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SameMargeBackend(test.left, test.right); got != test.same {
				t.Fatalf("SameMargeBackend(%q, %q) = %v, want %v", test.left, test.right, got, test.same)
			}
		})
	}
}
