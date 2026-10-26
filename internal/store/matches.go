package store

// Lado de ESCRITURA de los partidos (la "C" de CQRS): comandos que validan
// y agregan eventos. Nunca escriben en los modelos de lectura; de eso se
// encarga el proyector (projection.go).
//
// Esquema en Redis:
//
//   - "gepa:match:{id}:events": stream con los eventos de UN partido, en
//     orden. Es la fuente de verdad (event store). Su largo es la versión
//     del partido.
//   - "gepa:events": stream global, un aviso por cada evento agregado. Es
//     la cola que consume el proyector.
//   - "gepa:match:ids": set con los IDs de todos los partidos (para el
//     rebuild y para chequear relaciones al borrar un equipo).
//   - "gepa:team:{id}:match": último partido que empezó el equipo. Es el
//     candado de "un partido en vivo por equipo" (ver claimTeams).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"gepa-cli/internal/model"
)

const (
	keyMatchIDs  = "gepa:match:ids"
	keyEventFeed = "gepa:events"
	seqMatch     = "gepa:seq:match"
)

func matchStreamKey(id int) string { return fmt.Sprintf("gepa:match:%d:events", id) }

func teamMatchKey(id int) string { return fmt.Sprintf("gepa:team:%d:match", id) }

// ErrTeamBusy indica que un equipo ya está jugando otro partido.
var ErrTeamBusy = errors.New("el equipo ya está jugando un partido")

// ErrVersionConflict indica que el partido cambió desde la versión que el
// cliente esperaba (control de concurrencia optimista).
var ErrVersionConflict = errors.New("el partido cambió mientras tanto")

// VersionError lleva la versión real del partido para que el cliente pueda
// refrescar y reintentar.
type VersionError struct {
	Expected, Current int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("%v: esperabas la versión %d y está en la %d", ErrVersionConflict, e.Expected, e.Current)
}

func (e *VersionError) Unwrap() error { return ErrVersionConflict }

// appendScript agrega un evento SOLO si el partido sigue en la versión
// esperada. Chequeo y escritura van en un único script Lua porque Redis
// ejecuta cada script de forma atómica: así se evita el check-then-act
// (dos réplicas leyendo la versión 3 y escribiendo las dos la 4).
//
// KEYS: stream del partido, feed global, set de IDs.
// ARGV: versión esperada, ID del partido, uno o más eventos en JSON.
var appendScript = redis.NewScript(`
local current = redis.call('XLEN', KEYS[1])
if current ~= tonumber(ARGV[1]) then
  return redis.error_reply('VERSION ' .. current)
end
for i = 3, #ARGV do
  redis.call('XADD', KEYS[1], '*', 'event', ARGV[i])
end
redis.call('XADD', KEYS[2], '*', 'match_id', ARGV[2])
redis.call('SADD', KEYS[3], ARGV[2])
return current + #ARGV - 2
`)

// StartMatch crea un partido entre dos equipos existentes. Arranca en vivo
// y 0-0: el resultado se construye con los eventos que se vayan agregando.
func (s *Store) StartMatch(ctx context.Context, teamAID, teamBID int) (model.Match, error) {
	for _, id := range []int{teamAID, teamBID} {
		ok, err := s.teamExists(ctx, id)
		if err != nil {
			return model.Match{}, err
		}
		if !ok {
			return model.Match{}, fmt.Errorf("equipo %d: %w", id, ErrNotFound)
		}
	}

	e := model.Event{Seq: 1, Type: model.EventMatchStarted, At: time.Now().UTC(), TeamAID: teamAID, TeamBID: teamBID}
	if err := (model.Match{}).Check(e); err != nil {
		return model.Match{}, err
	}

	id, err := s.rdb.Incr(ctx, seqMatch).Result()
	if err != nil {
		return model.Match{}, fmt.Errorf("generando id: %w", err)
	}
	if err := s.claimTeams(ctx, int(id), teamAID, teamBID); err != nil {
		return model.Match{}, err
	}
	if err := s.appendEvent(ctx, int(id), 0, e); err != nil {
		return model.Match{}, err
	}
	return model.Replay(int(id), []model.Event{e}), nil
}

// claimTeamsScript anota el partido nuevo en el candado de los dos equipos
// SOLO si cada candado sigue con el valor que se leyó (compare-and-set).
// Si otra réplica arrancó un partido en el medio, devuelve los valores
// actuales para volver a evaluarlos.
//
// KEYS: candado del equipo A, candado del equipo B.
// ARGV: valor esperado de A, valor esperado de B, ID del partido nuevo.
var claimTeamsScript = redis.NewScript(`
local a = redis.call('GET', KEYS[1]) or ''
local b = redis.call('GET', KEYS[2]) or ''
if a ~= ARGV[1] or b ~= ARGV[2] then
  return {a, b}
end
redis.call('SET', KEYS[1], ARGV[3])
redis.call('SET', KEYS[2], ARGV[3])
return 'OK'
`)

// claimTeams reserva los dos equipos para el partido matchID. El candado de
// cada equipo apunta a su último partido: si ese partido ya terminó, se
// canceló o nunca llegó a guardarse, el candado está libre y se pisa. Así
// no hace falta liberarlo al terminar (y un candado huérfano no traba al
// equipo para siempre).
func (s *Store) claimTeams(ctx context.Context, matchID int, teams ...int) error {
	keys := []string{teamMatchKey(teams[0]), teamMatchKey(teams[1])}
	expected := []any{"", ""}
	for range 3 {
		res, err := claimTeamsScript.Run(ctx, s.rdb, keys, expected[0], expected[1], matchID).Result()
		if err != nil {
			return fmt.Errorf("reservando equipos: %w", err)
		}
		current, ok := res.([]any)
		if !ok {
			return nil // "OK"
		}
		for i, raw := range current {
			v, _ := raw.(string)
			expected[i] = v
			if v == "" {
				continue
			}
			other, _ := strconv.Atoi(v)
			m, err := s.LoadMatch(ctx, other)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if err == nil && m.Status == model.StatusLive {
				return fmt.Errorf("equipo %d: %w (partido #%d)", teams[i], ErrTeamBusy, other)
			}
		}
	}
	return fmt.Errorf("reservando equipos: demasiados conflictos, reintentá")
}

// AppendEvent agrega un evento a un partido en juego. Si expectedVersion es
// >= 0 y no coincide con la versión actual, devuelve *VersionError sin
// escribir nada. Con -1 no se chequea la versión del cliente (igual queda
// protegido contra escrituras simultáneas por appendScript).
//
// El tiempo de partido del evento lo fija el servidor con su propio reloj
// (model.Stamp): atClock solo permite indicar un instante ya transcurrido,
// nunca anterior al último evento ni posterior al reloj actual.
func (s *Store) AppendEvent(ctx context.Context, matchID, expectedVersion int, e model.Event, atClock *int) (model.Event, error) {
	m, err := s.LoadMatch(ctx, matchID)
	if err != nil {
		return model.Event{}, err
	}
	if expectedVersion >= 0 && expectedVersion != m.Version {
		return model.Event{}, &VersionError{Expected: expectedVersion, Current: m.Version}
	}

	// Si viene un jugador, el equipo sale de él (y si vienen los dos,
	// tienen que coincidir).
	if e.PlayerID != 0 {
		p, err := s.getPlayer(ctx, e.PlayerID)
		if err != nil {
			return model.Event{}, err
		}
		if e.TeamID != 0 && e.TeamID != p.TeamID {
			return model.Event{}, fmt.Errorf("%w: el jugador %d no es del equipo %d", model.ErrInvalidEvent, p.ID, e.TeamID)
		}
		e.TeamID = p.TeamID
	}

	e.Seq = m.Version + 1
	if err := m.Stamp(&e, time.Now().UTC(), atClock); err != nil {
		return model.Event{}, err
	}
	e.TeamAID, e.TeamBID, e.ScoreA, e.ScoreB = 0, 0, 0, 0
	if e.Type == model.EventMatchFinished {
		// "Pasar en limpio": el resultado final queda escrito en el propio
		// evento de cierre, calculado acá y no enviado por el cliente.
		e.ScoreA, e.ScoreB = m.ScoreA, m.ScoreB
	}
	if err := m.Check(e); err != nil {
		return model.Event{}, err
	}

	events := []model.Event{e}
	if e.Type == model.EventYellowCard && e.PlayerID != 0 && m.YellowCards(e.PlayerID) == 1 {
		// Doble amarilla: la roja se escribe en el mismo instante y en la
		// misma operación atómica, así nunca queda una sin la otra.
		red := e
		red.Seq, red.Type = e.Seq+1, model.EventRedCard
		events = append(events, red)
	}

	if err := s.appendEvent(ctx, matchID, m.Version, events...); err != nil {
		return model.Event{}, err
	}
	// El último evento escrito: su Seq es la versión nueva del partido.
	return events[len(events)-1], nil
}

// CancelMatch reemplaza al borrado: en Event Sourcing no se borra la
// historia, se agrega un evento que la compensa. Es idempotente: cancelar
// un partido ya cancelado no hace nada y no es error.
func (s *Store) CancelMatch(ctx context.Context, id int) error {
	for range 3 {
		m, err := s.LoadMatch(ctx, id)
		if err != nil {
			return err
		}
		if m.Status == model.StatusCancelled {
			return nil
		}
		_, err = s.AppendEvent(ctx, id, m.Version, model.Event{Type: model.EventMatchCancelled}, nil)
		if !errors.Is(err, ErrVersionConflict) {
			return err
		}
		// Otro evento entró justo en el medio: releer y reintentar.
	}
	return fmt.Errorf("partido %d: demasiados conflictos de versión", id)
}

// LoadMatch reconstruye el estado actual de un partido leyendo todos sus
// eventos del event store.
func (s *Store) LoadMatch(ctx context.Context, id int) (model.Match, error) {
	events, err := s.loadEvents(ctx, id)
	if err != nil {
		return model.Match{}, err
	}
	if len(events) == 0 {
		return model.Match{}, fmt.Errorf("partido %d: %w", id, ErrNotFound)
	}
	return model.Replay(id, events), nil
}

func (s *Store) loadEvents(ctx context.Context, id int) ([]model.Event, error) {
	msgs, err := s.rdb.XRange(ctx, matchStreamKey(id), "-", "+").Result()
	if err != nil {
		return nil, fmt.Errorf("leyendo eventos del partido %d: %w", id, err)
	}
	events := make([]model.Event, 0, len(msgs))
	for _, msg := range msgs {
		raw, _ := msg.Values["event"].(string)
		var e model.Event
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, fmt.Errorf("decodificando evento: %w", err)
		}
		events = append(events, e)
	}
	return events, nil
}

func (s *Store) appendEvent(ctx context.Context, matchID, expectedVersion int, events ...model.Event) error {
	args := []any{expectedVersion, matchID}
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("codificando evento: %w", err)
		}
		args = append(args, raw)
	}
	keys := []string{matchStreamKey(matchID), keyEventFeed, keyMatchIDs}
	err := appendScript.Run(ctx, s.rdb, keys, args...).Err()
	if err != nil && strings.HasPrefix(err.Error(), "VERSION ") {
		current, _ := strconv.Atoi(strings.TrimPrefix(err.Error(), "VERSION "))
		return &VersionError{Expected: expectedVersion, Current: current}
	}
	if err != nil {
		return fmt.Errorf("guardando evento: %w", err)
	}
	return nil
}

// matchIDs devuelve los IDs de todos los partidos registrados.
func (s *Store) matchIDs(ctx context.Context) ([]int, error) {
	raw, err := s.rdb.SMembers(ctx, keyMatchIDs).Result()
	if err != nil {
		return nil, fmt.Errorf("listando partidos: %w", err)
	}
	ids := make([]int, 0, len(raw))
	for _, r := range raw {
		id, err := strconv.Atoi(r)
		if err != nil {
			return nil, fmt.Errorf("id de partido inválido %q: %w", r, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// teamHasMatches dice si el equipo juega algún partido no cancelado.
//
// ponytail: recorre los eventos de todos los partidos (O(eventos totales));
// alcanza para esta escala, un índice equipo->partidos si crece.
func (s *Store) teamHasMatches(ctx context.Context, teamID int) (bool, error) {
	ids, err := s.matchIDs(ctx)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		m, err := s.LoadMatch(ctx, id)
		if err != nil {
			return false, err
		}
		if m.Status != model.StatusCancelled && (m.TeamAID == teamID || m.TeamBID == teamID) {
			return true, nil
		}
	}
	return false, nil
}
