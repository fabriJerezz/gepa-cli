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
	"slices"
	"strconv"
	"time"

	"gepa-cli/internal/api"
	"gepa-cli/internal/model"
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
	case "standings":
		return runStandings(ctx, s)
	case "rebuild":
		n, err := s.Rebuild(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("modelos de lectura reconstruidos desde los eventos de %d partidos\n", n)
		return nil
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

// runMatch maneja los subcomandos de partidos. Los equipos se referencian
// por ID (los mismos que muestra "team list").
//
// "match add" carga un partido ya jugado: por dentro genera los mismos
// eventos que la interfaz en vivo (inicio, un gol por cada tanto, cierre).
// Después de escribir, la CLI proyecta el partido ella misma porque no tiene
// un proyector corriendo; como la proyección es idempotente, que un
// servidor lo proyecte de nuevo no cambia nada.
func runMatch(ctx context.Context, s *store.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: gepa match <add|list|del> ...")
	}
	switch args[0] {
	case "add":
		if len(args) < 5 {
			return fmt.Errorf("uso: gepa match add <equipoA_id> <equipoB_id> <golesA> <golesB>")
		}
		nums := make([]int, 4)
		for i, name := range []string{"equipoA_id", "equipoB_id", "golesA", "golesB"} {
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("%s inválido: %w", name, err)
			}
			nums[i] = n
		}
		if nums[2] < 0 || nums[3] < 0 {
			return fmt.Errorf("el resultado no puede ser negativo")
		}
		m, err := s.StartMatch(ctx, nums[0], nums[1])
		if err != nil {
			return err
		}
		goals := append(slices.Repeat([]int{m.TeamAID}, nums[2]), slices.Repeat([]int{m.TeamBID}, nums[3])...)
		for _, team := range goals {
			if _, err := s.AppendEvent(ctx, m.ID, -1, model.Event{Type: model.EventGoal, TeamID: team}, nil); err != nil {
				return err
			}
		}
		if _, err := s.AppendEvent(ctx, m.ID, -1, model.Event{Type: model.EventMatchFinished}, nil); err != nil {
			return err
		}
		if err := s.ProjectMatch(ctx, m.ID); err != nil {
			return err
		}
		fmt.Printf("partido cargado: #%d equipo %d %d - %d equipo %d\n", m.ID, m.TeamAID, nums[2], nums[3], m.TeamBID)
	case "list":
		matches, err := s.ListMatches(ctx)
		if err != nil {
			return err
		}
		for _, m := range matches {
			fmt.Printf("#%d equipo %d %d - %d equipo %d (%s, v%d)\n", m.ID, m.TeamAID, m.ScoreA, m.ScoreB, m.TeamBID, m.Status, m.Version)
		}
	case "del":
		if len(args) < 2 {
			return fmt.Errorf("uso: gepa match del <id>")
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("partido_id inválido: %w", err)
		}
		if err := s.CancelMatch(ctx, id); err != nil {
			return err
		}
		if err := s.ProjectMatch(ctx, id); err != nil {
			return err
		}
		fmt.Printf("partido cancelado: #%d\n", id)
	default:
		return fmt.Errorf("subcomando desconocido: match %s", args[0])
	}
	return nil
}

// runStandings imprime la tabla de posiciones (modelo de lectura).
func runStandings(ctx context.Context, s *store.Store) error {
	rows, err := s.Standings(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%-3s %-20s %3s %3s %3s %3s %3s %3s %4s %4s\n", "#", "Equipo", "PJ", "G", "E", "P", "GF", "GC", "DG", "Pts")
	for i, r := range rows {
		fmt.Printf("%-3d %-20s %3d %3d %3d %3d %3d %3d %4d %4d\n", i+1, r.TeamName, r.Played, r.Won, r.Drawn, r.Lost, r.GoalsFor, r.GoalsAgainst, r.GoalDiff, r.Points)
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

	// El proyector corre dentro de cada réplica, en segundo plano. Las 3
	// comparten un consumer group de Redis, así que cada evento lo proyecta
	// una sola (ver store.RunProjector).
	go s.RunProjector(context.Background(), instanceName())

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
  gepa [--redis-addr <host:puerto>] match add <equipoA_id> <equipoB_id> <golesA> <golesB>
  gepa [--redis-addr <host:puerto>] match list
  gepa [--redis-addr <host:puerto>] match del <id>        (cancela el partido)
  gepa [--redis-addr <host:puerto>] standings             (tabla de posiciones)
  gepa [--redis-addr <host:puerto>] rebuild               (rearma la tabla desde los eventos)
  gepa [--redis-addr <host:puerto>] serve [addr]   (por defecto :8080)

Redis por defecto: localhost:6379 (override con --redis-addr o REDIS_ADDR).`)
}
