package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"gepa-cli/internal/store"
)

// newTestServer arma un handler completo (mux + middlewares) sobre un store
// respaldado por miniredis, igual que en producción pero sin depender de un
// Redis real. instanceName simula el valor que en Docker viene de la
// variable de entorno INSTANCE_NAME.
func newTestServer(t *testing.T, instanceName string) http.Handler {
	t.Helper()

	mr := miniredis.RunT(t)
	s, err := store.Open(context.Background(), mr.Addr())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return NewServer(s, instanceName)
}

func doRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	h := newTestServer(t, "app1")

	rec := doRequest(t, h, http.MethodGet, "/health", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperaba %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Instance"); got != "app1" {
		t.Errorf("X-Instance = %q, esperaba %q", got, "app1")
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if body["instance"] != "app1" {
		t.Errorf("body.instance = %q, esperaba %q", body["instance"], "app1")
	}
}

func TestFrontendMantieneInstancia(t *testing.T) {
	h := newTestServer(t, "app1")

	rec := doRequest(t, h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperaba %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("Content-Type = %q, esperaba HTML", got)
	}
	if !strings.Contains(rec.Body.String(), "FutGo") {
		t.Error("el frontend no contiene la marca FutGo")
	}
	if got := rec.Header().Get("X-Instance"); got != "app1" {
		t.Errorf("X-Instance = %q, esperaba %q", got, "app1")
	}
}

func TestCreateYListarPlayers(t *testing.T) {
	h := newTestServer(t, "app1")
	teamID := createTeam(t, h, "Argentina")

	rec := doRequest(t, h, http.MethodPost, "/players", map[string]any{
		"name":    "Lionel Messi",
		"team_id": teamID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodGet, "/players", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var players []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &players); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if len(players) != 1 || players[0]["name"] != "Lionel Messi" || int(players[0]["team_id"].(float64)) != teamID {
		t.Errorf("jugadores inesperados: %v", players)
	}
}

func TestDeletePlayerYListaVacia(t *testing.T) {
	h := newTestServer(t, "app1")
	teamID := createTeam(t, h, "Argentina")

	rec := doRequest(t, h, http.MethodPost, "/players", map[string]any{
		"name":    "Lionel Messi",
		"team_id": teamID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("creando jugador: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodDelete, "/players/1", nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("borrando jugador: status = %d, body = %q", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodGet, "/players", nil)
	var players []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &players); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if len(players) != 0 {
		t.Fatalf("esperaba lista vacía, obtuve %v", players)
	}
}

func TestDeletePlayerInexistenteDevuelve404(t *testing.T) {
	h := newTestServer(t, "app1")

	rec := doRequest(t, h, http.MethodDelete, "/players/999", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, esperaba %d", rec.Code, http.StatusNotFound)
	}
}

func TestCreatePlayerNombreVacio(t *testing.T) {
	h := newTestServer(t, "app1")

	rec := doRequest(t, h, http.MethodPost, "/players", map[string]any{"name": "", "team_id": 0})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, esperaba %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCreatePlayerEquipoInexistenteDevuelve404(t *testing.T) {
	h := newTestServer(t, "app1")

	rec := doRequest(t, h, http.MethodPost, "/players", map[string]any{
		"name":    "Lionel Messi",
		"team_id": 999,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, esperaba %d (equipo inexistente)", rec.Code, http.StatusNotFound)
	}
}

func TestCreateMatchEquipoInexistenteDevuelve404(t *testing.T) {
	h := newTestServer(t, "app1")

	rec := doRequest(t, h, http.MethodPost, "/matches", map[string]int{
		"team_a_id": 1,
		"team_b_id": 2,
		"score_a":   1,
		"score_b":   0,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, esperaba %d (equipos inexistentes)", rec.Code, http.StatusNotFound)
	}
}

func TestCreateMatchFlujoCompleto(t *testing.T) {
	h := newTestServer(t, "app1")

	teamA := createTeam(t, h, "Argentina")
	teamB := createTeam(t, h, "Francia")

	rec := doRequest(t, h, http.MethodPost, "/matches", map[string]int{
		"team_a_id": teamA,
		"team_b_id": teamB,
		"score_a":   3,
		"score_b":   3,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodGet, "/matches", nil)
	var matches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &matches); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("esperaba 1 partido, obtuve %d", len(matches))
	}
}

func TestDeleteTeamConJugadorYLuegoSinEl(t *testing.T) {
	h := newTestServer(t, "app1")
	teamID := createTeam(t, h, "Argentina")

	rec := doRequest(t, h, http.MethodPost, "/players", map[string]any{
		"name":    "Lionel Messi",
		"team_id": teamID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("creando jugador: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodDelete, "/teams/1", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "el equipo tiene jugadores") {
		t.Fatalf("borrando equipo con jugador: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodDelete, "/players/1", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("borrando jugador: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, h, http.MethodDelete, "/teams/1", nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("borrando equipo vacío: status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestDeleteMatch(t *testing.T) {
	h := newTestServer(t, "app1")
	teamA := createTeam(t, h, "Argentina")
	teamB := createTeam(t, h, "Francia")

	rec := doRequest(t, h, http.MethodPost, "/matches", map[string]int{
		"team_a_id": teamA,
		"team_b_id": teamB,
		"score_a":   3,
		"score_b":   1,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("creando partido: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, http.MethodDelete, "/matches/1", nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("borrando partido: status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestDeleteEntidadIDInvalidoDevuelve400(t *testing.T) {
	h := newTestServer(t, "app1")

	for _, path := range []string{"/players/no-es-un-id", "/teams/no-es-un-id", "/matches/no-es-un-id"} {
		t.Run(path, func(t *testing.T) {
			rec := doRequest(t, h, http.MethodDelete, path, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, esperaba %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

// createTeam es un helper de test: crea un equipo vía la API y devuelve su ID.
func createTeam(t *testing.T, h http.Handler, name string) int {
	t.Helper()

	rec := doRequest(t, h, http.MethodPost, "/teams", map[string]string{"name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("creando equipo %q: status = %d", name, rec.Code)
	}

	var team map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	return int(team["id"].(float64))
}
