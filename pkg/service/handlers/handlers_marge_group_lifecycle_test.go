package handlers

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/constants"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/go-chi/chi/v5"
)

func margeLifecycleRouter(ds *datastore.DataStore) http.Handler {
	server := NewServer(ds, nil, "http://localhost:8001", false, false, false)
	router := chi.NewRouter()
	router.Use(clientIPMiddleware(false, nil, nil))
	router.Get("/streaming/account/{account}/device/{device}/group", server.HandleMargeDeviceGroup)
	router.Post("/streaming/account/{account}/group/", server.HandleMargeAddGroup)
	router.Delete("/streaming/account/{account}/group/", server.HandleMargeDeleteAccountGroups)
	router.Delete("/streaming/account/{account}/group/{groupId}", server.HandleMargeDeleteGroup)

	return router
}

func margeLifecycleGroupXML(master, left, right, name string) string {
	return fmt.Sprintf(`<group><name>%s</name><masterDeviceId>%s</masterDeviceId><roles>`+
		`<groupRole><deviceId>%s</deviceId><role>LEFT</role></groupRole>`+
		`<groupRole><deviceId>%s</deviceId><role>RIGHT</role></groupRole>`+
		`</roles></group>`, name, master, left, right)
}

func margeLifecycleRequest(t *testing.T, handler http.Handler, method, path, remoteAddr, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	request.Header.Set("Content-Type", "application/vnd.bose.streaming-v1.2+xml")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

func margeLifecycleGroup(master, left, right, name string) models.Group {
	return models.Group{
		Name:           name,
		MasterDeviceID: master,
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: left, Role: "LEFT"},
			{DeviceID: right, Role: "RIGHT"},
		}},
	}
}

func countMargeLifecycleGroupFiles(t *testing.T, ds *datastore.DataStore, account string) int {
	t.Helper()

	entries, err := os.ReadDir(ds.AccountDevicesDir(account))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}

		t.Fatalf("read account devices directory: %v", err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "Group_") && strings.HasSuffix(entry.Name(), ".xml") {
			count++
		}
	}

	return count
}

func TestMargeAddGroupRetryReusesStoredGroup(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	handler := margeLifecycleRouter(ds)
	const (
		account = "ACCOUNT1"
		path    = "/streaming/account/" + account + "/group/"
	)

	first := margeLifecycleRequest(t, handler, http.MethodPost, path, "192.0.2.10:1234",
		margeLifecycleGroupXML("MASTER", "MASTER", "SLAVE", "Original name"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201; body=%s", first.Code, first.Body.String())
	}

	var firstGroup models.Group
	if err := xml.Unmarshal(first.Body.Bytes(), &firstGroup); err != nil {
		t.Fatalf("decode first response: %v; body=%s", err, first.Body.String())
	}
	firstLocation := first.Header().Get("Location")
	if firstGroup.ID == "" || !strings.HasSuffix(firstLocation, "/group/"+firstGroup.ID) {
		t.Fatalf("first response ID=%q Location=%q", firstGroup.ID, firstLocation)
	}

	retry := margeLifecycleRequest(t, handler, http.MethodPost, path, "192.0.2.10:1234",
		margeLifecycleGroupXML("MASTER", "MASTER", "SLAVE", "Retry name"))
	if retry.Code != http.StatusCreated {
		t.Fatalf("retry POST status = %d, want 201; body=%s", retry.Code, retry.Body.String())
	}

	var retryGroup models.Group
	if err := xml.Unmarshal(retry.Body.Bytes(), &retryGroup); err != nil {
		t.Fatalf("decode retry response: %v; body=%s", err, retry.Body.String())
	}
	if retryGroup.ID != firstGroup.ID || retryGroup.Name != firstGroup.Name {
		t.Fatalf("retry group = %#v, want stored group %#v", retryGroup, firstGroup)
	}
	if got := retry.Header().Get("Location"); got != firstLocation {
		t.Fatalf("retry Location = %q, want %q", got, firstLocation)
	}
	if got := retry.Header().Get("Content-Type"); got != "application/vnd.bose.streaming-v1.2+xml" {
		t.Fatalf("retry Content-Type = %q", got)
	}
	if got := countMargeLifecycleGroupFiles(t, ds, account); got != 1 {
		t.Fatalf("stored group files = %d, want 1", got)
	}
}

func TestMargeAddGroupMembershipConflictReturns409(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	handler := margeLifecycleRouter(ds)
	const (
		account = "ACCOUNT1"
		path    = "/streaming/account/" + account + "/group/"
	)

	first := margeLifecycleRequest(t, handler, http.MethodPost, path, "192.0.2.10:1234",
		margeLifecycleGroupXML("MASTER1", "MASTER1", "SHARED", "First pair"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201; body=%s", first.Code, first.Body.String())
	}

	conflict := margeLifecycleRequest(t, handler, http.MethodPost, path, "192.0.2.20:1234",
		margeLifecycleGroupXML("MASTER2", "MASTER2", "SHARED", "Conflicting pair"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting POST status = %d, want 409; body=%s", conflict.Code, conflict.Body.String())
	}
	if got := countMargeLifecycleGroupFiles(t, ds, account); got != 1 {
		t.Fatalf("stored group files = %d after conflict, want 1", got)
	}
}

func TestMargeDeviceGroupReturnsEmptyGroupOnlyWhenNotFound(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	response := margeLifecycleRequest(t, margeLifecycleRouter(ds), http.MethodGet,
		"/streaming/account/ACCOUNT1/device/MASTER/group", "192.0.2.10:1234", "")
	if response.Code != http.StatusOK {
		t.Fatalf("missing group GET status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if got := response.Body.String(); got != constants.XMLHeader+`<group/>` {
		t.Fatalf("missing group GET body = %q, want empty group", got)
	}
}

func TestMargeDeviceGroupReturns500ForMalformedOrUnreadableData(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "malformed XML",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := os.WriteFile(path, []byte("<group>"), 0600); err != nil {
					t.Fatalf("write malformed group: %v", err)
				}
			},
		},
		{
			name: "unreadable group",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := os.Symlink("missing-group-target", path); err != nil {
					t.Fatalf("create unreadable group symlink: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ds := datastore.NewDataStore(t.TempDir())
			if err := os.MkdirAll(ds.AccountDevicesDir("ACCOUNT1"), 0755); err != nil {
				t.Fatalf("create account directory: %v", err)
			}
			test.setup(t, filepath.Join(ds.AccountDevicesDir("ACCOUNT1"), "Group_1234567.xml"))

			response := margeLifecycleRequest(t, margeLifecycleRouter(ds), http.MethodGet,
				"/streaming/account/ACCOUNT1/device/MASTER/group", "192.0.2.10:1234", "")
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("invalid group GET status = %d, want 500; body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "<group/>") {
				t.Fatalf("invalid group GET returned empty-group success: %s", response.Body.String())
			}
		})
	}
}

func TestMargeDeleteAccountGroupsDeletesAllGroupsForAccount(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	handler := margeLifecycleRouter(ds)
	const account = "ACCOUNT1"
	const otherAccount = "ACCOUNT2"

	group := margeLifecycleGroup("MASTER", "MASTER", "SLAVE", "Current pair")
	if _, err := ds.AddGroup(account, &group); err != nil {
		t.Fatalf("add group: %v", err)
	}

	otherGroup := margeLifecycleGroup("OTHER-MASTER", "OTHER-MASTER", "OTHER-SLAVE", "Other account pair")
	if _, err := ds.AddGroup(otherAccount, &otherGroup); err != nil {
		t.Fatalf("add group in other account: %v", err)
	}

	response := margeLifecycleRequest(t, handler, http.MethodDelete,
		"/streaming/account/"+account+"/group/", "192.0.2.10:1234", "")
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200; body=%s", response.Code, response.Body.String())
	}

	if _, err := ds.GetGroupForDevice(account, "MASTER"); !errors.Is(err, datastore.ErrGroupNotFound) {
		t.Fatalf("generation-less teardown did not delete stored group: err=%v", err)
	}

	if current, err := ds.GetGroupForDevice(otherAccount, "OTHER-MASTER"); err != nil || current.ID != otherGroup.ID {
		t.Fatalf("teardown affected a different account's group: group=%#v err=%v", current, err)
	}
}

func TestMargeDeleteGroupDoesNotHideAmbiguousActiveGeneration(t *testing.T) {
	baseDir := t.TempDir()
	ds := datastore.NewDataStore(baseDir)
	handler := margeLifecycleRouter(ds)
	const account = "ACCOUNT1"

	group := margeLifecycleGroup("MASTER", "MASTER", "SLAVE", "Current pair")
	if _, err := ds.AddGroup(account, &group); err != nil {
		t.Fatalf("add group: %v", err)
	}

	retiredPath := filepath.Join(ds.AccountDevicesDir(account), "Group_"+group.ID+".retired")
	if err := os.WriteFile(retiredPath, []byte("retired\n"), 0600); err != nil {
		t.Fatalf("create conflicting tombstone: %v", err)
	}

	response := margeLifecycleRequest(t, handler, http.MethodDelete,
		"/streaming/account/"+account+"/group/"+group.ID, "192.0.2.10:1234", "")
	if response.Code != http.StatusConflict {
		t.Fatalf("ambiguous DELETE status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
	if current, err := ds.GetGroupForDevice(account, "MASTER"); err != nil || current.ID != group.ID {
		t.Fatalf("ambiguous DELETE changed active generation: group=%#v err=%v", current, err)
	}
}

func TestMargeDeleteMissingGroupReturns404(t *testing.T) {
	ds := datastore.NewDataStore(t.TempDir())
	response := margeLifecycleRequest(t, margeLifecycleRouter(ds), http.MethodDelete,
		"/streaming/account/ACCOUNT1/group/MISSING", "192.0.2.10:1234", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing DELETE status = %d, want 404; body=%s", response.Code, response.Body.String())
	}
}
