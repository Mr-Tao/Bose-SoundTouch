package webtypes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestSourceCacheStatusAtTTLBoundary(t *testing.T) {
	readAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	sources := &models.Sources{SourceItem: []models.SourceItem{{Source: "AUX"}}}
	status := &DeviceStatus{
		Sources:       sources,
		SourcesReadAt: readAt,
	}

	fresh := sourceCacheStatusAt(status, readAt.Add(sourceCacheTTL-time.Nanosecond), sourceCacheTTL)
	if fresh.SourcesStale {
		t.Fatal("source cache became stale before its TTL elapsed")
	}

	stale := sourceCacheStatusAt(status, readAt.Add(sourceCacheTTL), sourceCacheTTL)
	if !stale.SourcesStale {
		t.Fatal("source cache was not stale at its TTL boundary")
	}
	if stale.Sources != sources {
		t.Fatal("stale projection did not retain the last successful source list")
	}

	refreshed := *stale
	refreshed.SourcesReadAt = readAt.Add(sourceCacheTTL + time.Second)
	refreshed.SourcesStale = false
	got := sourceCacheStatusAt(&refreshed, refreshed.SourcesReadAt, sourceCacheTTL)
	if got.SourcesStale {
		t.Fatal("successful source readback did not clear staleness immediately")
	}
}

func TestSourceCacheWithoutSuccessfulReadIsNotStale(t *testing.T) {
	status := &DeviceStatus{Sources: &models.Sources{}}
	got := sourceCacheStatusAt(status, time.Now().Add(time.Hour), time.Nanosecond)
	if got.SourcesStale {
		t.Fatal("source cache without a recorded successful read was marked stale")
	}
}

func TestSourceCacheFailureWithoutInventoryIsExplicit(t *testing.T) {
	status := &DeviceStatus{}
	if !status.MergeSourcesFailure(1) {
		t.Fatal("initial source failure was not accepted")
	}
	if status.Sources != nil || !status.SourcesStale {
		t.Fatalf("initial source failure was not represented without inventory: %+v", status)
	}

	projected := sourceCacheStatusAt(status, time.Now().Add(time.Hour), time.Nanosecond)
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("marshal initial source failure: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"sourcesStale":true`) {
		t.Fatalf("initial source failure omitted stale state: %s", got)
	}
}

func TestSourceCacheFailureRemainsStaleUntilNewerSuccess(t *testing.T) {
	readAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	oldSources := &models.Sources{SourceItem: []models.SourceItem{{Source: "AUX"}}}
	status := &DeviceStatus{}
	if !status.MergeSources(oldSources, readAt, 1) {
		t.Fatal("initial source success was not accepted")
	}
	if !status.MergeSourcesFailure(3) {
		t.Fatal("newer source failure was not accepted")
	}

	projected := sourceCacheStatusAt(status, readAt.Add(time.Second), sourceCacheTTL)
	if !projected.SourcesStale || projected.Sources != oldSources {
		t.Fatalf("failure did not keep retained inventory stale before TTL: %+v", projected)
	}

	olderSources := &models.Sources{SourceItem: []models.SourceItem{{Source: "PRODUCT"}}}
	if status.MergeSources(olderSources, readAt.Add(2*time.Second), 2) {
		t.Fatal("older success was accepted after newer failure")
	}
	if !status.SourcesStale || status.Sources != oldSources {
		t.Fatalf("older success cleared newer failure: %+v", status)
	}

	newerSources := &models.Sources{SourceItem: []models.SourceItem{{Source: "BLUETOOTH"}}}
	newerRead := readAt.Add(3 * time.Second)
	if !status.MergeSources(newerSources, newerRead, 4) {
		t.Fatal("newer source success was not accepted")
	}
	if status.SourcesStale || status.Sources != newerSources || !status.SourcesReadAt.Equal(newerRead) {
		t.Fatalf("newer source success did not restore actionability: %+v", status)
	}
}
