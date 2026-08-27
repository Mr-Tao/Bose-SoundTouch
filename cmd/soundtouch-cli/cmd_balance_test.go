package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/urfave/cli/v2"
)

func TestSetBalanceUsesAdvertisedRangeAndReportsReadback(t *testing.T) {
	server, posted := balanceCommandServer(t, 0, 10, -12, 12)
	defer server.Close()

	ctx := balanceCommandContext(t, server.URL, 10, 0)
	output := captureStdout(t, func() {
		if err := setBalance(ctx); err != nil {
			t.Fatalf("setBalance: %v", err)
		}
	})

	if *posted != 10 {
		t.Fatalf("posted balance = %d, want 10", *posted)
	}
	if !strings.Contains(output, "device target 10, actual 10") {
		t.Fatalf("output does not report verified readback:\n%s", output)
	}
}

func TestBalanceRightUsesAdvertisedRangeAndReportsReadback(t *testing.T) {
	server, posted := balanceCommandServer(t, 8, 9, -12, 9)
	defer server.Close()

	ctx := balanceCommandContext(t, server.URL, 0, 4)
	output := captureStdout(t, func() {
		if err := balanceRight(ctx); err != nil {
			t.Fatalf("balanceRight: %v", err)
		}
	})

	if *posted != 9 {
		t.Fatalf("posted balance = %d, want advertised maximum 9", *posted)
	}
	if !strings.Contains(output, "device target 9, actual 9") {
		t.Fatalf("output does not report verified readback:\n%s", output)
	}
}

func TestSetBalanceRejectsLevelOutsideAdvertisedRange(t *testing.T) {
	server, posted := balanceCommandServer(t, 0, 0, -7, 9)
	defer server.Close()

	ctx := balanceCommandContext(t, server.URL, 10, 0)
	err := setBalance(ctx)
	if err == nil || !strings.Contains(err.Error(), "must be between -7 and 9") {
		t.Fatalf("setBalance error = %v, want advertised-range rejection", err)
	}
	if *posted != noBalancePost {
		t.Fatalf("posted balance = %d, want no POST", *posted)
	}
}

const noBalancePost = 1 << 30

func balanceCommandServer(t *testing.T, initial, final, minLevel, maxLevel int) (*httptest.Server, *int) {
	t.Helper()
	posted := noBalancePost
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/balance":
			level := initial
			if getCalls > 0 {
				level = final
			}
			getCalls++
			_, _ = fmt.Fprintf(w, `<balance deviceID="LEFT"><balanceAvailable>true</balanceAvailable><balanceMin>%d</balanceMin><balanceMax>%d</balanceMax><balanceDefault>0</balanceDefault><targetBalance>%d</targetBalance><actualBalance>%d</actualBalance></balance>`,
				minLevel, maxLevel, level, level)
		case r.Method == http.MethodPost && r.URL.Path == "/balance":
			var request models.BalanceRequest
			if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode balance request: %v", err)
				return
			}
			posted = request.Level
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))

	return server, &posted
}

func balanceCommandContext(t *testing.T, serverURL string, level, amount int) *cli.Context {
	t.Helper()
	host, port := testServerHostPort(t, serverURL)
	flags := flag.NewFlagSet("balance-test", flag.ContinueOnError)
	flags.String("host", host, "")
	flags.Int("port", port, "")
	flags.Duration("timeout", time.Second, "")
	flags.Int("level", level, "")
	flags.Int("amount", amount, "")

	return cli.NewContext(cli.NewApp(), flags, nil)
}
