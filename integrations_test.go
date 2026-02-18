package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSlackIntegrationNotifySendsPayload(t *testing.T) {
	received := ""
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", req.Method)
		}
		var payload map[string]string
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		received = payload["text"]
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})

	slack := NewSlackIntegration("https://hooks.slack.test/services/x")
	slack.client = &http.Client{Transport: rt}

	evt := IntegrationEvent{
		Type:       integrationEventDailyCompact,
		Message:    "Daily compact sent",
		OccurredAt: time.Now().UTC(),
	}

	if err := slack.Notify(context.Background(), evt); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if received != "Daily compact sent" {
		t.Fatalf("unexpected text: %q", received)
	}
}

func TestSlackIntegrationNotifyErrorOnNon2xx(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader("bad request")),
			Header:     make(http.Header),
		}, nil
	})
	slack := NewSlackIntegration("https://hooks.slack.test/services/x")
	slack.client = &http.Client{Transport: rt}

	err := slack.Notify(context.Background(), IntegrationEvent{Type: integrationEventDailyCompact, Message: "x"})
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeIntegration struct {
	name    string
	calls   int
	err     error
	lastEvt IntegrationEvent
}

func (f *fakeIntegration) Name() string {
	return f.name
}

func (f *fakeIntegration) Notify(_ context.Context, evt IntegrationEvent) error {
	f.calls++
	f.lastEvt = evt
	return f.err
}

func TestIntegrationDispatcherContinuesOnFailure(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	d := NewIntegrationDispatcher(logger)
	fail := &fakeIntegration{name: "fail", err: errors.New("boom")}
	ok := &fakeIntegration{name: "ok"}
	d.Add(fail)
	d.Add(ok)

	evt := IntegrationEvent{Type: integrationEventDailyCompact, Message: "daily compact"}
	d.Notify(context.Background(), evt)

	if fail.calls != 1 {
		t.Fatalf("expected fail integration to be called once, got %d", fail.calls)
	}
	if ok.calls != 1 {
		t.Fatalf("expected ok integration to be called once, got %d", ok.calls)
	}
	if ok.lastEvt.Type != integrationEventDailyCompact {
		t.Fatalf("unexpected event type %q", ok.lastEvt.Type)
	}
}
