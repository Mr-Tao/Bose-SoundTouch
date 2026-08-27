package client

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestClient_GetBalance(t *testing.T) {
	tests := []struct {
		name              string
		serverResponse    string
		wantError         bool
		wantTargetBalance int
		wantActualBalance int
		wantDeviceID      string
	}{
		{
			name: "Valid balance response",
			serverResponse: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="ABCD1234EFGH">
  <targetbalance>15</targetbalance>
  <actualbalance>15</actualbalance>
</balance>`,
			wantError:         false,
			wantTargetBalance: 15,
			wantActualBalance: 15,
			wantDeviceID:      "ABCD1234EFGH",
		},
		{
			name: "Negative balance response",
			serverResponse: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="ABCD1234EFGH">
  <targetbalance>-25</targetbalance>
  <actualbalance>-25</actualbalance>
</balance>`,
			wantError:         false,
			wantTargetBalance: -25,
			wantActualBalance: -25,
			wantDeviceID:      "ABCD1234EFGH",
		},
		{
			name: "Zero balance response",
			serverResponse: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="ABCD1234EFGH">
  <targetbalance>0</targetbalance>
  <actualbalance>0</actualbalance>
</balance>`,
			wantError:         false,
			wantTargetBalance: 0,
			wantActualBalance: 0,
			wantDeviceID:      "ABCD1234EFGH",
		},
		{
			name: "Balance adjustment in progress",
			serverResponse: `<?xml version="1.0" encoding="UTF-8" ?>
<balance deviceID="ABCD1234EFGH">
  <targetbalance>30</targetbalance>
  <actualbalance>20</actualbalance>
</balance>`,
			wantError:         false,
			wantTargetBalance: 30,
			wantActualBalance: 20,
			wantDeviceID:      "ABCD1234EFGH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("Expected GET request, got %s", r.Method)
					return
				}

				if r.URL.Path != "/balance" {
					t.Errorf("Expected path /balance, got %s", r.URL.Path)
					return
				}

				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.serverResponse))
			}))
			defer server.Close()

			config := &Config{
				Host:      server.URL[7:], // Remove "http://"
				Port:      80,
				Timeout:   testTimeout,
				UserAgent: testUserAgent,
			}
			client := NewClient(config)
			client.baseURL = server.URL

			balance, err := client.GetBalance()

			if tt.wantError {
				if err == nil {
					t.Errorf("Expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				if balance.TargetBalance != tt.wantTargetBalance {
					t.Errorf("Expected target balance %d, got %d", tt.wantTargetBalance, balance.TargetBalance)
				}

				if balance.ActualBalance != tt.wantActualBalance {
					t.Errorf("Expected actual balance %d, got %d", tt.wantActualBalance, balance.ActualBalance)
				}

				if balance.DeviceID != tt.wantDeviceID {
					t.Errorf("Expected device ID %s, got %s", tt.wantDeviceID, balance.DeviceID)
				}
			}
		})
	}
}

func TestClient_SetBalance(t *testing.T) {
	tests := []struct {
		name      string
		level     int
		wantError bool
	}{
		{
			name:      "Valid balance level 0",
			level:     0,
			wantError: false,
		},
		{
			name:      "Valid balance level +7",
			level:     7,
			wantError: false,
		},
		{
			name:      "Valid balance level -7",
			level:     -7,
			wantError: false,
		},
		{
			name:      "Valid balance level +3",
			level:     3,
			wantError: false,
		},
		{
			name:      "Valid balance level -3",
			level:     -3,
			wantError: false,
		},
		{
			name:      "Invalid balance level +8",
			level:     8,
			wantError: true,
		},
		{
			name:      "Invalid balance level -8",
			level:     -8,
			wantError: true,
		},
		{
			name:      "Invalid balance level +100",
			level:     100,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.wantError {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "POST" {
						t.Errorf("Expected POST request, got %s", r.Method)
						return
					}

					if r.URL.Path != "/balance" {
						t.Errorf("Expected path /balance, got %s", r.URL.Path)
						return
					}

					// Verify Content-Type
					if contentType := r.Header.Get("Content-Type"); contentType != "application/xml" {
						t.Errorf("Expected Content-Type application/xml, got %s", contentType)
						return
					}

					// Parse and validate request body
					var balanceReq models.BalanceRequest

					err := xml.NewDecoder(r.Body).Decode(&balanceReq)
					if err != nil {
						t.Errorf("Failed to decode request XML: %v", err)
						return
					}

					if balanceReq.Level != tt.level {
						t.Errorf("Expected balance level %d, got %d", tt.level, balanceReq.Level)
						return
					}

					w.WriteHeader(http.StatusOK)
				}))
				defer server.Close()

				config := &Config{
					Host:      server.URL[7:],
					Port:      80,
					Timeout:   testTimeout,
					UserAgent: testUserAgent,
				}
				client := NewClient(config)
				client.baseURL = server.URL

				err := client.SetBalance(tt.level)
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			} else {
				// Test validation without server
				config := &Config{
					Host:      "localhost",
					Port:      8090,
					Timeout:   testTimeout,
					UserAgent: testUserAgent,
				}
				client := NewClient(config)

				err := client.SetBalance(tt.level)
				if err == nil {
					t.Errorf("Expected error for invalid balance level %d, got nil", tt.level)
				}
			}
		})
	}
}

func TestClient_SetBalanceSafe(t *testing.T) {
	tests := []struct {
		name          string
		level         int
		expectedLevel int
	}{
		{
			name:          "Valid level unchanged",
			level:         3,
			expectedLevel: 3,
		},
		{
			name:          "Too high clamped",
			level:         75,
			expectedLevel: 7,
		},
		{
			name:          "Too low clamped",
			level:         -75,
			expectedLevel: -7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Parse request body to verify clamped level
				var balanceReq models.BalanceRequest

				err := xml.NewDecoder(r.Body).Decode(&balanceReq)
				if err != nil {
					t.Errorf("Failed to decode request XML: %v", err)
					return
				}

				if balanceReq.Level != tt.expectedLevel {
					t.Errorf("Expected clamped balance level %d, got %d", tt.expectedLevel, balanceReq.Level)
					return
				}

				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			config := &Config{
				Host:      server.URL[7:],
				Port:      80,
				Timeout:   testTimeout,
				UserAgent: testUserAgent,
			}
			client := NewClient(config)
			client.baseURL = server.URL

			err := client.SetBalanceSafe(tt.level)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestClient_IncreaseBalance(t *testing.T) {
	tests := []struct {
		name               string
		currentBalance     int
		amount             int
		expectedNewBalance int
	}{
		{
			name:               "Normal increase",
			currentBalance:     0,
			amount:             3,
			expectedNewBalance: 3,
		},
		{
			name:               "Increase with clamping",
			currentBalance:     5,
			amount:             4,
			expectedNewBalance: 7,
		},
		{
			name:               "Increase from negative",
			currentBalance:     -5,
			amount:             2,
			expectedNewBalance: -3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getCallCount := 0
			postCallCount := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")

				if r.Method == "GET" && r.URL.Path == "/balance" {
					getCallCount++

					var response string
					if getCallCount == 1 {
						// First call - return current balance
						response = `<balance deviceID="ABCD1234EFGH"><targetbalance>` +
							fmt.Sprintf("%d", tt.currentBalance) + `</targetbalance><actualbalance>` +
							fmt.Sprintf("%d", tt.currentBalance) + `</actualbalance></balance>`
					} else {
						// Second call - return new balance level
						response = `<balance deviceID="ABCD1234EFGH"><targetbalance>` +
							fmt.Sprintf("%d", tt.expectedNewBalance) + `</targetbalance><actualbalance>` +
							fmt.Sprintf("%d", tt.expectedNewBalance) + `</actualbalance></balance>`
					}

					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(response))
				} else if r.Method == "POST" && r.URL.Path == "/balance" {
					postCallCount++

					w.WriteHeader(http.StatusOK)
				}
			}))

			defer server.Close()

			config := &Config{
				Host:      server.URL[7:],
				Port:      80,
				Timeout:   testTimeout,
				UserAgent: testUserAgent,
			}
			client := NewClient(config)
			client.baseURL = server.URL

			balance, err := client.IncreaseBalance(tt.amount)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if balance.GetLevel() != tt.expectedNewBalance {
				t.Errorf("Expected new balance level %d, got %d", tt.expectedNewBalance, balance.GetLevel())
			}

			if getCallCount != 2 {
				t.Errorf("Expected 2 GET calls, got %d", getCallCount)
			}

			if postCallCount != 1 {
				t.Errorf("Expected 1 POST call, got %d", postCallCount)
			}
		})
	}
}

func TestClient_DecreaseBalance(t *testing.T) {
	tests := []struct {
		name               string
		currentBalance     int
		amount             int
		expectedNewBalance int
	}{
		{
			name:               "Normal decrease",
			currentBalance:     3,
			amount:             2,
			expectedNewBalance: 1,
		},
		{
			name:               "Decrease with clamping",
			currentBalance:     -5,
			amount:             4,
			expectedNewBalance: -7,
		},
		{
			name:               "Decrease to negative",
			currentBalance:     4,
			amount:             6,
			expectedNewBalance: -2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getCallCount := 0
			postCallCount := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")

				if r.Method == "GET" && r.URL.Path == "/balance" {
					getCallCount++

					var response string
					if getCallCount == 1 {
						// First call - return current balance
						response = `<balance deviceID="ABCD1234EFGH"><targetbalance>` +
							fmt.Sprintf("%d", tt.currentBalance) + `</targetbalance><actualbalance>` +
							fmt.Sprintf("%d", tt.currentBalance) + `</actualbalance></balance>`
					} else {
						// Second call - return new balance level
						response = `<balance deviceID="ABCD1234EFGH"><targetbalance>` +
							fmt.Sprintf("%d", tt.expectedNewBalance) + `</targetbalance><actualbalance>` +
							fmt.Sprintf("%d", tt.expectedNewBalance) + `</actualbalance></balance>`
					}

					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(response))
				} else if r.Method == "POST" && r.URL.Path == "/balance" {
					postCallCount++

					w.WriteHeader(http.StatusOK)
				}
			}))

			defer server.Close()

			config := &Config{
				Host:      server.URL[7:],
				Port:      80,
				Timeout:   testTimeout,
				UserAgent: testUserAgent,
			}
			client := NewClient(config)
			client.baseURL = server.URL

			balance, err := client.DecreaseBalance(tt.amount)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if balance.GetLevel() != tt.expectedNewBalance {
				t.Errorf("Expected new balance level %d, got %d", tt.expectedNewBalance, balance.GetLevel())
			}

			if getCallCount != 2 {
				t.Errorf("Expected 2 GET calls, got %d", getCallCount)
			}

			if postCallCount != 1 {
				t.Errorf("Expected 1 POST call, got %d", postCallCount)
			}
		})
	}
}

func TestClient_BalanceAdjustmentUsesAdvertisedRange(t *testing.T) {
	tests := []struct {
		name     string
		current  int
		amount   int
		expected int
		adjust   func(*Client, int) (*models.Balance, error)
	}{
		{
			name:     "increase clamps to advertised maximum",
			current:  8,
			amount:   4,
			expected: 9,
			adjust:   (*Client).IncreaseBalance,
		},
		{
			name:     "decrease clamps to advertised minimum",
			current:  -10,
			amount:   5,
			expected: -12,
			adjust:   (*Client).DecreaseBalance,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getCalls := 0
			postedLevels := []int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/balance":
					getCalls++
					level := tt.current
					if getCalls > 1 {
						level = tt.expected
					}
					_, _ = fmt.Fprintf(w, `<balance deviceID="LEFT"><balanceAvailable>true</balanceAvailable><balanceMin>-12</balanceMin><balanceMax>9</balanceMax><balanceDefault>0</balanceDefault><targetBalance>%d</targetBalance><actualBalance>%d</actualBalance></balance>`, level, level)
				case r.Method == http.MethodPost && r.URL.Path == "/balance":
					var request models.BalanceRequest
					if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode balance request: %v", err)
						return
					}
					postedLevels = append(postedLevels, request.Level)
					w.WriteHeader(http.StatusOK)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client := NewClient(&Config{Host: server.URL[7:], Port: 80, Timeout: testTimeout, UserAgent: testUserAgent})
			client.baseURL = server.URL
			balance, err := tt.adjust(client, tt.amount)
			if err != nil {
				t.Fatalf("adjust balance: %v", err)
			}
			if balance.ActualBalance != tt.expected {
				t.Fatalf("actual balance = %d, want %d", balance.ActualBalance, tt.expected)
			}
			if fmt.Sprint(postedLevels) != fmt.Sprintf("[%d]", tt.expected) {
				t.Fatalf("posted levels = %v, want [%d]", postedLevels, tt.expected)
			}
			if getCalls != 2 {
				t.Fatalf("GET calls = %d, want 2", getCalls)
			}
		})
	}
}

func TestClient_BalanceAdjustmentDoesNotFallbackWhenUnavailable(t *testing.T) {
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalls++
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`<balance deviceID="LEFT"><balanceAvailable>false</balanceAvailable><balanceMin>-7</balanceMin><balanceMax>7</balanceMax><balanceDefault>0</balanceDefault><targetBalance>0</targetBalance><actualBalance>0</actualBalance></balance>`))
	}))
	defer server.Close()

	client := NewClient(&Config{Host: server.URL[7:], Port: 80, Timeout: testTimeout, UserAgent: testUserAgent})
	client.baseURL = server.URL
	if _, err := client.IncreaseBalance(1); err == nil || !containsSubstring(err.Error(), "balance is unavailable") {
		t.Fatalf("IncreaseBalance() error = %v, want unavailable", err)
	}
	if postCalls != 0 {
		t.Fatalf("unavailable balance sent %d POST requests", postCalls)
	}
}

func TestClient_Balance_ErrorHandling(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		method         func(*Client) error
		wantError      bool
		errorContains  string
	}{
		{
			name: "GetBalance server returns 404",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("Not Found"))
			},
			method: func(c *Client) error {
				_, err := c.GetBalance()
				return err
			},
			wantError:     true,
			errorContains: "failed to get balance",
		},
		{
			name: "SetBalance server returns 500",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("Internal Server Error"))
			},
			method: func(c *Client) error {
				return c.SetBalance(5)
			},
			wantError:     true,
			errorContains: "API request failed with status 500",
		},
		{
			name: "GetBalance invalid XML response",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("invalid xml"))
			},
			method: func(c *Client) error {
				_, err := c.GetBalance()
				return err
			},
			wantError:     true,
			errorContains: "failed to get balance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			config := &Config{
				Host:      server.URL[7:],
				Port:      80,
				Timeout:   testTimeout,
				UserAgent: testUserAgent,
			}
			client := NewClient(config)
			client.baseURL = server.URL

			err := tt.method(client)

			if tt.wantError {
				if err == nil {
					t.Errorf("Expected error, got nil")
				} else if !containsSubstring(err.Error(), tt.errorContains) {
					t.Errorf("Expected error containing '%s', got '%s'", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestClient_Balance_RequestFormat(t *testing.T) {
	// Test that the request XML format is correct
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and parse the raw request body
		var balanceReq models.BalanceRequest

		err := xml.NewDecoder(r.Body).Decode(&balanceReq)
		if err != nil {
			t.Errorf("Failed to decode request XML: %v", err)
			return
		}

		// Validate XML structure
		expectedLevel := 6
		if balanceReq.Level != expectedLevel {
			t.Errorf("Expected balance level %d, got %d", expectedLevel, balanceReq.Level)
		}

		// Re-encode to verify XML format
		actualXML, err := xml.Marshal(balanceReq)
		if err != nil {
			t.Errorf("Failed to marshal BalanceRequest: %v", err)
			return
		}

		expectedXML := "<balance>6</balance>"
		if string(actualXML) != expectedXML {
			t.Errorf("Expected XML '%s', got '%s'", expectedXML, string(actualXML))
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Host:      server.URL[7:],
		Port:      80,
		Timeout:   testTimeout,
		UserAgent: testUserAgent,
	}
	client := NewClient(config)
	client.baseURL = server.URL

	err := client.SetBalance(6)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
}
