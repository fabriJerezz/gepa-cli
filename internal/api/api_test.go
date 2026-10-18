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
	h, _ := newTestEnv(t, instanceName)
	return h
}

// newTestEnv es como newTestServer pero también devuelve el store, para
// los tests que necesitan proyectar (en producción lo hace el proyector en
// segundo plano; en los tests se fuerza con Rebuild para que sea
// determinístico).
func newTestEnv(t *testing.T, instanceName string) (http.Handler, *store.Store) {
	t.Helper()

	mr := miniredis.RunT(t)
	s, err := store.Open(context.Background(), mr.Addr())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return NewServer(s, instanceName), s
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

	rec := doRequest(t, h, http.MethodPost, "/matches", map[string]int{"team_a_id": 1, "team_b_id": 2})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, esperaba %d (equipos inexistentes)", rec.Code, http.StatusNotFound)
	}
}

func TestPartidoEnVivoFlujoCompleto(t *testing.T) {
	h, s := newTestEnv(t, "app1")
	teamA := createTeam(t, h, "Argentina")
	teamB := createTeam(t, h, "Francia")

	rec := doRequest(t, h, http.MethodPost, "/matches", map[string]int{"team_a_id": teamA, "team_b_id": teamB})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("iniciando partido: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	for _, ev := range []map[string]any{
		{"type": "goal", "team_id": teamA, "minute": 12, "expected_version": 1},
		{"type": "yellow_card", "team_id": teamB, "minute": 40, "expected_version": 2},
		{"type": "match_finished", "expected_version": 3},
	} {
		rec = doRequest(t, h, http.MethodPost, "/matches/1/events", ev)
		if rec.Code != http.StatusCreated {
			t.Fatalf("evento %v: status = %d, body = %s", ev["type"], rec.Code, rec.Body.String())
		}
	}

	if _, err := s.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	rec = doRequest(t, h, http.MethodGet, "/matches", nil)
	var matches []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &matches)
	if len(matches) != 1 || matches[0]["status"] != "finished" || matches[0]["score_a"] != 1.0 || len(matches[0]["events"].([]any)) != 4 {
		t.Fatalf("partido inesperado: %v", matches)
	}

	rec = doRequest(t, h, http.MethodGet, "/standings", nil)
	var rows []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &rows)
	if len(rows) != 2 || rows[0]["team_name"] != "Argentina" || rows[0]["points"] != 3.0 {
		t.Fatalf("tabla inesperada: %v", rows)
	}
}

func TestEventoConVersionViejaDevuelve409(t *testing.T) {
	h := newTestServer(t, "app1")
	teamA := createTeam(t, h, "Argentina")
	teamB := createTeam(t, h, "Francia")
	doRequest(t, h, http.MethodPost, "/matches", map[string]int{"team_a_id": teamA, "team_b_id": teamB})

	goal := map[string]any{"type": "goal", "team_id": teamA, "expected_version": 1}
	if rec := doRequest(t, h, http.MethodPost, "/matches/1/events", goal); rec.Code != http.StatusCreated {
		t.Fatalf("primer gol: status = %d", rec.Code)
	}
	rec := doRequest(t, h, http.MethodPost, "/matches/1/events", goal)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"current_version":2`) {
		t.Fatalf("status = %d, body = %s; esperaba 409 con current_version", rec.Code, rec.Body.String())
	}
}

func TestRelojDelPartidoNoPermiteAnotarHaciaAtras(t *testing.T) {
	h := newTestServer(t, "app1")
	teamA := createTeam(t, h, "Argentina")
	teamB := createTeam(t, h, "Francia")
	doRequest(t, h, http.MethodPost, "/matches", map[string]int{"team_a_id": teamA, "team_b_id": teamB})

	if rec := doRequest(t, h, http.MethodPost, "/matches/1/events", map[string]any{"type": "clock_advanced", "advance_sec": 600}); rec.Code != http.StatusCreated {
		t.Fatalf("avanzando el reloj: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, h, http.MethodPost, "/matches/1/events", map[string]any{"type": "goal", "team_id": teamA}); rec.Code != http.StatusCreated {
		t.Fatalf("gol en el 10': status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec := doRequest(t, h, http.MethodPost, "/matches/1/events", map[string]any{"type": "goal", "team_id": teamB, "clock_sec": 60})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "antes del último evento") {
		t.Fatalf("gol en el 1' después de uno en el 10': status = %d, body = %s; esperaba 400", rec.Code, rec.Body.String())
	}
}

func TestIdempotencyKeyNoDuplicaPartidos(t *testing.T) {
	h, s := newTestEnv(t, "app1")
	teamA := createTeam(t, h, "Argentina")
	teamB := createTeam(t, h, "Francia")
	body := map[string]int{"team_a_id": teamA, "team_b_id": teamB}

	first := doRequestWithKey(t, h, http.MethodPost, "/matches", body, "clave-1")
	second := doRequestWithKey(t, h, http.MethodPost, "/matches", body, "clave-1")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("status = %d / %d", first.Code, second.Code)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" || second.Body.String() != first.Body.String() {
		t.Fatalf("el reintento debería devolver la respuesta original: %q vs %q", second.Body.String(), first.Body.String())
	}

	// Una key nueva sí es un partido nuevo (dos partidos reales A vs B).
	doRequestWithKey(t, h, http.MethodPost, "/matches", body, "clave-2")
	_, _ = s.Rebuild(context.Background())
	var matches []map[string]any
	_ = json.Unmarshal(doRequest(t, h, http.MethodGet, "/matches", nil).Body.Bytes(), &matches)
	if len(matches) != 2 {
		t.Fatalf("esperaba 2 partidos (clave-1 una vez + clave-2), obtuve %d", len(matches))
	}
}

func TestIdempotencyKeyConOtroBodyDevuelve422(t *testing.T) {
	h := newTestServer(t, "app1")
	doRequestWithKey(t, h, http.MethodPost, "/teams", map[string]string{"name": "Argentina"}, "k")

	rec := doRequestWithKey(t, h, http.MethodPost, "/teams", map[string]string{"name": "Brasil"}, "k")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, esperaba 422", rec.Code)
	}
}

func TestIdempotencyKeySeLiberaSiFalla(t *testing.T) {
	h := newTestServer(t, "app1")

	// Falla (equipos inexistentes): no hubo efectos, la key se libera...
	body := map[string]int{"team_a_id": 1, "team_b_id": 2}
	if rec := doRequestWithKey(t, h, http.MethodPost, "/matches", body, "k"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, esperaba 404", rec.Code)
	}
	// ...y el reintento con la misma key se procesa de verdad.
	createTeam(t, h, "Argentina")
	createTeam(t, h, "Francia")
	rec := doRequestWithKey(t, h, http.MethodPost, "/matches", body, "k")
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("status = %d, replayed = %q; esperaba 201 procesado de nuevo", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
}

func doRequestWithKey(t *testing.T, h http.Handler, method, path string, body any, key string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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

func TestDeleteMatchCancelaYEsIdempotente(t *testing.T) {
	h := newTestServer(t, "app1")
	teamA := createTeam(t, h, "Argentina")
	teamB := createTeam(t, h, "Francia")

	rec := doRequest(t, h, http.MethodPost, "/matches", map[string]int{"team_a_id": teamA, "team_b_id": teamB})
	if rec.Code != http.StatusCreated {
		t.Fatalf("creando partido: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	for range 2 {
		rec = doRequest(t, h, http.MethodDelete, "/matches/1", nil)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("cancelando partido: status = %d, body = %q", rec.Code, rec.Body.String())
		}
	}
	if rec = doRequest(t, h, http.MethodDelete, "/matches/99", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("partido inexistente: status = %d, esperaba 404", rec.Code)
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
