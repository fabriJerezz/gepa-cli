// Package api expone la funcionalidad del store como una API REST en JSON.
// Cada recurso (players, teams, matches) tiene endpoints para listar, crear y
// eliminar; los handlers son delgados a propósito, toda la validación de
// negocio vive en internal/store.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

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

	mux.HandleFunc("GET /matches", handleListMatches(s))
	mux.HandleFunc("POST /matches", handleCreateMatch(s))
	mux.HandleFunc("DELETE /matches/{id}", handleDeleteMatch(s))

	web.Mount(mux)

	return logMiddleware(instanceMiddleware(instanceName, mux))
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

// createMatchRequest es el body esperado para crear un partido: los IDs de
// los equipos que jugaron y el resultado final de cada uno.
type createMatchRequest struct {
	TeamAID int `json:"team_a_id"`
	TeamBID int `json:"team_b_id"`
	ScoreA  int `json:"score_a"`
	ScoreB  int `json:"score_b"`
}

func handleCreateMatch(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createMatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "body inválido: "+err.Error())
			return
		}
		m, err := s.AddMatch(r.Context(), req.TeamAID, req.TeamBID, req.ScoreA, req.ScoreB)
		if err != nil {
			// Si el error es porque un equipo no existe, devolvemos 404 en
			// vez del 400 genérico para que el cliente pueda distinguir
			// "datos mal formados" de "referencia inexistente".
			status := http.StatusBadRequest
			if errors.Is(err, store.ErrNotFound) {
				status = http.StatusNotFound
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, m)
	}
}

func handleDeleteMatch(s *store.Store) http.HandlerFunc {
	return handleDelete(s.DeleteMatch)
}

func handleDelete(deleteEntity func(context.Context, int) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "id inválido: "+err.Error())
			return
		}

		if err := deleteEntity(r.Context(), id); err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, store.ErrNotFound):
				status = http.StatusNotFound
			case errors.Is(err, store.ErrConflict):
				status = http.StatusConflict
			}
			writeError(w, status, err.Error())
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

// writeError responde con un JSON {"error": msg} y el status dado.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
