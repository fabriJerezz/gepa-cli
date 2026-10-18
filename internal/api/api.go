// Package api expone la funcionalidad del store como una API REST en JSON.
// Jugadores y equipos son CRUD simple; los partidos separan comandos
// (escritura de eventos) de consultas (modelos de lectura). Los handlers son
// delgados a propósito: la validación de negocio vive en internal/store e
// internal/model.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"gepa-cli/internal/model"
	"gepa-cli/internal/store"
	"gepa-cli/internal/web"
)

// NewServer arma el mux con todas las rutas de la API y le agrega los
// middlewares de logging e identificación de instancia. Usa el enrutamiento
// por método+path nativo de net/http (disponible desde Go 1.22), por eso no
// hace falta un router externo.
//
// instanceName identifica a esta instancia en particular (p. ej. "app1").
// Cuando corren varias instancias detrás de un balanceador de carga, es la
// forma de saber "a quién le pegó" cada request: viaja en la cabecera
// X-Instance de toda respuesta y en el campo "instance" de /health.
func NewServer(s *store.Store, instanceName string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", handleHealth(instanceName))

	mux.HandleFunc("GET /players", handleListPlayers(s))
	mux.HandleFunc("POST /players", handleCreatePlayer(s))
	mux.HandleFunc("DELETE /players/{id}", handleDeletePlayer(s))

	mux.HandleFunc("GET /teams", handleListTeams(s))
	mux.HandleFunc("POST /teams", handleCreateTeam(s))
	mux.HandleFunc("DELETE /teams/{id}", handleDeleteTeam(s))

	// Partidos con CQRS: los POST/DELETE son comandos que agregan eventos;
	// los GET leen modelos de lectura armados por el proyector.
	mux.HandleFunc("POST /matches", handleCreateMatch(s))
	mux.HandleFunc("POST /matches/{id}/events", handleAppendEvent(s))
	mux.HandleFunc("DELETE /matches/{id}", handleDeleteMatch(s))
	mux.HandleFunc("GET /matches", handleListMatches(s))
	mux.HandleFunc("GET /standings", handleStandings(s))
	mux.HandleFunc("POST /projections/rebuild", handleRebuild(s))

	web.Mount(mux)

	return logMiddleware(instanceMiddleware(instanceName, idempotencyMiddleware(s, mux)))
}

// logMiddleware imprime método y path de cada request recibido; útil para
// ver qué está pasando cuando la API corre dentro de un contenedor.
//
// Usamos %q (no %s) para method y path: %s los volcaría tal cual al log, y
// method/path vienen del cliente sin validar, así que alguien podría meter
// un salto de línea para falsificar una entrada de log completa (log
// injection). %q los escapa entre comillas, así ese contenido queda
// visible pero inerte.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// #nosec G706 -- gosec marca cualquier dato del request que llegue
		// a un log, sin evaluar el verbo usado. %q ya escapa saltos de
		// línea y caracteres de control, así que no hay log injection real.
		log.Printf("%q %q", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// instanceMiddleware agrega la cabecera X-Instance a toda respuesta, para
// poder identificar con `curl -i` (o mirando las dev tools) qué instancia
// atendió cada request sin necesidad de pegarle a /health.
func instanceMiddleware(instanceName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Instance", instanceName)
		// Hora del servidor en ms: la interfaz la usa para que el reloj del
		// partido que dibuja no dependa de la hora de la computadora del
		// usuario.
		w.Header().Set("X-Server-Time", strconv.FormatInt(time.Now().UnixMilli(), 10))
		next.ServeHTTP(w, r)
	})
}

// handleHealth es el chequeo de salud de la app: devuelve el estado y el
// nombre de la instancia que atendió la request. Se usa para verificar el
// balanceo de carga (qué réplica respondió detrás de Nginx) y es el health
// check path configurado en Render. Llevar la instancia en el body permite
// identificarla aunque no se inspeccionen las cabeceras de la respuesta.
func handleHealth(instanceName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":   "ok",
			"instance": instanceName,
		})
	}
}

// createNameRequest es el body esperado para crear equipos.
type createNameRequest struct {
	Name string `json:"name"`
}

// createPlayerRequest es el body esperado para crear un jugador asociado a un
// equipo existente.
type createPlayerRequest struct {
	Name   string `json:"name"`
	TeamID int    `json:"team_id"`
}

func handleListPlayers(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		players, err := s.ListPlayers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, players)
	}
}

func handleCreatePlayer(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createPlayerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "body inválido: "+err.Error())
			return
		}
		p, err := s.AddPlayer(r.Context(), req.Name, req.TeamID)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, store.ErrNotFound) {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, p)
	}
}

func handleDeletePlayer(s *store.Store) http.HandlerFunc {
	return handleDelete(s.DeletePlayer)
}

func handleListTeams(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		teams, err := s.ListTeams(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, teams)
	}
}

func handleCreateTeam(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createNameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "body inválido: "+err.Error())
			return
		}
		t, err := s.AddTeam(r.Context(), req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, t)
	}
}

func handleDeleteTeam(s *store.Store) http.HandlerFunc {
	return handleDelete(s.DeleteTeam)
}

func handleListMatches(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		matches, err := s.ListMatches(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, matches)
	}
}

// createMatchRequest es el body esperado para iniciar un partido. Ya no
// lleva resultado: el marcador se arma con los eventos.
type createMatchRequest struct {
	TeamAID int `json:"team_a_id"`
	TeamBID int `json:"team_b_id"`
}

// commandResult es la respuesta de un comando: solo confirma qué se escribió
// y en qué versión quedó el partido. Para ver el estado hay que consultar el
// lado de lectura (GET /matches), que puede tardar unos ms en reflejarlo.
type commandResult struct {
	MatchID int `json:"match_id"`
	Version int `json:"version"`
	Seq     int `json:"seq,omitempty"`
}

func handleCreateMatch(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createMatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "body inválido: "+err.Error())
			return
		}
		m, err := s.StartMatch(r.Context(), req.TeamAID, req.TeamBID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, commandResult{MatchID: m.ID, Version: m.Version})
	}
}

// appendEventRequest es el body para registrar un evento. ExpectedVersion es
// la versión del partido que el cliente tenía al decidir el evento: si el
// partido cambió desde entonces, el servidor responde 409.
//
// El cliente no manda el minuto: lo pone el reloj del servidor. ClockSec es
// opcional y solo sirve para fijar un instante ya transcurrido (el que
// marcaba el reloj cuando el usuario tocó el botón).
type appendEventRequest struct {
	Type            model.EventType `json:"type"`
	TeamID          int             `json:"team_id"`
	PlayerID        int             `json:"player_id"`
	RefSeq          int             `json:"ref_seq"`
	Speed           int             `json:"speed"`
	AdvanceSec      int             `json:"advance_sec"`
	ClockSec        *int            `json:"clock_sec"`
	ExpectedVersion *int            `json:"expected_version"`
}

func handleAppendEvent(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "id inválido: "+err.Error())
			return
		}
		var req appendEventRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "body inválido: "+err.Error())
			return
		}
		expected := -1
		if req.ExpectedVersion != nil {
			expected = *req.ExpectedVersion
		}
		e, err := s.AppendEvent(r.Context(), id, expected, model.Event{
			Type: req.Type, TeamID: req.TeamID, PlayerID: req.PlayerID, RefSeq: req.RefSeq,
			Speed: req.Speed, AdvanceSec: req.AdvanceSec,
		}, req.ClockSec)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, commandResult{MatchID: id, Version: e.Seq, Seq: e.Seq})
	}
}

// handleDeleteMatch cancela el partido (agrega match_cancelled). DELETE es
// idempotente: cancelar algo ya cancelado también responde 204.
func handleDeleteMatch(s *store.Store) http.HandlerFunc {
	return handleDelete(s.CancelMatch)
}

func handleStandings(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.Standings(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

// handleRebuild borra los modelos de lectura y los recalcula desde los
// eventos. Existe para demostrar que los eventos son la fuente de verdad.
func handleRebuild(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := s.Rebuild(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"matches": n})
	}
}

func handleDelete(deleteEntity func(context.Context, int) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "id inválido: "+err.Error())
			return
		}

		if err := deleteEntity(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// writeJSON serializa v como JSON, setea el status code y el
// Content-Type. Centraliza el formato de respuesta de toda la API.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeStoreError traduce un error del store al status HTTP que corresponde.
// Los errores de reglas de negocio (validaciones) son 400.
func writeStoreError(w http.ResponseWriter, err error) {
	var verr *store.VersionError
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "current_version": verr.Current})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// writeError responde con un JSON {"error": msg} y el status dado.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
