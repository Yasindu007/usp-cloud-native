package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/urlshortener/platform/internal/domain/analytics"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
)

type fakeStreamURLRepo struct {
	url *domainurl.URL
	err error
}

func (f *fakeStreamURLRepo) GetByID(_ context.Context, _, _ string) (*domainurl.URL, error) {
	return f.url, f.err
}

type fakeClickSubscription struct {
	ch chan *analytics.ClickEvent
}

func (f *fakeClickSubscription) Events() <-chan *analytics.ClickEvent { return f.ch }
func (f *fakeClickSubscription) Close() error                         { close(f.ch); return nil }

type fakeClickSubscriber struct {
	sub *fakeClickSubscription
}

func (f *fakeClickSubscriber) SubscribeToURL(_ context.Context, _, _ string) (redisinfra.ClickEventStream, error) {
	return f.sub, nil
}
func (f *fakeClickSubscriber) SubscribeToWorkspace(_ context.Context, _ string) (redisinfra.ClickEventStream, error) {
	return f.sub, nil
}

func withStreamClaims(r *http.Request) *http.Request {
	return r.WithContext(domainauth.WithContext(r.Context(), &domainauth.Claims{
		UserID: "usr_001", WorkspaceID: "ws_001", Scope: "read write",
	}))
}

func withStreamURLID(r *http.Request, urlID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspaceID", "ws_001")
	rctx.URLParams.Add("urlID", urlID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestStreamHandlerStreamURLWritesClickEvent(t *testing.T) {
	sub := &fakeClickSubscription{ch: make(chan *analytics.ClickEvent, 1)}
	h := handler.NewStreamHandler(
		&fakeStreamURLRepo{url: &domainurl.URL{ID: "url_001", ShortCode: "abc1234", WorkspaceID: "ws_001"}},
		&fakeClickSubscriber{sub: sub},
		testLog,
	)

	ctx, cancel := context.WithCancel(context.Background())
	req := withStreamClaims(withStreamURLID(httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx), "url_001"))
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.StreamURL(rec, req)
		close(done)
	}()

	sub.ch <- &analytics.ClickEvent{
		ShortCode:   "abc1234",
		WorkspaceID: "ws_001",
		OccurredAt:  time.Now().UTC(),
		DeviceType:  string(analytics.DeviceTypeDesktop),
		CountryCode: "XX",
		RequestID:   "req_001",
	}
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("expected connected event, got %q", body)
	}
	if !strings.Contains(body, "event: click") {
		t.Fatalf("expected click event, got %q", body)
	}
	if !strings.Contains(body, "\"short_code\":\"abc1234\"") {
		t.Fatalf("expected click payload, got %q", body)
	}
}

func TestStreamHandlerStreamWorkspaceSkipsBotEvents(t *testing.T) {
	sub := &fakeClickSubscription{ch: make(chan *analytics.ClickEvent, 1)}
	h := handler.NewStreamHandler(&fakeStreamURLRepo{}, &fakeClickSubscriber{sub: sub}, testLog)

	ctx, cancel := context.WithCancel(context.Background())
	req := withStreamClaims(httptest.NewRequest(http.MethodGet, "/workspace-stream", nil).WithContext(ctx))
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.StreamWorkspace(rec, req)
		close(done)
	}()

	sub.ch <- &analytics.ClickEvent{
		ShortCode:   "abc1234",
		WorkspaceID: "ws_001",
		IsBot:       true,
	}
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit")
	}

	body := rec.Body.String()
	if strings.Contains(body, "event: click") {
		t.Fatalf("expected bot event to be skipped, got %q", body)
	}
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("expected connected event, got %q", body)
	}
}
