// Package model define las entidades de dominio de la aplicación: jugadores,
// equipos y partidos. No tiene dependencias de almacenamiento (eso vive en
// internal/store); la única lógica es la de los eventos de partido, que es
// pura y por eso vive acá.
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

// Los partidos viven en match.go: a diferencia de jugadores y equipos (CRUD
// simple), se modelan como una secuencia de eventos.
