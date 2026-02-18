package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	integrationEventDailyCompact = "daily_compact"
)

type IntegrationEvent struct {
	Type       string         `json:"type"`
	Message    string         `json:"message"`
	OccurredAt time.Time      `json:"occurred_at"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type Integration interface {
	Name() string
	Notify(context.Context, IntegrationEvent) error
}

type IntegrationDispatcher struct {
	logger       *log.Logger
	integrations []Integration
}

func NewIntegrationDispatcher(logger *log.Logger) *IntegrationDispatcher {
	return &IntegrationDispatcher{logger: logger, integrations: make([]Integration, 0, 2)}
}

func (d *IntegrationDispatcher) Add(integration Integration) {
	if integration == nil {
		return
	}
	d.integrations = append(d.integrations, integration)
	if d.logger != nil {
		d.logger.Printf("event=integration_enabled name=%s", integration.Name())
	}
}

func (d *IntegrationDispatcher) HasIntegrations() bool {
	return len(d.integrations) > 0
}

func (d *IntegrationDispatcher) Notify(ctx context.Context, evt IntegrationEvent) {
	for _, integration := range d.integrations {
		if err := integration.Notify(ctx, evt); err != nil {
			if d.logger != nil {
				d.logger.Printf("event=integration_notify_failed name=%s type=%s err=%v", integration.Name(), evt.Type, err)
			}
			continue
		}
		if d.logger != nil {
			d.logger.Printf("event=integration_notify_ok name=%s type=%s", integration.Name(), evt.Type)
		}
	}
}

type SlackIntegration struct {
	webhookURL string
	client     *http.Client
}

func NewSlackIntegration(webhookURL string) *SlackIntegration {
	return &SlackIntegration{
		webhookURL: strings.TrimSpace(webhookURL),
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *SlackIntegration) Name() string {
	return "slack"
}

func (s *SlackIntegration) Notify(ctx context.Context, evt IntegrationEvent) error {
	if s.webhookURL == "" {
		return fmt.Errorf("missing webhook url")
	}
	payload := map[string]string{
		"text": s.renderText(evt),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("unexpected status %d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (s *SlackIntegration) renderText(evt IntegrationEvent) string {
	if strings.TrimSpace(evt.Message) != "" {
		return evt.Message
	}
	return fmt.Sprintf("Pudnats event: %s", evt.Type)
}
