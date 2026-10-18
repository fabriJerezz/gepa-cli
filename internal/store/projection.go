package store

// Lado de LECTURA de los partidos (la "Q" de CQRS) y el proyector que lo
// mantiene actualizado a partir de los eventos.
//
// Esquema en Redis (modelos de lectura, todos descartables: se pueden
// reconstruir desde los eventos con Rebuild):
//
//   - "gepa:read:matches": hash id -> partido en JSON (marcador, estado,
//     relato). Lo lee GET /matches.
//   - "gepa:read:match_versions": hash id -> versión proyectada. Evita que
//     una proyección vieja pise a una más nueva.
//   - "gepa:read:standings": hash "{equipo}:{campo}" -> número. Es la tabla
//     de posiciones que lee GET /standings.
//   - "gepa:read:counted": set con los partidos ya sumados a la tabla. Es
//     lo que hace idempotente a la proyección de la tabla.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"gepa-cli/internal/model"
)

const (
	keyReadMatches  = "gepa:read:matches"
	keyReadVersions = "gepa:read:match_versions"
	keyStandings    = "gepa:read:standings"
	keyCounted      = "gepa:read:counted"

	projectorGroup = "projector"
)

// saveMatchScript guarda el partido proyectado solo si su versión es más
// nueva que la ya guardada. Hace falta porque varias réplicas proyectan en
// paralelo y una puede terminar después que otra con datos más viejos.
var saveMatchScript = redis.NewScript(`
local current = tonumber(redis.call('HGET', KEYS[2], ARGV[1]) or '0')
if tonumber(ARGV[2]) <= current then return 0 end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[3])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
return 1
`)

// standingsScript suma (signo 1) o resta (signo -1) un partido a la tabla
// UNA sola vez. El SADD/SREM sobre "counted" es el que deduplica: si el
// partido ya estaba sumado, el script no hace nada. Así procesar el mismo
// evento dos veces (algo normal: Redis Streams entrega "al menos una vez")
// no duplica puntos.
//
// ARGV: id del partido, signo, y pares campo/valor a sumar.
var standingsScript = redis.NewScript(`
local changed
if ARGV[2] == '1' then
  changed = redis.call('SADD', KEYS[1], ARGV[1])
else
  changed = redis.call('SREM', KEYS[1], ARGV[1])
end
if changed == 0 then return 0 end
for i = 3, #ARGV, 2 do
  redis.call('HINCRBY', KEYS[2], ARGV[i], tonumber(ARGV[i + 1]) * tonumber(ARGV[2]))
end
return 1
`)

// ProjectMatch actualiza los modelos de lectura de un partido a partir de
// sus eventos. Es idempotente: llamarlo una o diez veces deja el mismo
// resultado, por eso es seguro que lo ejecuten varias réplicas, que se
// repita un mensaje, o que la CLI lo llame por su cuenta.
func (s *Store) ProjectMatch(ctx context.Context, id int) error {
	m, err := s.LoadMatch(ctx, id)
	if err != nil {
		return err
	}

	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("codificando partido: %w", err)
	}
	if err := saveMatchScript.Run(ctx, s.rdb, []string{keyReadMatches, keyReadVersions}, id, m.Version, raw).Err(); err != nil {
		return fmt.Errorf("proyectando partido %d: %w", id, err)
	}

	sign := 0
	switch m.Status {
	case model.StatusFinished:
		sign = 1
	case model.StatusCancelled:
		sign = -1 // si había sumado a la tabla, se descuenta
	}
	if sign == 0 {
		return nil
	}

	args := []any{id, sign}
	for _, d := range m.StandingDeltas() {
		p := strconv.Itoa(d.TeamID) + ":"
		args = append(args,
			p+"played", d.Played, p+"won", d.Won, p+"drawn", d.Drawn, p+"lost", d.Lost,
			p+"goals_for", d.GoalsFor, p+"goals_against", d.GoalsAgainst, p+"points", d.Points)
	}
	if err := standingsScript.Run(ctx, s.rdb, []string{keyCounted, keyStandings}, args...).Err(); err != nil {
		return fmt.Errorf("actualizando tabla: %w", err)
	}
	return nil
}

// RunProjector consume el feed de eventos hasta que ctx se cancele. Cada
// réplica corre uno con su propio nombre dentro del MISMO consumer group:
// Redis le entrega cada mensaje a una sola de ellas, así el trabajo se
// reparte sin que dos réplicas procesen lo mismo (salvo reintentos, que la
// proyección idempotente absorbe).
func (s *Store) RunProjector(ctx context.Context, consumer string) {
	for ctx.Err() == nil {
		if _, err := s.projectBatch(ctx, consumer, 5*time.Second); err != nil && ctx.Err() == nil {
			log.Printf("proyector: %v", err)
			time.Sleep(time.Second)
		}
	}
}

// projectBatch procesa un lote del feed. block < 0 no espera mensajes
// nuevos (lo usan los tests).
func (s *Store) projectBatch(ctx context.Context, consumer string, block time.Duration) (int, error) {
	// "0" hace que un grupo nuevo arranque desde el principio del feed: como
	// la proyección es idempotente, reprocesar historia vieja no rompe nada.
	err := s.rdb.XGroupCreateMkStream(ctx, keyEventFeed, projectorGroup, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return 0, fmt.Errorf("creando consumer group: %w", err)
	}

	// Mensajes que otra réplica recibió pero nunca confirmó (se cayó a la
	// mitad): pasados 30s, los toma esta.
	claimed, _, err := s.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: keyEventFeed, Group: projectorGroup, Consumer: consumer,
		MinIdle: 30 * time.Second, Start: "0-0", Count: 50,
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("reclamando pendientes: %w", err)
	}

	msgs := claimed
	streams, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: projectorGroup, Consumer: consumer,
		Streams: []string{keyEventFeed, ">"}, Count: 50, Block: block,
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, fmt.Errorf("leyendo feed: %w", err)
	}
	for _, st := range streams {
		msgs = append(msgs, st.Messages...)
	}

	for _, msg := range msgs {
		id, _ := strconv.Atoi(fmt.Sprint(msg.Values["match_id"]))
		if err := s.ProjectMatch(ctx, id); err != nil {
			// Sin XACK: el mensaje queda pendiente y se reintenta.
			return 0, err
		}
		if err := s.rdb.XAck(ctx, keyEventFeed, projectorGroup, msg.ID).Err(); err != nil {
			return 0, fmt.Errorf("confirmando mensaje: %w", err)
		}
	}
	return len(msgs), nil
}

// Rebuild borra todos los modelos de lectura y los vuelve a armar desde los
// eventos. Demuestra que los eventos son la fuente de verdad: la tabla de
// posiciones es solo una vista derivada. Devuelve cuántos partidos proyectó.
func (s *Store) Rebuild(ctx context.Context) (int, error) {
	if err := s.rdb.Del(ctx, keyReadMatches, keyReadVersions, keyStandings, keyCounted).Err(); err != nil {
		return 0, fmt.Errorf("borrando modelos de lectura: %w", err)
	}
	ids, err := s.matchIDs(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.ProjectMatch(ctx, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// ListMatches devuelve los partidos no cancelados desde el modelo de
// lectura, ordenados por ID. Puede ir unos milisegundos atrasado respecto de
// los eventos (consistencia eventual).
func (s *Store) ListMatches(ctx context.Context) ([]model.Match, error) {
	raw, err := s.rdb.HGetAll(ctx, keyReadMatches).Result()
	if err != nil {
		return nil, fmt.Errorf("listando partidos: %w", err)
	}

	out := make([]model.Match, 0, len(raw))
	for _, v := range raw {
		var m model.Match
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, fmt.Errorf("decodificando partido: %w", err)
		}
		if m.Status != model.StatusCancelled {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Standings devuelve la tabla de posiciones con todos los equipos (los que
// no jugaron aparecen en cero), ordenada por puntos, diferencia de gol y
// goles a favor.
func (s *Store) Standings(ctx context.Context) ([]model.Standing, error) {
	teams, err := s.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := s.rdb.HGetAll(ctx, keyStandings).Result()
	if err != nil {
		return nil, fmt.Errorf("leyendo tabla: %w", err)
	}
	get := func(team int, field string) int {
		n, _ := strconv.Atoi(raw[strconv.Itoa(team)+":"+field])
		return n
	}

	out := make([]model.Standing, 0, len(teams))
	for _, t := range teams {
		row := model.Standing{
			TeamID: t.ID, TeamName: t.Name,
			Played: get(t.ID, "played"), Won: get(t.ID, "won"), Drawn: get(t.ID, "drawn"), Lost: get(t.ID, "lost"),
			GoalsFor: get(t.ID, "goals_for"), GoalsAgainst: get(t.ID, "goals_against"), Points: get(t.ID, "points"),
		}
		row.GoalDiff = row.GoalsFor - row.GoalsAgainst
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		if a.GoalDiff != b.GoalDiff {
			return a.GoalDiff > b.GoalDiff
		}
		return a.GoalsFor > b.GoalsFor
	})
	return out, nil
}
