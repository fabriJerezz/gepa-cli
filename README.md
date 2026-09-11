# gepa-cli

- gestionador de partidos

CLI simple en Go para administrar **jugadores** (solo nombre), **equipos** y
**partidos** (con resultado entre dos equipos). Además de la línea de
comandos, expone una **API REST** que opera sobre los mismos datos.

Los datos se persisten en **Redis**, así que tanto la CLI como la API leen y
escriben sobre la misma base de datos.

## Requisitos

- Docker y Docker Compose (forma recomendada), **o**
- Go 1.22+ y un Redis accesible en `localhost:6379` (para correr sin Docker)

## Levantar todo con Docker Compose (recomendado)

```bash
docker compose up --build
```

Esto levanta **3 instancias independientes de `[gepa-cli + redis]`** más un
**Nginx** que balancea la carga entre ellas:

| Servicio | Rol                                            |
|----------|-------------------------------------------------|
| `redis1`, `redis2`, `redis3` | Una base de datos Redis por instancia |
| `app1`, `app2`, `app3`       | Una API REST por instancia, cada una conectada a su propio Redis |
| `nginx`                      | Balanceador de carga (round robin), único punto de entrada público |

Punto de entrada de la aplicación (a través de Nginx):

```
http://localhost:8080
```

Cada instancia también queda expuesta por separado para poder pegarle
directo y comparar (`app1` → `8081`, `app2` → `8082`, `app3` → `8083`).

Verificar que está arriba y ver cómo rota el balanceo entre backends (mirá
la cabecera `X-Upstream-Addr` en la respuesta):

```bash
for i in $(seq 1 20); do
  curl -s -D - -o /dev/null http://localhost:8080/health | grep -i x-upstream-addr
done | sort | uniq -c
```

Con pocas requests puede parecer que siempre responde el mismo backend:
Nginx corre varios worker processes y cada uno mantiene su propio contador
de round robin, así que hacen falta unas cuantas requests para que se
note la rotación entre los tres (`app1`, `app2`, `app3`). Verificado en
este repo: con 20 requests, la distribución fue 7/7/6.

### Nota sobre el estado

Cada instancia tiene **su propio Redis**, no uno compartido: son 3 pares
`[app-redis]` aislados, no 3 réplicas con estado consistente. Esto significa
que, por ejemplo, si creás un jugador y Nginx te enrutó a `app1`, ese
jugador va a aparecer en `GET /players` solo cuando vuelvas a caer en
`app1` (o si le pegás directo por `localhost:8081`). Para comprobarlo:

```bash
curl -X POST localhost:8081/players -d '{"name":"Solo en instancia 1"}'
curl localhost:8081/players   # lo ves
curl localhost:8082/players   # no lo ves: redis2 no tiene ese dato
```

Si en cambio necesitás que las 3 instancias vean los mismos datos, el
cambio es apuntar las 3 apps a un único servicio de Redis compartido en vez
de uno por instancia (o migrar a Redis en modo cluster/sentinel).

Para usar la **CLI** contra una instancia puntual, corré un contenedor
apuntando a su Redis (por ejemplo, la de `app1`):

```bash
docker compose run --rm -e REDIS_ADDR=redis1:6379 app1 player add "Lionel Messi"
docker compose run --rm -e REDIS_ADDR=redis1:6379 app1 player list
```

Apagar todo:

```bash
docker compose down       # conserva los datos (volúmenes redis1-data, redis2-data, redis3-data)
docker compose down -v    # borra también los datos
```

## Correr sin Docker

Compilar:

```bash
go build -o gepa-cli .
```

Necesitás un Redis corriendo y accesible. Por defecto la CLI/API se conecta
a `localhost:6379`; podés cambiarlo con el flag `--redis-addr` (funciona en
cualquier posición) o con la variable de entorno `REDIS_ADDR`:

```bash
./gepa-cli --redis-addr localhost:6390 team add "Brasil"
REDIS_ADDR=localhost:6390 ./gepa-cli team list
```

### Uso de la CLI

```bash
# Jugadores
./gepa-cli player add "Lionel Messi"
./gepa-cli player list

# Equipos
./gepa-cli team add "Argentina"
./gepa-cli team add "Francia"
./gepa-cli team list

# Partidos (usa los IDs de equipo mostrados en `team list`)
./gepa-cli match add 1 2 3 3
./gepa-cli match list
```

### Levantar la API

```bash
./gepa-cli serve :8080
```

## API REST

### Endpoints

| Método | Ruta        | Descripción                          |
|--------|-------------|---------------------------------------|
| GET    | `/health`   | Chequeo de salud                      |
| GET    | `/players`  | Lista todos los jugadores             |
| POST   | `/players`  | Crea un jugador (`{"name": "..."}`)   |
| GET    | `/teams`    | Lista todos los equipos               |
| POST   | `/teams`    | Crea un equipo (`{"name": "..."}`)    |
| GET    | `/matches`  | Lista todos los partidos              |
| POST   | `/matches`  | Crea un partido                       |

Body para crear un partido:

```json
{
  "team_a_id": 1,
  "team_b_id": 2,
  "score_a": 3,
  "score_b": 1
}
```

### Ejemplos con curl

> Si tenés las 3 instancias corriendo detrás de Nginx (`localhost:8080`),
> cada request de esta secuencia puede caer en una instancia distinta y
> los IDs no van a coincidir entre sí (ver "Nota sobre el estado" más
> arriba). Para reproducir esta secuencia tal cual, corré todo contra una
> sola instancia fija, por ejemplo `localhost:8081`.

```bash
curl -X POST localhost:8080/players -d '{"name":"Lionel Messi"}'
curl localhost:8080/players

curl -X POST localhost:8080/teams -d '{"name":"Argentina"}'
curl -X POST localhost:8080/teams -d '{"name":"Francia"}'
curl localhost:8080/teams

curl -X POST localhost:8080/matches -d '{"team_a_id":1,"team_b_id":2,"score_a":3,"score_b":3}'
curl localhost:8080/matches
```

## Estructura del proyecto

```
.
├── main.go                # CLI: parseo de subcomandos y arranque del servidor
├── internal/
│   ├── model/              # Structs de dominio: Player, Team, Match
│   ├── store/               # Persistencia en Redis
│   └── api/                  # Handlers HTTP de la API REST
├── Dockerfile               # Build multi-stage de la imagen de la app
├── docker-compose.yml        # Orquesta 3x [app + Redis] + Nginx
├── nginx.conf                # Config del balanceador de carga (round robin)
└── .dockerignore
```

## Modelo de datos en Redis

- `gepa:seq:player` / `gepa:seq:team` / `gepa:seq:match`: contadores
  (`INCR`) usados para generar IDs autoincrementales.
- `gepa:players` / `gepa:teams` / `gepa:matches`: hashes de Redis donde cada
  campo es el ID de la entidad y el valor es el registro en JSON.
