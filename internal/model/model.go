// Package model define las entidades de dominio de la aplicación: jugadores,
// equipos y partidos. Son simples structs de datos, sin lógica de negocio ni
// dependencias de almacenamiento (eso vive en internal/store).
package model

// Player representa un jugador con nombre y pertenencia a un equipo.
type Player struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	TeamID int    `json:"team_id"`
}

// Team representa un equipo.
type Team struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Match representa un partido jugado entre dos equipos (TeamAID vs TeamBID)
// y el resultado final (ScoreA vs ScoreB).
type Match struct {
	ID      int `json:"id"`
	TeamAID int `json:"team_a_id"`
	TeamBID int `json:"team_b_id"`
	ScoreA  int `json:"score_a"`
	ScoreB  int `json:"score_b"`
}
