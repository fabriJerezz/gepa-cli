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
	team, err := s.AddTeam(ctx, "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	p, err := s.AddPlayer(ctx, "Lionel Messi", team.ID)
	if err != nil {
		t.Fatalf("AddPlayer: %v", err)
	}
	if p.ID != 1 || p.Name != "Lionel Messi" || p.TeamID != team.ID {
		t.Errorf("jugador inesperado: %+v", p)
	}

	// El segundo jugador debe recibir el siguiente ID (contador INCR de Redis).
	p2, err := s.AddPlayer(ctx, "Emiliano Martinez", team.ID)
	if err != nil {
		t.Fatalf("AddPlayer: %v", err)
	}
	if p2.ID != 2 {
		t.Errorf("esperaba ID 2, obtuve %d", p2.ID)
	}
}

func TestAddPlayerNombreVacio(t *testing.T) {
	s := newTestStore(t)
	team, err := s.AddTeam(context.Background(), "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	if _, err := s.AddPlayer(context.Background(), "", team.ID); err == nil {
		t.Fatal("esperaba error con nombre vacío, no hubo ninguno")
	}
}

func TestAddPlayerSinEquipo(t *testing.T) {
	s := newTestStore(t)

	_, err := s.AddPlayer(context.Background(), "Lionel Messi", 0)
	if err == nil || err.Error() != "el jugador debe pertenecer a un equipo" {
		t.Fatalf("esperaba error de pertenencia a equipo, obtuve %v", err)
	}
}

func TestAddPlayerEquipoInexistente(t *testing.T) {
	s := newTestStore(t)

	_, err := s.AddPlayer(context.Background(), "Lionel Messi", 999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("esperaba ErrNotFound, obtuve %v", err)
	}
}

func TestListPlayersOrdenPorID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	team, err := s.AddTeam(ctx, "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	for _, name := range []string{"C", "A", "B"} {
		if _, err := s.AddPlayer(ctx, name, team.ID); err != nil {
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
		if p.TeamID != team.ID {
			t.Errorf("jugador %q: esperaba equipo %d, obtuve %d", p.Name, team.ID, p.TeamID)
		}
	}
}

func TestDeletePlayer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	team, err := s.AddTeam(ctx, "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	player, err := s.AddPlayer(ctx, "Lionel Messi", team.ID)
	if err != nil {
		t.Fatalf("AddPlayer: %v", err)
	}

	if err := s.DeletePlayer(ctx, player.ID); err != nil {
		t.Fatalf("DeletePlayer: %v", err)
	}
	players, err := s.ListPlayers(ctx)
	if err != nil {
		t.Fatalf("ListPlayers: %v", err)
	}
	if len(players) != 0 {
		t.Fatalf("esperaba lista vacía, obtuve %d jugadores", len(players))
	}
	if err := s.DeletePlayer(ctx, player.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("esperaba ErrNotFound al borrar jugador inexistente, obtuve %v", err)
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

func TestDeleteTeamSinRelaciones(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	team, err := s.AddTeam(ctx, "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	if err := s.DeleteTeam(ctx, team.ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if _, err := s.GetTeam(ctx, team.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("esperaba equipo inexistente, obtuve %v", err)
	}
}

func TestDeleteTeamConJugadorYLuegoSinEl(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	team, err := s.AddTeam(ctx, "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	player, err := s.AddPlayer(ctx, "Lionel Messi", team.ID)
	if err != nil {
		t.Fatalf("AddPlayer: %v", err)
	}

	if err := s.DeleteTeam(ctx, team.ID); !errors.Is(err, ErrConflict) || err.Error() != "el equipo tiene jugadores: no se puede eliminar" {
		t.Fatalf("esperaba conflicto por jugadores, obtuve %v", err)
	}
	if _, err := s.GetTeam(ctx, team.ID); err != nil {
		t.Fatalf("el equipo no debería haberse eliminado: %v", err)
	}
	if err := s.DeletePlayer(ctx, player.ID); err != nil {
		t.Fatalf("DeletePlayer: %v", err)
	}
	if err := s.DeleteTeam(ctx, team.ID); err != nil {
		t.Fatalf("DeleteTeam después de borrar jugador: %v", err)
	}
}

func TestDeleteTeamConPartido(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	teamA, err := s.AddTeam(ctx, "Argentina")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	teamB, err := s.AddTeam(ctx, "Francia")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	if _, err := s.AddMatch(ctx, teamA.ID, teamB.ID, 3, 3); err != nil {
		t.Fatalf("AddMatch: %v", err)
	}

	if err := s.DeleteTeam(ctx, teamA.ID); !errors.Is(err, ErrConflict) || err.Error() != "el equipo tiene partidos: no se puede eliminar" {
		t.Fatalf("esperaba conflicto por partidos, obtuve %v", err)
	}
	if _, err := s.GetTeam(ctx, teamA.ID); err != nil {
		t.Fatalf("el equipo no debería haberse eliminado: %v", err)
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

func TestDeleteMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	teamA, _ := s.AddTeam(ctx, "Argentina")
	teamB, _ := s.AddTeam(ctx, "Francia")
	match, err := s.AddMatch(ctx, teamA.ID, teamB.ID, 3, 3)
	if err != nil {
		t.Fatalf("AddMatch: %v", err)
	}

	if err := s.DeleteMatch(ctx, match.ID); err != nil {
		t.Fatalf("DeleteMatch: %v", err)
	}
	matches, err := s.ListMatches(ctx)
	if err != nil {
		t.Fatalf("ListMatches: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("esperaba lista vacía, obtuve %d partidos", len(matches))
	}
	if err := s.DeleteMatch(ctx, match.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("esperaba ErrNotFound al borrar partido inexistente, obtuve %v", err)
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
