package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
)

// ── Fake audit capturer ────────────────────────────────────────────────────────

type fakeAuditCapturer struct {
	captured []*domainaudit.Event
}

func (f *fakeAuditCapturer) BuildEvent(
	_ context.Context,
	action domainaudit.Action,
	resourceType domainaudit.ResourceType,
	resourceID string,
	sourceIP, userAgent, requestID string,
	metadata map[string]any,
) *domainaudit.Event {
	return &domainaudit.Event{
		ID:           "test_evt",
		ActorID:      "usr_test",
		ActorType:    domainaudit.ActorUser,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		SourceIP:     sourceIP,
		UserAgent:    userAgent,
		RequestID:    requestID,
		Metadata:     metadata,
	}
}

func (f *fakeAuditCapturer) Capture(evt *domainaudit.Event) {
	f.captured = append(f.captured, evt)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestAuditAction_SuccessfulRequest_CapturesEvent(t *testing.T) {
	capturer := &fakeAuditCapturer{}
	mw := httpmiddleware.AuditAction(capturer, domainaudit.ActionURLCreate)

	// Handler annotates context with the created resource
	successHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domainaudit.AnnotateContext(r.Context(),
			domainaudit.ResourceURL, "url_created_001",
			map[string]any{"short_code": "abc1234"})
		w.WriteHeader(http.StatusCreated)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)

	mw(successHandler).ServeHTTP(w, r)

	if len(capturer.captured) != 1 {
		t.Fatalf("expected 1 captured event, got %d", len(capturer.captured))
	}

	evt := capturer.captured[0]
	if evt.Action != domainaudit.ActionURLCreate {
		t.Errorf("expected action=url:create, got %q", evt.Action)
	}
	if evt.ResourceID != "url_created_001" {
		t.Errorf("expected ResourceID=url_created_001, got %q", evt.ResourceID)
	}
	if evt.Metadata["short_code"] != "abc1234" {
		t.Errorf("expected Metadata[short_code]=abc1234, got %v", evt.Metadata["short_code"])
	}
}

func TestAuditAction_FailedRequest_DoesNotCapture(t *testing.T) {
	capturer := &fakeAuditCapturer{}
	mw := httpmiddleware.AuditAction(capturer, domainaudit.ActionURLCreate)

	// Handler returns 422 — validation failed, no resource was created
	errorHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)

	mw(errorHandler).ServeHTTP(w, r)

	if len(capturer.captured) != 0 {
		t.Errorf("expected 0 captured events for 422 response, got %d", len(capturer.captured))
	}
}

func TestAuditAction_ServerError_DoesNotCapture(t *testing.T) {
	capturer := &fakeAuditCapturer{}
	mw := httpmiddleware.AuditAction(capturer, domainaudit.ActionWorkspaceCreate)

	handler500 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/workspaces", nil)

	mw(handler500).ServeHTTP(w, r)

	if len(capturer.captured) != 0 {
		t.Errorf("expected 0 captured events for 500 response, got %d", len(capturer.captured))
	}
}

func TestAuditAction_HandlerDoesNotAnnotate_DoesNotCapture(t *testing.T) {
	// Handler returns 201 but never calls AnnotateContext.
	// The event must NOT be captured (no resource ID = incomplete event).
	capturer := &fakeAuditCapturer{}
	mw := httpmiddleware.AuditAction(capturer, domainaudit.ActionURLCreate)

	noAnnotationHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately no AnnotateContext call
		w.WriteHeader(http.StatusCreated)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/urls", nil)

	mw(noAnnotationHandler).ServeHTTP(w, r)

	if len(capturer.captured) != 0 {
		t.Errorf("expected 0 captured events when handler does not annotate, got %d",
			len(capturer.captured))
	}
}

func TestAuditAction_PendingEventAvailableInHandlerContext(t *testing.T) {
	capturer := &fakeAuditCapturer{}
	mw := httpmiddleware.AuditAction(capturer, domainaudit.ActionMemberAdd)

	var pendingEventFound bool
	checkHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pendingEventFound = domainaudit.PendingEventFromContext(r.Context())
		domainaudit.AnnotateContext(r.Context(), domainaudit.ResourceMember, "mem_001", nil)
		w.WriteHeader(http.StatusCreated)
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/members", nil)

	mw(checkHandler).ServeHTTP(w, r)

	if !pendingEventFound {
		t.Error("expected pending audit event to be available in handler context")
	}
}

func TestAuditAction_2xxVariants_AllCapture(t *testing.T) {
	for _, status := range []int{200, 201, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			capturer := &fakeAuditCapturer{}
			mw := httpmiddleware.AuditAction(capturer, domainaudit.ActionURLCreate)

			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				domainaudit.AnnotateContext(r.Context(),
					domainaudit.ResourceURL, "url_001", nil)
				w.WriteHeader(status)
			})

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/urls", nil)
			mw(h).ServeHTTP(w, r)

			if len(capturer.captured) != 1 {
				t.Errorf("status %d: expected 1 captured event, got %d",
					status, len(capturer.captured))
			}
		})
	}
}
