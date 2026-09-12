// Command gepa-cli es una CLI simple para administrar jugadores, equipos y
// partidos, y también expone esa misma funcionalidad como una API REST.
// Los datos se guardan en Redis (ver internal/store), así que tanto la CLI
// como la API leen y escriben sobre la misma base de datos.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"gepa-cli/internal/api"
	"gepa-cli/internal/store"
)

// defaultRedisAddr se usa cuando no se pasa --redis-addr ni se define la
// variable de entorno REDIS_ADDR. Apunta al Redis local por defecto; dentro
// de docker-compose se sobreescribe con el hostname del servicio "redis".
const defaultRedisAddr = "localhost:6379"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run es el punto de entrada testeable de la CLI: recibe los argumentos ya
// sin el nombre del binario y devuelve un error en vez de llamar os.Exit
// directamente.
func run(ctx context.Context, args []string) error {
	redisAddr, args := extractRedisAddrFlag(args)

	if len(args) == 0 {
		printUsage()
		return nil
	}

	// help/usage no necesitan conexión a Redis, así que se resuelven antes
	// de abrir el store para no fallar si la base no está disponible.
	switch args[0] {
	case "help", "-h", "--help":
		printUsage()
		return nil
	}

	s, err := store.Open(ctx, redisAddr)
	if err != nil {
		return err
	}
	defer s.Close()

	switch args[0] {
	case "player":
		return runPlayer(ctx, s, args[1:])
	case "team":
		return runTeam(ctx, s, args[1:])
	case "match":
		return runMatch(ctx, s, args[1:])
	case "serve":
		return runServe(s, args[1:])
	default:
		printUsage()
		return fmt.Errorf("comando desconocido: %s", args[0])
	}
}

// extractRedisAddrFlag busca "--redis-addr <host:puerto>" en cualquier
// posición de args y devuelve la dirección a usar (flag > env > default)
// junto con los argumentos restantes, ya sin el flag.
func extractRedisAddrFlag(args []string) (string, []string) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = defaultRedisAddr
	}

	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--redis-addr" && i+1 < len(args) {
			addr = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return addr, rest
}

// runPlayer maneja los subcomandos "player add", "player list" y "player del".
func runPlayer(ctx context.Context, s *store.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: gepa player <add|list|del> ...")
	}
	switch args[0] {
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("uso: gepa player add <equipo_id> <nombre>")
		}
		teamID, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("equipo_id inválido: %w", err)
		}
		p, err := s.AddPlayer(ctx, args[2], teamID)
		if err != nil {
			return err
		}
		fmt.Printf("jugador creado: #%d %s (equipo %d)\n", p.ID, p.Name, p.TeamID)
	case "list":
		players, err := s.ListPlayers(ctx)
		if err != nil {
			return err
		}
		for _, p := range players {
			fmt.Printf("#%d %s (equipo %d)\n", p.ID, p.Name, p.TeamID)
		}
	case "del":
		if len(args) < 2 {
			return fmt.Errorf("uso: gepa player del <id>")
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("jugador_id inválido: %w", err)
		}
		if err := s.DeletePlayer(ctx, id); err != nil {
			return err
		}
		fmt.Printf("jugador eliminado: #%d\n", id)
	default:
		return fmt.Errorf("subcomando desconocido: player %s", args[0])
	}
	return nil
}

// runTeam maneja los subcomandos "team add", "team list" y "team del".
func runTeam(ctx context.Context, s *store.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: gepa team <add|list|del> ...")
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("uso: gepa team add <nombre>")
		}
		t, err := s.AddTeam(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("equipo creado: #%d %s\n", t.ID, t.Name)
	case "list":
		teams, err := s.ListTeams(ctx)
		if err != nil {
			return err
		}
		for _, t := range teams {
			fmt.Printf("#%d %s\n", t.ID, t.Name)
		}
	case "del":
		if len(args) < 2 {
			return fmt.Errorf("uso: gepa team del <id>")
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("equipo_id inválido: %w", err)
		}
		if err := s.DeleteTeam(ctx, id); err != nil {
			return err
		}
		fmt.Printf("equipo eliminado: #%d\n", id)
	default:
		return fmt.Errorf("subcomando desconocido: team %s", args[0])
	}
	return nil
}

// runMatch maneja los subcomandos "match add", "match list" y "match del". Los equipos se
// referencian por ID (los mismos que muestra "team list").
func runMatch(ctx context.Context, s *store.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: gepa match <add|list|del> ...")
	}
	switch args[0] {
	case "add":
		if len(args) < 5 {
			return fmt.Errorf("uso: gepa match add <equipoA_id> <equipoB_id> <marcadorA> <marcadorB>")
		}
		teamAID, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("equipoA_id inválido: %w", err)
		}
		teamBID, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("equipoB_id inválido: %w", err)
		}
		scoreA, err := strconv.Atoi(args[3])
		if err != nil {
			return fmt.Errorf("marcadorA inválido: %w", err)
		}
		scoreB, err := strconv.Atoi(args[4])
		if err != nil {
			return fmt.Errorf("marcadorB inválido: %w", err)
		}
		m, err := s.AddMatch(ctx, teamAID, teamBID, scoreA, scoreB)
		if err != nil {
			return err
		}
		fmt.Printf("partido creado: #%d equipo %d %d - %d equipo %d\n", m.ID, m.TeamAID, m.ScoreA, m.ScoreB, m.TeamBID)
	case "list":
		matches, err := s.ListMatches(ctx)
		if err != nil {
			return err
		}
		for _, m := range matches {
			fmt.Printf("#%d equipo %d %d - %d equipo %d\n", m.ID, m.TeamAID, m.ScoreA, m.ScoreB, m.TeamBID)
		}
	case "del":
		if len(args) < 2 {
			return fmt.Errorf("uso: gepa match del <id>")
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("partido_id inválido: %w", err)
		}
		if err := s.DeleteMatch(ctx, id); err != nil {
			return err
		}
		fmt.Printf("partido eliminado: #%d\n", id)
	default:
		return fmt.Errorf("subcomando desconocido: match %s", args[0])
	}
	return nil
}

// runServe levanta el servidor HTTP de la API REST sobre el store dado.
// addr por defecto es ":8080"; se puede pasar otro como primer argumento
// (por ejemplo "gepa serve :9090").
func runServe(s *store.Store, args []string) error {
	addr := ":8080"
	if len(args) > 0 {
		addr = args[0]
	}
	handler := api.NewServer(s, instanceName())
	fmt.Printf("escuchando en %s (instancia %q)\n", addr, instanceName())

	// No usamos http.ListenAndServe directo porque no permite configurar
	// timeouts: un cliente lento (o malicioso) podría dejar conexiones
	// abiertas indefinidamente y agotar los file descriptors del proceso
	// (Slowloris). ReadHeaderTimeout corta esa ventana.
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	return server.ListenAndServe()
}

// instanceName identifica a esta instancia frente a las demás cuando corren
// varias detrás de un balanceador de carga (ver docker-compose.yml, donde
// cada app tiene su propia INSTANCE_NAME). Si no está seteada, usamos el
// hostname del contenedor/proceso como mejor esfuerzo.
func instanceName() string {
	if name := os.Getenv("INSTANCE_NAME"); name != "" {
		return name
	}
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	return "desconocida"
}

// printUsage imprime la ayuda de la CLI a stdout.
func printUsage() {
	fmt.Println(`gepa-cli: gestión simple de jugadores, equipos y partidos

Uso:
  gepa [--redis-addr <host:puerto>] player add <equipo_id> <nombre>
  gepa [--redis-addr <host:puerto>] player list
  gepa [--redis-addr <host:puerto>] player del <id>
  gepa [--redis-addr <host:puerto>] team add <nombre>
  gepa [--redis-addr <host:puerto>] team list
  gepa [--redis-addr <host:puerto>] team del <id>
  gepa [--redis-addr <host:puerto>] match add <equipoA_id> <equipoB_id> <marcadorA> <marcadorB>
  gepa [--redis-addr <host:puerto>] match list
  gepa [--redis-addr <host:puerto>] match del <id>
  gepa [--redis-addr <host:puerto>] serve [addr]   (por defecto :8080)

Redis por defecto: localhost:6379 (override con --redis-addr o REDIS_ADDR).`)
}
