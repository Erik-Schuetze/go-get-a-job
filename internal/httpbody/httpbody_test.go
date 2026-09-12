package httpbody

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestReadAll(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		limit   int64
		want    string
		wantErr bool
	}{
		{"under the limit", "hello", 10, "hello", false},
		{"empty body", "", 10, "", false},
		{"exactly the limit is accepted", "0123456789", 10, "0123456789", false},
		{"one byte over the limit is rejected", "01234567890", 10, "", true},
		{"much larger than the limit is rejected", strings.Repeat("a", 500), 10, "", true},
		{"a non-positive limit falls back to the default", "hello", 0, "hello", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadAll(strings.NewReader(tt.body), tt.limit)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for a %d-byte body against a %d-byte limit", len(tt.body), tt.limit)
				}
				var tooLarge *TooLargeError
				if !errors.As(err, &tooLarge) {
					t.Fatalf("expected a *TooLargeError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadAll returned unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("ReadAll = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTooLargeError_ReportsLimit(t *testing.T) {
	_, err := ReadAll(strings.NewReader(strings.Repeat("x", 100)), 8)

	var tooLarge *TooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected a *TooLargeError, got %v", err)
	}
	if tooLarge.Limit != 8 {
		t.Errorf("expected Limit 8, got %d", tooLarge.Limit)
	}
	if !strings.Contains(err.Error(), "8 byte limit") {
		t.Errorf("expected the message to name the limit, got %q", err.Error())
	}
}

func TestReadAll_ReaderErrorIsPropagated(t *testing.T) {
	_, err := ReadAll(failingReader{}, 10)
	if err == nil {
		t.Fatal("expected the reader's error to be returned")
	}
	var tooLarge *TooLargeError
	if errors.As(err, &tooLarge) {
		t.Errorf("a read failure should not be reported as a size violation: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, fmt.Errorf("boom") }

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	t.Run("valid JSON under the limit", func(t *testing.T) {
		var got payload
		if err := DecodeJSON(strings.NewReader(`{"name":"acme"}`), 100, &got); err != nil {
			t.Fatalf("DecodeJSON returned error: %v", err)
		}
		if got.Name != "acme" {
			t.Errorf("expected name acme, got %q", got.Name)
		}
	})

	t.Run("oversized body fails with TooLargeError, not a syntax error", func(t *testing.T) {
		var got payload
		err := DecodeJSON(strings.NewReader(`{"name":"`+strings.Repeat("a", 200)+`"}`), 50, &got)
		if err == nil {
			t.Fatal("expected an error")
		}
		var tooLarge *TooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("expected a *TooLargeError, got %T: %v", err, err)
		}
	})

	t.Run("malformed JSON under the limit is a decode error", func(t *testing.T) {
		var got payload
		err := DecodeJSON(strings.NewReader(`{"name":`), 100, &got)
		if err == nil {
			t.Fatal("expected an error")
		}
		var tooLarge *TooLargeError
		if errors.As(err, &tooLarge) {
			t.Errorf("a malformed body should not be reported as a size violation: %v", err)
		}
	})
}
