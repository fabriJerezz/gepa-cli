package store

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestStore levanta un Redis en memoria (miniredis) y un Store apuntando
// a él. No necesita Docker ni una instancia real de Redis: corre entero en
// el proceso del test, por eso es rápido y sirve para CI.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	mr := miniredis.RunT(t) // RunT registra el cleanup solo: se cierra al terminar el test.

	s, err := Open(context.Background(), mr.Addr())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return s
}

func TestAddPlayer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.AddPlayer(ctx, "Lionel Messi")
	if err != nil {
		t.Fatalf("AddPlayer: %v", err)
	}
	if p.ID != 1 || p.Name != "Lionel Messi" {
		t.Errorf("jugador inesperado: %+v", p)
	}

	// El segundo jugador debe recibir el siguiente ID (contador INCR de Redis).
	p2, err := s.AddPlayer(ctx, "Emiliano Martinez")
	if err != nil {
		t.Fatalf("AddPlayer: %v", err)
	}
	if p2.ID != 2 {
		t.Errorf("esperaba ID 2, obtuve %d", p2.ID)
	}
}

func TestAddPlayerNombreVacio(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.AddPlayer(context.Background(), ""); err == nil {
		t.Fatal("esperaba error con nombre vacío, no hubo ninguno")
	}
}

func TestListPlayersOrdenPorID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"C", "A", "B"} {
		if _, err := s.AddPlayer(ctx, name); err != nil {
			t.Fatalf("AddPlayer(%s): %v", name, err)
		}
	}

	players, err := s.ListPlayers(ctx)
	if err != nil {
		t.Fatalf("ListPlayers: %v", err)
	}
	if len(players) != 3 {
		t.Fatalf("esperaba 3 jugadores, obtuve %d", len(players))
	}
	// HGETALL no garantiza orden; ListPlayers debe ordenar por ID antes de
	// devolver, así que el orden de inserción (C, A, B -> ids 1, 2, 3) debe
	// mantenerse en el resultado.
	want := []string{"C", "A", "B"}
	for i, p := range players {
		if p.Name != want[i] {
			t.Errorf("posición %d: esperaba %q, obtuve %q", i, want[i], p.Name)
		}
	}
}

func TestAddTeamYGetTeam(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	team, err := s.AddTeam(ctx, "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	got, err := s.GetTeam(ctx, team.ID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if got.Name != "Argentina" {
		t.Errorf("esperaba %q, obtuve %q", "Argentina", got.Name)
	}
}

func TestGetTeamInexistente(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetTeam(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("esperaba ErrNotFound, obtuve %v", err)
	}
}

func TestAddMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	teamA, _ := s.AddTeam(ctx, "Argentina")
	teamB, _ := s.AddTeam(ctx, "Francia")

	m, err := s.AddMatch(ctx, teamA.ID, teamB.ID, 3, 3)
	if err != nil {
		t.Fatalf("AddMatch: %v", err)
	}
	if m.TeamAID != teamA.ID || m.TeamBID != teamB.ID || m.ScoreA != 3 || m.ScoreB != 3 {
		t.Errorf("partido inesperado: %+v", m)
	}

	matches, err := s.ListMatches(ctx)
	if err != nil {
		t.Fatalf("ListMatches: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("esperaba 1 partido, obtuve %d", len(matches))
	}
}

func TestAddMatchEquipoInexistente(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	teamA, _ := s.AddTeam(ctx, "Argentina")

	_, err := s.AddMatch(ctx, teamA.ID, 999, 1, 0)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("esperaba ErrNotFound, obtuve %v", err)
	}
}

func TestAddMatchMismoEquipo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	teamA, _ := s.AddTeam(ctx, "Argentina")

	if _, err := s.AddMatch(ctx, teamA.ID, teamA.ID, 1, 0); err == nil {
		t.Fatal("esperaba error por equipo jugando contra sí mismo")
	}
}

func TestAddMatchResultadoNegativo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	teamA, _ := s.AddTeam(ctx, "Argentina")
	teamB, _ := s.AddTeam(ctx, "Francia")

	if _, err := s.AddMatch(ctx, teamA.ID, teamB.ID, -1, 0); err == nil {
		t.Fatal("esperaba error por resultado negativo")
	}
}
