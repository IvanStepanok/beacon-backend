package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/stepanok/beacon-server/internal/model"
)

// doPatchCrisisForm invokes the PATCH /crises/{id}/form handler directly with
// the given (possibly nil = anonymous) user. Every asserted path below is
// rejected BEFORE the store is touched, so empty Deps are safe.
func doPatchCrisisForm(t *testing.T, u *model.User, crisisID, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := New(Deps{})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/crises/"+crisisID+"/form", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", crisisID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if u != nil {
		ctx = WithUser(ctx, u)
	}
	rec := httptest.NewRecorder()
	h.PatchCrisisForm(rec, req.WithContext(ctx))
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an error envelope: %v (%s)", err, rec.Body.String())
	}
	return env.Error
}

// TestPatchCrisisForm_RBAC locks the role gate: shaping a crisis's capture form
// is a senior decision — ONLY regional_analyst / crisis_admin pass; every other
// role (and anonymous, fail-closed even if the router middleware were bypassed)
// gets 403 before anything is validated or stored.
func TestPatchCrisisForm_RBAC(t *testing.T) {
	body := `{"required":["electricity"],"disabled":[]}`
	denied := []*model.User{
		nil, // anonymous — requireMutator normally 401s first; the handler still fails closed
		{ID: "u1", Email: "fv@undp.org", Role: model.RoleFieldValidator, CrisisScope: []string{"*"}},
		{ID: "u2", Email: "co@undp.org", Role: model.RoleCOAnalyst, CrisisScope: []string{"*"}},
		{ID: "u3", Email: "ev@undp.org", Role: model.RoleExternalViewer, CrisisScope: []string{"*"}},
	}
	for _, u := range denied {
		rec := doPatchCrisisForm(t, u, "crisis-antakya", body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("user %+v: status %d, want 403", u, rec.Code)
			continue
		}
		if code := errCode(t, rec); code != "forbidden" {
			t.Errorf("user %+v: error code %q, want forbidden", u, code)
		}
	}
}

// TestPatchCrisisForm_Scope locks the crisis-scope gate: a senior role still
// may not adjust the form of a crisis OUTSIDE its scope (403 out_of_scope),
// while the same body passes the role+scope+validation gates for an in-scope
// admin (it then proceeds to the store — not exercised here, no DB).
func TestPatchCrisisForm_Scope(t *testing.T) {
	u := &model.User{ID: "u4", Email: "ra@undp.org", Role: model.RoleRegionalAnalyst, CrisisScope: []string{"crisis-antakya"}}
	rec := doPatchCrisisForm(t, u, "crisis-elsewhere", `{"required":["pressingNeeds"]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope status %d, want 403", rec.Code)
	}
	if code := errCode(t, rec); code != "out_of_scope" {
		t.Errorf("out-of-scope error code %q, want out_of_scope", code)
	}
}

// TestPatchCrisisForm_Validation locks the body validation for an authorized
// caller: unknown section keys and contradictory required+disabled are 400
// validation errors (rejected before any store write).
func TestPatchCrisisForm_Validation(t *testing.T) {
	admin := &model.User{ID: "u5", Email: "admin@undp.org", Role: model.RoleCrisisAdmin, CrisisScope: []string{"*"}}
	cases := map[string]string{
		"unknown key":       `{"required":["ghost"]}`,
		"unknown disabled":  `{"disabled":["what3words"]}`,
		"required+disabled": `{"required":["electricity"],"disabled":["electricity"]}`,
	}
	for name, body := range cases {
		rec := doPatchCrisisForm(t, admin, "crisis-antakya", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
			continue
		}
		if code := errCode(t, rec); code != "validation" {
			t.Errorf("%s: error code %q, want validation", name, code)
		}
	}

	// Malformed JSON is a 400 bad_json (decode, not validation).
	rec := doPatchCrisisForm(t, admin, "crisis-antakya", `{"required":`)
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "bad_json" {
		t.Errorf("malformed body: status %d code %q, want 400 bad_json", rec.Code, errCode(t, rec))
	}
}
