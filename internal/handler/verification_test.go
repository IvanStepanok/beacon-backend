package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// doPatchVerification invokes the PATCH /reports/{id}/verification handler
// directly. Every asserted path below is rejected BEFORE the store is touched,
// so empty Deps are safe (the 409/force flow needs a DB and is exercised in the
// service-layer integration tests).
func doPatchVerification(t *testing.T, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := New(Deps{})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/reports/"+id+"/verification", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	rec := httptest.NewRecorder()
	h.PatchVerification(rec, req.WithContext(ctx))
	return rec
}

// TestPatchVerification_BadStatus locks the closed status enum: anything but
// pending|verified|flagged — via the canonical `status` key OR the legacy
// `verification` alias, or a missing status entirely — is a 400 validation
// error before any scope check or store write.
func TestPatchVerification_BadStatus(t *testing.T) {
	cases := map[string]string{
		"unknown status":        `{"status":"approved"}`,
		"unknown legacy alias":  `{"verification":"confirmed"}`,
		"empty body":            `{}`,
		"note without a status": `{"note":"looks fine","force":true}`,
	}
	for name, body := range cases {
		rec := doPatchVerification(t, "1156", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
			continue
		}
		if code := errCode(t, rec); code != "validation" {
			t.Errorf("%s: error code %q, want validation", name, code)
		}
	}

	// Malformed JSON is a 400 bad_json (decode, not validation).
	rec := doPatchVerification(t, "1156", `{"status":`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "bad_json" {
		t.Errorf("malformed body: status %d code %q, want 400 bad_json", rec.Code, errCode(t, rec))
	}
}
