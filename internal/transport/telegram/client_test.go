package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTransportErrorRedactsBotToken(t *testing.T) {
	token := strings.Join([]string{"123456789", "abcdefghijklmnopqrstuvwxyzABCDEFG"}, ":")
	sentinel := errors.New("connection failed")

	client := NewClient(token)
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{
			Op:  "POST",
			URL: request.URL.String(),
			Err: errors.Join(sentinel, errors.New("upstream rejected "+request.URL.String())),
		}
	})})

	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("GetMe succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked bot token: %q", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error does not identify redaction: %q", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("sanitized error does not preserve its cause")
	}
}

func TestAPIErrorsRemainUseful(t *testing.T) {
	token := strings.Join([]string{"123456789", "abcdefghijklmnopqrstuvwxyzABCDEFG"}, ":")
	client := NewClient(token)
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error_code":401,"description":"Unauthorized"}`)),
		}, nil
	})})

	_, err := client.GetMe(context.Background())
	if err == nil || err.Error() != "401: Unauthorized" {
		t.Fatalf("GetMe error = %v", err)
	}
}
