// Package store implementa la persistencia de jugadores, equipos y partidos
// usando Redis como base de datos.
//
// Esquema de datos en Redis:
//
//   - "gepa:seq:player" / "gepa:seq:team" / "gepa:seq:match"
//     Contadores atómicos (INCR) usados para generar IDs autoincrementales.
//
//   - "gepa:players" / "gepa:teams" / "gepa:matches"
//     Hashes de Redis donde cada campo es el ID de la entidad (como string)
//     y el valor es el registro serializado en JSON. Usar un hash nos da
//     inserción, lectura por ID y listado completo (HGETALL) en O(1)/O(n)
//     sin tener que mantener índices adicionales.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/redis/go-redis/v9"

	"gepa-cli/internal/model"
)

// ErrNotFound se devuelve cuando una entidad referenciada (p. ej. un equipo
// al crear un partido) no existe en la base de datos.
var ErrNotFound = errors.New("no encontrado")

// ErrConflict se devuelve cuando una operación no puede realizarse por las
// relaciones actuales entre entidades.
var ErrConflict = errors.New("no se puede eliminar")

// Claves (keys) de Redis usadas por el store. Centralizadas acá para evitar
// strings mágicos repetidos en cada método.
const (
	keyPlayers = "gepa:players"
	keyTeams   = "gepa:teams"
	keyMatches = "gepa:matches"

	seqPlayer = "gepa:seq:player"
	seqTeam   = "gepa:seq:team"
	seqMatch  = "gepa:seq:match"
)

// Store administra el acceso a Redis para todas las entidades del dominio.
type Store struct {
	rdb *redis.Client
}

// Open conecta contra Redis en addr (host:puerto) y verifica la conexión con
// un PING antes de devolver el Store, para fallar rápido si Redis no está
// disponible en vez de descubrirlo recién en el primer comando.
func Open(ctx context.Context, addr string) (*Store, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr})

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("conectando a redis en %s: %w", addr, err)
	}

	return &Store{rdb: rdb}, nil
}

// Close cierra la conexión con Redis.
func (s *Store) Close() error {
	return s.rdb.Close()
}

// AddPlayer crea un jugador asociado a un equipo existente, le asigna un ID
// autoincremental y lo guarda en el hash de jugadores.
func (s *Store) AddPlayer(ctx context.Context, name string, teamID int) (model.Player, error) {
	if name == "" {
		return model.Player{}, errors.New("el nombre no puede estar vacío")
	}
	if teamID == 0 {
		return model.Player{}, errors.New("el jugador debe pertenecer a un equipo")
	}

	ok, err := s.teamExists(ctx, teamID)
	if err != nil {
		return model.Player{}, err
	}
	if !ok {
		return model.Player{}, fmt.Errorf("equipo %d: %w", teamID, ErrNotFound)
	}

	id, err := s.rdb.Incr(ctx, seqPlayer).Result()
	if err != nil {
		return model.Player{}, fmt.Errorf("generando id: %w", err)
	}

	p := model.Player{ID: int(id), Name: name, TeamID: teamID}
	if err := s.hsetJSON(ctx, keyPlayers, p.ID, p); err != nil {
		return model.Player{}, err
	}
	return p, nil
}

// ListPlayers devuelve todos los jugadores, ordenados por ID.
func (s *Store) ListPlayers(ctx context.Context) ([]model.Player, error) {
	raw, err := s.rdb.HGetAll(ctx, keyPlayers).Result()
	if err != nil {
		return nil, fmt.Errorf("listando jugadores: %w", err)
	}

	out := make([]model.Player, 0, len(raw))
	for _, v := range raw {
		var p model.Player
		if err := json.Unmarshal([]byte(v), &p); err != nil {
			return nil, fmt.Errorf("decodificando jugador: %w", err)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DeletePlayer elimina un jugador por ID.
func (s *Store) DeletePlayer(ctx context.Context, id int) error {
	deleted, err := s.rdb.HDel(ctx, keyPlayers, strconv.Itoa(id)).Result()
	if err != nil {
		return fmt.Errorf("eliminando jugador %d: %w", id, err)
	}
	if deleted == 0 {
		return fmt.Errorf("jugador %d: %w", id, ErrNotFound)
	}
	return nil
}

// AddTeam crea un equipo con el nombre dado y lo persiste.
func (s *Store) AddTeam(ctx context.Context, name string) (model.Team, error) {
	if name == "" {
		return model.Team{}, errors.New("el nombre no puede estar vacío")
	}

	id, err := s.rdb.Incr(ctx, seqTeam).Result()
	if err != nil {
		return model.Team{}, fmt.Errorf("generando id: %w", err)
	}

	t := model.Team{ID: int(id), Name: name}
	if err := s.hsetJSON(ctx, keyTeams, t.ID, t); err != nil {
		return model.Team{}, err
	}
	return t, nil
}

// ListTeams devuelve todos los equipos, ordenados por ID.
func (s *Store) ListTeams(ctx context.Context) ([]model.Team, error) {
	raw, err := s.rdb.HGetAll(ctx, keyTeams).Result()
	if err != nil {
		return nil, fmt.Errorf("listando equipos: %w", err)
	}

	out := make([]model.Team, 0, len(raw))
	for _, v := range raw {
		var t model.Team
		if err := json.Unmarshal([]byte(v), &t); err != nil {
			return nil, fmt.Errorf("decodificando equipo: %w", err)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetTeam busca un equipo por ID. Devuelve ErrNotFound si no existe.
func (s *Store) GetTeam(ctx context.Context, id int) (model.Team, error) {
	raw, err := s.rdb.HGet(ctx, keyTeams, strconv.Itoa(id)).Result()
	if errors.Is(err, redis.Nil) {
		return model.Team{}, ErrNotFound
	}
	if err != nil {
		return model.Team{}, fmt.Errorf("buscando equipo %d: %w", id, err)
	}

	var t model.Team
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return model.Team{}, fmt.Errorf("decodificando equipo: %w", err)
	}
	return t, nil
}

// DeleteTeam elimina un equipo solo cuando no tiene jugadores ni partidos
// asociados.
func (s *Store) DeleteTeam(ctx context.Context, id int) error {
	if ok, err := s.teamExists(ctx, id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("equipo %d: %w", id, ErrNotFound)
	}

	players, err := s.ListPlayers(ctx)
	if err != nil {
		return err
	}
	for _, player := range players {
		if player.TeamID == id {
			return fmt.Errorf("el equipo tiene jugadores: %w", ErrConflict)
		}
	}

	matches, err := s.ListMatches(ctx)
	if err != nil {
		return err
	}
	for _, match := range matches {
		if match.TeamAID == id || match.TeamBID == id {
			return fmt.Errorf("el equipo tiene partidos: %w", ErrConflict)
		}
	}

	deleted, err := s.rdb.HDel(ctx, keyTeams, strconv.Itoa(id)).Result()
	if err != nil {
		return fmt.Errorf("eliminando equipo %d: %w", id, err)
	}
	if deleted == 0 {
		return fmt.Errorf("equipo %d: %w", id, ErrNotFound)
	}
	return nil
}

// teamExists chequea la existencia de un equipo sin traer/decodificar todo
// el registro (más barato que GetTeam cuando solo nos interesa validar).
func (s *Store) teamExists(ctx context.Context, id int) (bool, error) {
	n, err := s.rdb.HExists(ctx, keyTeams, strconv.Itoa(id)).Result()
	if err != nil {
		return false, fmt.Errorf("verificando equipo %d: %w", id, err)
	}
	return n, nil
}

// AddMatch registra un partido entre dos equipos existentes con su
// resultado. Valida que ambos equipos existan y que el resultado tenga
// sentido antes de persistir.
func (s *Store) AddMatch(ctx context.Context, teamAID, teamBID, scoreA, scoreB int) (model.Match, error) {
	if teamAID == teamBID {
		return model.Match{}, errors.New("un equipo no puede jugar contra sí mismo")
	}
	if scoreA < 0 || scoreB < 0 {
		return model.Match{}, errors.New("el resultado no puede ser negativo")
	}

	okA, err := s.teamExists(ctx, teamAID)
	if err != nil {
		return model.Match{}, err
	}
	if !okA {
		return model.Match{}, fmt.Errorf("equipo %d: %w", teamAID, ErrNotFound)
	}

	okB, err := s.teamExists(ctx, teamBID)
	if err != nil {
		return model.Match{}, err
	}
	if !okB {
		return model.Match{}, fmt.Errorf("equipo %d: %w", teamBID, ErrNotFound)
	}

	id, err := s.rdb.Incr(ctx, seqMatch).Result()
	if err != nil {
		return model.Match{}, fmt.Errorf("generando id: %w", err)
	}

	m := model.Match{
		ID:      int(id),
		TeamAID: teamAID,
		TeamBID: teamBID,
		ScoreA:  scoreA,
		ScoreB:  scoreB,
	}
	if err := s.hsetJSON(ctx, keyMatches, m.ID, m); err != nil {
		return model.Match{}, err
	}
	return m, nil
}

// ListMatches devuelve todos los partidos, ordenados por ID.
func (s *Store) ListMatches(ctx context.Context) ([]model.Match, error) {
	raw, err := s.rdb.HGetAll(ctx, keyMatches).Result()
	if err != nil {
		return nil, fmt.Errorf("listando partidos: %w", err)
	}

	out := make([]model.Match, 0, len(raw))
	for _, v := range raw {
		var m model.Match
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, fmt.Errorf("decodificando partido: %w", err)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DeleteMatch elimina un partido por ID.
func (s *Store) DeleteMatch(ctx context.Context, id int) error {
	deleted, err := s.rdb.HDel(ctx, keyMatches, strconv.Itoa(id)).Result()
	if err != nil {
		return fmt.Errorf("eliminando partido %d: %w", id, err)
	}
	if deleted == 0 {
		return fmt.Errorf("partido %d: %w", id, ErrNotFound)
	}
	return nil
}

// hsetJSON serializa v como JSON y lo guarda en el campo `id` del hash
// `key`. Es el helper genérico que usan todos los Add* de arriba.
func (s *Store) hsetJSON(ctx context.Context, key string, id int, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("codificando: %w", err)
	}
	if err := s.rdb.HSet(ctx, key, strconv.Itoa(id), raw).Err(); err != nil {
		return fmt.Errorf("guardando en redis: %w", err)
	}
	return nil
}
