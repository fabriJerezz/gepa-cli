package model

import (
	"errors"
	"fmt"
	"time"
)

// EventType identifica qué ocurrió en un partido. Los eventos son la fuente
// de verdad (Event Sourcing): el estado de un partido nunca se guarda
// directamente, se calcula aplicando sus eventos en orden (ver Replay).
type EventType string

const (
	EventMatchStarted   EventType = "match_started"
	EventGoal           EventType = "goal"
	EventYellowCard     EventType = "yellow_card"
	EventRedCard        EventType = "red_card"
	EventGoalAnnulled   EventType = "goal_annulled"
	EventMatchFinished  EventType = "match_finished"
	EventMatchCancelled EventType = "match_cancelled"
	// Reloj del partido: también son eventos, así todas las réplicas y
	// pestañas calculan el mismo minuto.
	EventClockStarted  EventType = "clock_started"
	EventClockPaused   EventType = "clock_paused"
	EventClockAdvanced EventType = "clock_advanced"
)

// Duración del partido: 90 minutos, o 120 si a los 90 van empatados.
const (
	RegulationSec = 90 * 60
	ExtraTimeSec  = 120 * 60
)

// MatchStatus es el estado del ciclo de vida de un partido.
type MatchStatus string

const (
	StatusLive      MatchStatus = "live"
	StatusFinished  MatchStatus = "finished"
	StatusCancelled MatchStatus = "cancelled"
)

// Event es un hecho inmutable ocurrido en un partido. Seq es su posición
// dentro del partido (1, 2, 3...) y coincide con la versión del partido
// después de aplicarlo.
type Event struct {
	Seq  int       `json:"seq"`
	Type EventType `json:"type"`
	// At es la hora real (del servidor) en que se registró el evento.
	At time.Time `json:"at"`
	// ClockSec es el tiempo de partido, en segundos, en que ocurrió el
	// evento. Lo fija el servidor; Minute es el mismo dato redondeado.
	ClockSec int `json:"clock_sec,omitempty"`
	Minute   int `json:"minute,omitempty"`
	// Speed: segundos de partido por segundo real (solo clock_started).
	Speed int `json:"speed,omitempty"`
	// AdvanceSec: cuánto se adelantó el reloj (solo clock_advanced).
	AdvanceSec int `json:"advance_sec,omitempty"`
	TeamID     int `json:"team_id,omitempty"`
	PlayerID   int `json:"player_id,omitempty"`
	// RefSeq apunta al gol que anula un goal_annulled.
	RefSeq int `json:"ref_seq,omitempty"`
	// TeamAID/TeamBID solo vienen en match_started.
	TeamAID int `json:"team_a_id,omitempty"`
	TeamBID int `json:"team_b_id,omitempty"`
	// ScoreA/ScoreB solo vienen en match_finished: es el resultado "pasado
	// en limpio" al cerrar el partido, calculado por el servidor.
	ScoreA int `json:"score_a,omitempty"`
	ScoreB int `json:"score_b,omitempty"`
}

// TimelineEvent es un evento tal como se muestra en el relato del partido:
// igual al evento original, más la marca de si fue anulado después.
type TimelineEvent struct {
	Event
	Annulled bool `json:"annulled,omitempty"`
}

// Match es el estado de un partido calculado a partir de sus eventos. Es lo
// que guarda el modelo de lectura y lo que devuelve GET /matches.
type Match struct {
	ID      int             `json:"id"`
	TeamAID int             `json:"team_a_id"`
	TeamBID int             `json:"team_b_id"`
	ScoreA  int             `json:"score_a"`
	ScoreB  int             `json:"score_b"`
	Status  MatchStatus     `json:"status"`
	Version int             `json:"version"`
	Events  []TimelineEvent `json:"events"`

	// Estado del reloj. Si ClockRunning, el tiempo actual es
	// ClockSec + (ahora - ClockSince) * ClockSpeed (ver ClockAt). Así el
	// modelo de lectura no necesita actualizarse cada segundo: guarda solo
	// desde cuándo y a qué velocidad corre.
	ClockSec     int       `json:"clock_sec"`
	ClockRunning bool      `json:"clock_running"`
	ClockSpeed   int       `json:"clock_speed,omitempty"`
	ClockSince   time.Time `json:"clock_since"`
	// LastEventSec es el tiempo de partido del último evento: nada puede
	// anotarse antes de este instante.
	LastEventSec int `json:"last_event_sec"`
}

// ClockAt devuelve el tiempo de partido (en segundos) en el instante real t.
func (m Match) ClockAt(t time.Time) int {
	sec := m.ClockSec
	if m.ClockRunning && t.After(m.ClockSince) {
		sec += int(t.Sub(m.ClockSince).Seconds() * float64(m.ClockSpeed))
	}
	return min(sec, m.LimitSec())
}

// LimitSec es el tope del reloj: 120' si va empatado o si ya se pasó del
// 90' (el alargue se juega entero aunque se desempate), 90' si no.
// ClockSec es el último instante fijado por un evento, así que un gol en el
// alargue no puede "bajar" el tope a 90'.
func (m Match) LimitSec() int {
	if m.ScoreA == m.ScoreB || m.ClockSec > RegulationSec {
		return ExtraTimeSec
	}
	return RegulationSec
}

// YellowCards cuenta las amarillas de un jugador en el partido.
func (m Match) YellowCards(playerID int) int {
	n := 0
	for _, e := range m.Events {
		if e.Type == EventYellowCard && e.PlayerID == playerID {
			n++
		}
	}
	return n
}

// SentOff dice si el jugador ya fue expulsado.
func (m Match) SentOff(playerID int) bool {
	for _, e := range m.Events {
		if e.Type == EventRedCard && e.PlayerID == playerID {
			return true
		}
	}
	return false
}

// Stamp fija el tiempo de partido de un evento nuevo registrado en el
// instante real now. Si atClock es nil, el evento ocurre "ahora". Si viene
// (el cliente capturó el reloj al tocar el botón), tiene que estar entre el
// último evento y el reloj actual: no se puede anotar hacia atrás ni en el
// futuro.
func (m Match) Stamp(e *Event, now time.Time, atClock *int) error {
	clock := m.ClockAt(now)
	e.At = now
	e.ClockSec = clock
	switch {
	case e.Type == EventClockAdvanced:
		if e.AdvanceSec <= 0 {
			return fmt.Errorf("%w: el reloj solo puede avanzar", ErrInvalidEvent)
		}
		if clock >= m.LimitSec() {
			return fmt.Errorf("%w: el reloj ya llegó al tope de %d minutos", ErrInvalidEvent, m.LimitSec()/60)
		}
		e.ClockSec = min(clock+e.AdvanceSec, m.LimitSec())
	case atClock != nil:
		if *atClock < m.LastEventSec {
			return fmt.Errorf("%w: no se puede anotar antes del último evento (%s)", ErrInvalidEvent, FormatClock(m.LastEventSec))
		}
		if *atClock > clock {
			return fmt.Errorf("%w: no se puede anotar en el futuro (el reloj va por %s)", ErrInvalidEvent, FormatClock(clock))
		}
		e.ClockSec = *atClock
	}
	e.Minute = e.ClockSec / 60
	return nil
}

// FormatClock muestra segundos de partido como mm:ss.
func FormatClock(sec int) string {
	return fmt.Sprintf("%02d:%02d", sec/60, sec%60)
}

// ErrInvalidEvent agrupa los errores de reglas de negocio de un evento.
var ErrInvalidEvent = errors.New("evento inválido")

// Replay reconstruye un partido aplicando sus eventos en orden. Es una
// función pura: los mismos eventos siempre dan el mismo estado, por eso la
// usan tanto el lado de escritura (para validar) como el proyector (para
// armar el modelo de lectura).
func Replay(id int, events []Event) Match {
	m := Match{ID: id, Events: []TimelineEvent{}}
	for _, e := range events {
		m.apply(e)
	}
	return m
}

func (m *Match) apply(e Event) {
	m.Version = e.Seq
	// Cada evento "rebasa" el reloj a su instante: el tiempo corrido hasta
	// acá queda fijo en ClockSec y, si sigue corriendo, se cuenta desde e.At.
	if m.Version > 1 {
		m.ClockSec, m.ClockSince = m.ClockAt(e.At), e.At
	}
	m.LastEventSec = max(m.LastEventSec, e.ClockSec)

	switch e.Type {
	case EventMatchStarted:
		m.TeamAID, m.TeamBID, m.Status = e.TeamAID, e.TeamBID, StatusLive
		m.ClockSince = e.At
	case EventClockStarted:
		m.ClockRunning, m.ClockSpeed = true, e.Speed
	case EventClockPaused:
		m.ClockRunning = false
	case EventClockAdvanced:
		m.ClockSec = e.ClockSec
	case EventGoal:
		m.addGoal(e.TeamID, 1)
	case EventGoalAnnulled:
		goal := &m.Events[e.RefSeq-1]
		goal.Annulled = true
		m.addGoal(goal.TeamID, -1)
	case EventMatchFinished:
		m.Status, m.ClockRunning = StatusFinished, false
	case EventMatchCancelled:
		m.Status, m.ClockRunning = StatusCancelled, false
	}
	m.Events = append(m.Events, TimelineEvent{Event: e})
}

func (m *Match) addGoal(teamID, n int) {
	if teamID == m.TeamAID {
		m.ScoreA += n
	} else {
		m.ScoreB += n
	}
}

// Check valida si el evento e puede aplicarse sobre el estado actual. Es la
// regla de negocio del lado de escritura: un evento que no pasa Check nunca
// llega a guardarse.
func (m Match) Check(e Event) error {
	if m.Version == 0 {
		if e.Type != EventMatchStarted {
			return fmt.Errorf("%w: el partido no empezó", ErrInvalidEvent)
		}
		if e.TeamAID == e.TeamBID {
			return fmt.Errorf("%w: un equipo no puede jugar contra sí mismo", ErrInvalidEvent)
		}
		return nil
	}
	// El tiempo de partido nunca retrocede (Stamp ya lo garantiza; esto
	// protege contra cualquier otro camino de escritura).
	if e.ClockSec < m.LastEventSec || e.ClockSec > m.LimitSec() {
		return fmt.Errorf("%w: el evento quedaría antes del último (%s)", ErrInvalidEvent, FormatClock(m.LastEventSec))
	}

	switch e.Type {
	case EventMatchStarted:
		return fmt.Errorf("%w: el partido ya empezó", ErrInvalidEvent)
	case EventMatchCancelled:
		if m.Status == StatusCancelled {
			return fmt.Errorf("%w: el partido ya está cancelado", ErrInvalidEvent)
		}
		return nil
	}

	if m.Status != StatusLive {
		return fmt.Errorf("%w: el partido no está en juego", ErrInvalidEvent)
	}

	switch e.Type {
	case EventGoal, EventYellowCard, EventRedCard:
		if e.TeamID != m.TeamAID && e.TeamID != m.TeamBID {
			return fmt.Errorf("%w: el equipo %d no juega este partido", ErrInvalidEvent, e.TeamID)
		}
		if e.PlayerID != 0 && m.SentOff(e.PlayerID) {
			return fmt.Errorf("%w: el jugador %d fue expulsado", ErrInvalidEvent, e.PlayerID)
		}
	case EventGoalAnnulled:
		if e.RefSeq < 1 || e.RefSeq > len(m.Events) || m.Events[e.RefSeq-1].Type != EventGoal {
			return fmt.Errorf("%w: el evento %d no es un gol", ErrInvalidEvent, e.RefSeq)
		}
		if m.Events[e.RefSeq-1].Annulled {
			return fmt.Errorf("%w: el gol %d ya fue anulado", ErrInvalidEvent, e.RefSeq)
		}
	case EventClockStarted:
		if m.ClockRunning {
			return fmt.Errorf("%w: el reloj ya está corriendo", ErrInvalidEvent)
		}
		if e.Speed < 1 || e.Speed > 600 {
			return fmt.Errorf("%w: velocidad fuera de rango (1-600)", ErrInvalidEvent)
		}
	case EventClockPaused:
		if !m.ClockRunning {
			return fmt.Errorf("%w: el reloj no está corriendo", ErrInvalidEvent)
		}
	case EventMatchFinished, EventClockAdvanced:
	default:
		return fmt.Errorf("%w: tipo desconocido %q", ErrInvalidEvent, e.Type)
	}
	return nil
}

// Standing es una fila de la tabla de posiciones.
type Standing struct {
	TeamID       int    `json:"team_id"`
	TeamName     string `json:"team_name"`
	Played       int    `json:"played"`
	Won          int    `json:"won"`
	Drawn        int    `json:"drawn"`
	Lost         int    `json:"lost"`
	GoalsFor     int    `json:"goals_for"`
	GoalsAgainst int    `json:"goals_against"`
	GoalDiff     int    `json:"goal_diff"`
	Points       int    `json:"points"`
}

// StandingDeltas devuelve cuánto suma un partido terminado a la fila de
// cada equipo (3 puntos por victoria, 1 por empate).
func (m Match) StandingDeltas() []Standing {
	row := func(team, gf, gc int) Standing {
		s := Standing{TeamID: team, Played: 1, GoalsFor: gf, GoalsAgainst: gc}
		switch {
		case gf > gc:
			s.Won, s.Points = 1, 3
		case gf == gc:
			s.Drawn, s.Points = 1, 1
		default:
			s.Lost = 1
		}
		return s
	}
	return []Standing{row(m.TeamAID, m.ScoreA, m.ScoreB), row(m.TeamBID, m.ScoreB, m.ScoreA)}
}
