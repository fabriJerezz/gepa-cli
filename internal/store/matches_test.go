package store

import (
	"context"
	"errors"
	"testing"

	"gepa-cli/internal/model"
)

// setupMatch crea dos equipos, un jugador del equipo A y un partido en vivo.
func setupMatch(t *testing.T) (*Store, model.Match, model.Player) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	a, _ := s.AddTeam(ctx, "Argentina")
	b, _ := s.AddTeam(ctx, "Francia")
	p, err := s.AddPlayer(ctx, "Messi", a.ID)
	if err != nil {
		t.Fatalf("AddPlayer: %v", err)
	}
	m, err := s.StartMatch(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("StartMatch: %v", err)
	}
	return s, m, p
}

func mustAppend(t *testing.T, s *Store, matchID int, e model.Event) model.Event {
	t.Helper()
	out, err := s.AppendEvent(context.Background(), matchID, -1, e, nil)
	if err != nil {
		t.Fatalf("AppendEvent(%s): %v", e.Type, err)
	}
	return out
}

func standingOf(t *testing.T, s *Store, teamID int) model.Standing {
	t.Helper()
	rows, err := s.Standings(context.Background())
	if err != nil {
		t.Fatalf("Standings: %v", err)
	}
	for _, r := range rows {
		if r.TeamID == teamID {
			return r
		}
	}
	t.Fatalf("equipo %d no está en la tabla", teamID)
	return model.Standing{}
}

func TestPartidoEnVivoHastaLaTabla(t *testing.T) {
	s, m, messi := setupMatch(t)
	ctx := context.Background()

	// El equipo del gol sale del jugador.
	goal := mustAppend(t, s, m.ID, model.Event{Type: model.EventGoal, PlayerID: messi.ID, Minute: 10})
	if goal.TeamID != m.TeamAID {
		t.Fatalf("el gol debería ser del equipo %d, quedó %d", m.TeamAID, goal.TeamID)
	}
	mustAppend(t, s, m.ID, model.Event{Type: model.EventGoal, TeamID: m.TeamBID, Minute: 20})
	mustAppend(t, s, m.ID, model.Event{Type: model.EventGoal, TeamID: m.TeamAID, Minute: 30})
	mustAppend(t, s, m.ID, model.Event{Type: model.EventGoalAnnulled, RefSeq: 4}) // anula el de los 30'
	mustAppend(t, s, m.ID, model.Event{Type: model.EventGoal, TeamID: m.TeamAID, Minute: 80})
	fin := mustAppend(t, s, m.ID, model.Event{Type: model.EventMatchFinished})
	if fin.ScoreA != 2 || fin.ScoreB != 1 {
		t.Fatalf("resultado pasado en limpio = %d-%d, esperaba 2-1", fin.ScoreA, fin.ScoreB)
	}

	// Sin proyectar, el lado de lectura todavía no lo ve (consistencia eventual).
	if got, _ := s.ListMatches(ctx); len(got) != 0 {
		t.Fatalf("el modelo de lectura no debería tener datos antes de proyectar: %+v", got)
	}

	n, err := s.projectBatch(ctx, "test", -1)
	if err != nil || n == 0 {
		t.Fatalf("projectBatch = %d, %v", n, err)
	}
	matches, _ := s.ListMatches(ctx)
	if len(matches) != 1 || matches[0].Status != model.StatusFinished || matches[0].ScoreA != 2 || matches[0].ScoreB != 1 {
		t.Fatalf("partido proyectado inesperado: %+v", matches)
	}

	a := standingOf(t, s, m.TeamAID)
	if a.Played != 1 || a.Won != 1 || a.Points != 3 || a.GoalsFor != 2 || a.GoalsAgainst != 1 || a.GoalDiff != 1 {
		t.Errorf("fila del ganador inesperada: %+v", a)
	}
	b := standingOf(t, s, m.TeamBID)
	if b.Lost != 1 || b.Points != 0 {
		t.Errorf("fila del perdedor inesperada: %+v", b)
	}
}

func TestProyeccionIdempotente(t *testing.T) {
	s, m, _ := setupMatch(t)
	ctx := context.Background()
	mustAppend(t, s, m.ID, model.Event{Type: model.EventGoal, TeamID: m.TeamAID})
	mustAppend(t, s, m.ID, model.Event{Type: model.EventMatchFinished})

	// Simula que el mismo evento se entrega varias veces (o que varias
	// réplicas lo procesan): la tabla no puede sumar dos veces.
	for range 3 {
		if err := s.ProjectMatch(ctx, m.ID); err != nil {
			t.Fatalf("ProjectMatch: %v", err)
		}
	}
	if a := standingOf(t, s, m.TeamAID); a.Played != 1 || a.Points != 3 {
		t.Fatalf("la tabla sumó de más: %+v", a)
	}
}

func TestCancelarPartidoTerminadoDescuentaDeLaTabla(t *testing.T) {
	s, m, _ := setupMatch(t)
	ctx := context.Background()
	mustAppend(t, s, m.ID, model.Event{Type: model.EventMatchFinished})
	_ = s.ProjectMatch(ctx, m.ID)
	if a := standingOf(t, s, m.TeamAID); a.Points != 1 {
		t.Fatalf("empate debería dar 1 punto: %+v", a)
	}

	// Cancelar dos veces es idempotente: sin error y sin restar de más.
	for range 2 {
		if err := s.CancelMatch(ctx, m.ID); err != nil {
			t.Fatalf("CancelMatch: %v", err)
		}
		_ = s.ProjectMatch(ctx, m.ID)
	}
	if a := standingOf(t, s, m.TeamAID); a.Played != 0 || a.Points != 0 {
		t.Fatalf("la tabla debería volver a cero: %+v", a)
	}
	if got, _ := s.ListMatches(ctx); len(got) != 0 {
		t.Fatalf("un partido cancelado no debería listarse: %+v", got)
	}
}

func TestVersionEsperadaDesactualizadaDevuelveConflicto(t *testing.T) {
	s, m, _ := setupMatch(t)
	ctx := context.Background()

	// Dos clientes vieron la versión 1. El primero registra un gol...
	if _, err := s.AppendEvent(ctx, m.ID, 1, model.Event{Type: model.EventGoal, TeamID: m.TeamAID}, nil); err != nil {
		t.Fatalf("primer evento: %v", err)
	}
	// ...y el segundo, con la versión vieja, es rechazado.
	_, err := s.AppendEvent(ctx, m.ID, 1, model.Event{Type: model.EventGoal, TeamID: m.TeamAID}, nil)
	var verr *VersionError
	if !errors.As(err, &verr) || verr.Current != 2 {
		t.Fatalf("esperaba VersionError con versión actual 2, obtuve %v", err)
	}
	if got, _ := s.LoadMatch(ctx, m.ID); got.ScoreA != 1 {
		t.Fatalf("el evento rechazado no debería haberse guardado: %+v", got)
	}
}

func TestReglasDelLadoDeEscritura(t *testing.T) {
	s, m, messi := setupMatch(t)
	ctx := context.Background()

	if _, err := s.StartMatch(ctx, m.TeamAID, m.TeamAID); err == nil {
		t.Error("un equipo no puede jugar contra sí mismo")
	}
	if _, err := s.StartMatch(ctx, m.TeamAID, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("equipo inexistente: esperaba ErrNotFound, obtuve %v", err)
	}
	if _, err := s.AppendEvent(ctx, 999, -1, model.Event{Type: model.EventGoal}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("partido inexistente: esperaba ErrNotFound, obtuve %v", err)
	}
	if _, err := s.AppendEvent(ctx, m.ID, -1, model.Event{Type: model.EventGoal, PlayerID: messi.ID, TeamID: m.TeamBID}, nil); !errors.Is(err, model.ErrInvalidEvent) {
		t.Errorf("jugador de otro equipo: esperaba ErrInvalidEvent, obtuve %v", err)
	}
	if _, err := s.AppendEvent(ctx, m.ID, -1, model.Event{Type: model.EventGoalAnnulled, RefSeq: 1}, nil); !errors.Is(err, model.ErrInvalidEvent) {
		t.Errorf("anular algo que no es gol: esperaba ErrInvalidEvent, obtuve %v", err)
	}
	mustAppend(t, s, m.ID, model.Event{Type: model.EventMatchFinished})
	if _, err := s.AppendEvent(ctx, m.ID, -1, model.Event{Type: model.EventGoal, TeamID: m.TeamAID}, nil); !errors.Is(err, model.ErrInvalidEvent) {
		t.Errorf("gol en partido terminado: esperaba ErrInvalidEvent, obtuve %v", err)
	}
}

func TestRebuildReconstruyeDesdeEventos(t *testing.T) {
	s, m, _ := setupMatch(t)
	ctx := context.Background()
	mustAppend(t, s, m.ID, model.Event{Type: model.EventGoal, TeamID: m.TeamBID})
	mustAppend(t, s, m.ID, model.Event{Type: model.EventMatchFinished})
	_ = s.ProjectMatch(ctx, m.ID)
	before := standingOf(t, s, m.TeamBID)

	// Romper a mano el modelo de lectura y reconstruirlo.
	s.rdb.Del(ctx, keyStandings, keyCounted, keyReadMatches)
	if n, err := s.Rebuild(ctx); err != nil || n != 1 {
		t.Fatalf("Rebuild = %d, %v", n, err)
	}
	if after := standingOf(t, s, m.TeamBID); after != before {
		t.Fatalf("la tabla reconstruida difiere: antes %+v, después %+v", before, after)
	}
}

func TestIdempotencyKeyStore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if rec, err := s.IdemBegin(ctx, "k1", "fp"); err != nil || rec != nil {
		t.Fatalf("primera reserva debería ser nueva: %+v, %v", rec, err)
	}
	if rec, _ := s.IdemBegin(ctx, "k1", "fp"); rec == nil || rec.Status != 0 {
		t.Fatalf("segunda reserva debería verse en curso: %+v", rec)
	}
	_ = s.IdemSave(ctx, "k1", IdemRecord{Fingerprint: "fp", Status: 201, Body: []byte(`{"ok":true}`)})
	if rec, _ := s.IdemBegin(ctx, "k1", "fp"); rec == nil || rec.Status != 201 || string(rec.Body) != `{"ok":true}` {
		t.Fatalf("debería devolver la respuesta guardada: %+v", rec)
	}
	_ = s.IdemRelease(ctx, "k1")
	if rec, _ := s.IdemBegin(ctx, "k1", "fp"); rec != nil {
		t.Fatalf("después de liberar, la key debería poder reservarse de nuevo: %+v", rec)
	}
}

func TestDobleAmarillaEsRoja(t *testing.T) {
	s, m, messi := setupMatch(t)
	ctx := context.Background()

	mustAppend(t, s, m.ID, model.Event{Type: model.EventYellowCard, PlayerID: messi.ID})
	mustAppend(t, s, m.ID, model.Event{Type: model.EventYellowCard, PlayerID: messi.ID})

	got, err := s.LoadMatch(ctx, m.ID)
	if err != nil {
		t.Fatalf("LoadMatch: %v", err)
	}
	last := got.Events[len(got.Events)-1]
	if last.Type != model.EventRedCard || last.PlayerID != messi.ID || got.Version != 4 {
		t.Fatalf("la segunda amarilla debería sumar una roja, último evento %+v (v%d)", last.Event, got.Version)
	}
	if _, err := s.AppendEvent(ctx, m.ID, -1, model.Event{Type: model.EventGoal, PlayerID: messi.ID}, nil); !errors.Is(err, model.ErrInvalidEvent) {
		t.Fatalf("un expulsado no puede hacer goles, obtuve %v", err)
	}
}

func TestUnPartidoEnVivoPorEquipo(t *testing.T) {
	s, m, _ := setupMatch(t)
	ctx := context.Background()
	c, _ := s.AddTeam(ctx, "Brasil")

	if _, err := s.StartMatch(ctx, m.TeamAID, m.TeamBID); !errors.Is(err, ErrTeamBusy) {
		t.Fatalf("mismos equipos en juego: esperaba ErrTeamBusy, obtuve %v", err)
	}
	if _, err := s.StartMatch(ctx, c.ID, m.TeamBID); !errors.Is(err, ErrTeamBusy) {
		t.Fatalf("un equipo en juego: esperaba ErrTeamBusy, obtuve %v", err)
	}

	// Al terminar el partido, los equipos quedan libres.
	mustAppend(t, s, m.ID, model.Event{Type: model.EventMatchFinished})
	if _, err := s.StartMatch(ctx, c.ID, m.TeamBID); err != nil {
		t.Fatalf("con el partido terminado debería poder jugar otro: %v", err)
	}
}
