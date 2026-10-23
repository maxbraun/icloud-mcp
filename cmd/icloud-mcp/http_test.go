package main

import "testing"

func TestValidBearerToken(t *testing.T) {
	token := "SENTINEL-HTTP-BEARER-TOKEN-0123456789" // gitleaks:allow, test sentinel
	for _, tc := range []struct {
		name   string
		header string
		want   bool
	}{
		{name: "valid", header: "Bearer " + token, want: true},
		{name: "wrong scheme", header: "Basic " + token, want: false},
		{name: "wrong token", header: "Bearer SENTINEL-HTTP-BEARER-TOKEN-0123456780", want: false},
		{name: "extra whitespace", header: "Bearer  " + token, want: false},
		{name: "empty", header: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validBearerToken(tc.header, token); got != tc.want {
				t.Fatalf("validBearerToken(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestServeHTTPRejectsEmptyToken(t *testing.T) {
	err := serveHTTP(newMCPServer(nil), "127.0.0.1:0", "", nil, nil)
	if err == nil {
		t.Fatal("serveHTTP accepted an empty bearer token")
	}
}
