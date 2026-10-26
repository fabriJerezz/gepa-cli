package model

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

// play aplica eventos uno por uno igual que el lado de escritura: Stamp para
// fijar el tiempo, Check para validar y Replay para el estado.
func play(t *testing.T, steps ...func(m Match) (Event, time.Time, *int)) Match {
	t.Helper()
	events := []Event{{Seq: 1, Type: EventMatchStarted, At: t0, TeamAID: 1, TeamBID: 2}}
	for _, step := range steps {
		m := Replay(1, events)
		e, now, atClock := step(m)
		e.Seq = m.Version + 1
		if err := m.Stamp(&e, now, atClock); err != nil {
			t.Fatalf("Stamp(%s): %v", e.Type, err)
		}
		if err := m.Check(e); err != nil {
			t.Fatalf("Check(%s): %v", e.Type, err)
		}
		events = append(events, e)
	}
	return Replay(1, events)
}

func ev(e Event, realSec int) func(Match) (Event, time.Time, *int) {
	return func(Match) (Event, time.Time, *int) { return e, at(realSec), nil }
}

func TestRelojAvanzaManualmente(t *testing.T) {
	m := play(t,
		ev(Event{Type: EventClockAdvanced, AdvanceSec: 5 * 60}, 10),
		ev(Event{Type: EventClockAdvanced, AdvanceSec: 60}, 20),
		ev(Event{Type: EventGoal, TeamID: 1}, 30),
	)
	if m.ClockAt(at(1000)) != 6*60 {
		t.Fatalf("reloj pausado = %d, esperaba 360", m.ClockAt(at(1000)))
	}
	if goal := m.Events[len(m.Events)-1]; goal.Minute != 6 || goal.ClockSec != 360 {
		t.Fatalf("el gol debería quedar en el minuto 6, quedó %+v", goal.Event)
	}
}

func TestRelojAutomaticoCorreMasRapido(t *testing.T) {
	// x60: un segundo real es un minuto de partido.
	m := play(t,
		ev(Event{Type: EventClockStarted, Speed: 60}, 0),
		ev(Event{Type: EventGoal, TeamID: 2}, 17), // minuto 17
		ev(Event{Type: EventClockPaused}, 30),     // minuto 30
	)
	goal := m.Events[2]
	if goal.Minute != 17 {
		t.Fatalf("gol en minuto %d, esperaba 17", goal.Minute)
	}
	if got := m.ClockAt(at(500)); got != 30*60 {
		t.Fatalf("después de pausar el reloj quedó en %d s, esperaba 1800", got)
	}
	if got := m.ClockAt(at(500)); got > RegulationSec {
		t.Fatalf("el reloj pasó el tope: %d", got)
	}
}

func TestRelojNoPasaElTope(t *testing.T) {
	m := play(t, ev(Event{Type: EventClockStarted, Speed: 600}, 0))
	// 0-0: se va al alargue y corta en 120'.
	if got := m.ClockAt(at(3600)); got != ExtraTimeSec {
		t.Fatalf("reloj = %d, esperaba el tope %d", got, ExtraTimeSec)
	}
	// 1-0 antes del 90': corta en 90'.
	m = play(t, ev(Event{Type: EventClockStarted, Speed: 60}, 0), ev(Event{Type: EventGoal, TeamID: 1}, 10))
	if got := m.ClockAt(at(3600)); got != RegulationSec {
		t.Fatalf("reloj = %d, esperaba el tope %d", got, RegulationSec)
	}
	// Gol en el alargue: el tope sigue en 120'.
	m = play(t, ev(Event{Type: EventClockStarted, Speed: 60}, 0), ev(Event{Type: EventGoal, TeamID: 1}, 100))
	if got := m.ClockAt(at(3600)); got != ExtraTimeSec {
		t.Fatalf("reloj = %d, esperaba el tope %d", got, ExtraTimeSec)
	}
}

func TestNoSePuedeAnotarAntesDelUltimoEvento(t *testing.T) {
	m := play(t,
		ev(Event{Type: EventClockAdvanced, AdvanceSec: 30 * 60}, 0),
		ev(Event{Type: EventGoal, TeamID: 1}, 1), // minuto 30
	)

	before := 20 * 60
	e := Event{Type: EventGoal, TeamID: 2}
	if err := m.Stamp(&e, at(2), &before); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("anotar en el 20' después de un gol en el 30' debería fallar, obtuve %v", err)
	}

	future := 40 * 60
	if err := m.Stamp(&e, at(2), &future); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("anotar en el futuro debería fallar, obtuve %v", err)
	}

	now := 30 * 60
	if err := m.Stamp(&e, at(2), &now); err != nil || e.Minute != 30 {
		t.Fatalf("anotar en el mismo instante del último evento es válido: %v, %+v", err, e)
	}
}

func TestRelojSoloAvanza(t *testing.T) {
	m := Replay(1, []Event{{Seq: 1, Type: EventMatchStarted, At: t0, TeamAID: 1, TeamBID: 2}})
	e := Event{Type: EventClockAdvanced, AdvanceSec: -60}
	if err := m.Stamp(&e, at(1), nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("avanzar con un valor negativo debería fallar, obtuve %v", err)
	}
}

func TestExpulsadoNoPuedeTenerMasEventos(t *testing.T) {
	m := play(t, ev(Event{Type: EventRedCard, TeamID: 1, PlayerID: 7}, 1))
	if err := m.Check(Event{Type: EventGoal, TeamID: 1, PlayerID: 7, ClockSec: 0}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("un expulsado no puede hacer goles, obtuve %v", err)
	}
}
