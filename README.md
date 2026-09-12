# gepa-cli

[![CI](https://github.com/fabriJerezz/gepa-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/fabriJerezz/gepa-cli/actions/workflows/ci.yml)
[![SAST](https://github.com/fabriJerezz/gepa-cli/actions/workflows/sast.yml/badge.svg)](https://github.com/fabriJerezz/gepa-cli/actions/workflows/sast.yml)

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

Esto levanta **3 instancias de la API (`app1`, `app2`, `app3`)** sin estado
propio, todas conectadas a un **único Redis compartido**, más un **Nginx**
que balancea la carga entre ellas:

| Servicio | Rol                                            |
|----------|-------------------------------------------------|
| `redis`                      | La única base de datos, compartida por las 3 instancias |
| `app1`, `app2`, `app3`       | 3 réplicas idénticas de la misma API REST, sin estado propio |
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

### ¿Cómo sé a qué instancia le pegué?

`X-Upstream-Addr` (la agrega Nginx) muestra la IP:puerto interna del
contenedor, pero no es muy legible. Para saber directamente si te
respondió `app1`, `app2` o `app3`, la propia app expone esa info de dos
formas:

- Toda respuesta trae la cabecera **`X-Instance`** con el nombre de la
  instancia (`app1`/`app2`/`app3`), seteado vía la variable de entorno
  `INSTANCE_NAME` en `docker-compose.yml`.
- `GET /health` devuelve además `{"status":"ok","instance":"app1"}` en el
  body, por si no estás mirando las cabeceras.

```bash
curl -s -D - -o /dev/null http://localhost:8080/health | grep -i x-instance
curl -s http://localhost:8080/health
```

### Nota sobre el estado: las 3 instancias comparten los mismos datos

Las 3 apps apuntan al mismo `REDIS_ADDR=redis:6379`, así que ven
exactamente la misma información sin importar a cuál te haya enrutado
Nginx. Podés comprobarlo creando un dato en una instancia y leyéndolo
desde otra:

```bash
curl -X POST localhost:8081/players -d '{"name":"Compartido entre instancias"}'
curl localhost:8081/players   # lo ves
curl localhost:8082/players   # también: es el mismo Redis
curl localhost:8083/players   # también
```

#### ¿Por qué hace falta que sea el mismo Redis, si en producción una base de datos también puede tener varias instancias con los mismos datos?

Porque "varias instancias con los mismos datos" en producción **no**
significa apuntar a instancias independientes que casualmente coinciden —
significa **replicación**: varios procesos de base de datos que se
sincronizan activamente entre sí (una *primary* que acepta escrituras y
*replicas* que la siguen, o un *cluster* con particionamiento y failover
automático — Redis Cluster, Redis Sentinel, o en el mundo SQL: replicación
primary-replica, Patroni, Aurora, etc.).

Acá optamos por el camino simple: **un solo proceso de Redis**, compartido
por las 3 apps (que sí son réplicas sin estado, apropiadas para ir detrás
de un balanceador). Eso resuelve la consistencia de datos, pero el propio
Redis vuelve a ser un único punto de falla y un cuello de botella — si se
cae, las 3 instancias se caen con él. El patrón de producción (réplicas de
Redis reales) es más complejo porque hay que resolver: qué nodo acepta
escrituras, cómo se propagan a las réplicas, qué pasa con una escritura
concurrente, y cómo se promueve automáticamente una réplica a primary si el
nodo activo se cae. Ese nivel de complejidad queda fuera del alcance de
este proyecto, pero el siguiente paso natural sería agregar Redis Sentinel
o Redis Cluster en vez de un Redis único.

Para usar la **CLI** contra la app, corré un contenedor puntual (ya apunta
al Redis compartido):

```bash
docker compose run --rm app1 player add "Lionel Messi"
docker compose run --rm app1 player list
```

Apagar todo:

```bash
docker compose down       # conserva los datos (volumen redis-data)
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

## Tests

```bash
go test ./...
```

**¿Contra qué Redis corren los tests si no levantaste ninguno?** Contra
ninguno real: usan [`miniredis`](https://github.com/alicebob/miniredis), una
implementación de Redis en memoria pensada para tests. Se levanta y se
destruye dentro del propio proceso del test (`miniredis.RunT(t)`), así que
no depende de Docker ni de una conexión de red — corre en milisegundos y
funciona igual en tu máquina que en GitHub Actions.

La regla simple para elegir qué testear: **funciones puras primero**
(`extractRedisAddrFlag`, `instanceName`) porque no dependen de nada externo
y son gratis de testear; después **la lógica de negocio** (`internal/store`:
¿un jugador sin nombre falla? ¿un partido contra un equipo inexistente
devuelve el error correcto?); y por último **el contrato HTTP**
(`internal/api`: ¿qué status code devuelve cada caso?) usando
`httptest` en vez de levantar un servidor real en un puerto.

## CI: tests + SAST en cada push

Dos workflows en `.github/workflows/`, cada uno con su badge arriba en este
README:

- **`ci.yml`** corre `go vet` + `go test ./... -race` en cada push/PR a
  `main`. Es la garantía de "el código compila y hace lo que dicen los
  tests" antes de mergear nada.
- **`sast.yml`** corre [`gosec`](https://github.com/securego/gosec), un
  SAST para Go. La diferencia con los tests: un test corre tu código y
  verifica el resultado; un SAST **nunca ejecuta nada** — lee el código
  fuente y busca patrones conocidos como inseguros (ej. este mismo repo
  tuvo que corregir dos: un servidor HTTP sin timeouts, vulnerable a
  Slowloris, y un log que volcaba el path del request sin escapar). El
  reporte también se sube al tab **Security → Code scanning** del repo en
  GitHub, no solo al badge.

Ambos badges leen directo el estado del último run de su workflow — no hay
nada más que configurar para que se actualicen solos en cada push.

## CD: publicar la imagen en el registry

`publish.yml` construye la imagen y la sube a **GHCR**
(`ghcr.io/fabrijerezz/gepa-cli`), el registry de contenedores de GitHub.

**¿Para qué sirve un registry?** Es el "depósito" de imágenes ya
construidas. Sin él, cada servidor donde quieras desplegar necesitaría el
código fuente, Go, y compilar la imagen por su cuenta. Con él, el servidor
solo hace `docker pull` y corre un artefacto ya testeado — el mismo binario
exacto que pasó por CI, sin recompilar nada.

Dos detalles del workflow que no son obvios:

- **Multi-arquitectura** (`linux/amd64,linux/arm64`): una imagen compilada
  para x86 no arranca en un CPU ARM. Como la VM gratis de Oracle Cloud es
  ARM (Ampere), el workflow usa QEMU para emular esa arquitectura y meter
  las dos variantes en la misma imagen. Después `docker pull` baja
  automáticamente la que corresponde a cada máquina.
- **Sin secrets**: GHCR se autentica con el `GITHUB_TOKEN` que GitHub le
  inyecta al workflow. Con Docker Hub habría que crear un access token a
  mano y guardarlo como secret del repo.

Se publican dos tags: `:latest` (último main, el que usa el cloud) y
`:sha-<commit>` (inmutable, para volver a una versión exacta).

### Local vs. nube: dos composes

| Archivo | Apps | Para qué |
|---------|------|----------|
| `docker-compose.yml` | `build: .` | Desarrollo: compila desde el código que tenés al lado |
| `docker-compose.cloud.yml` | `image: ghcr.io/...` | Despliegue: baja la imagen publicada, no necesita el código |

Son la misma arquitectura (nginx + 3 réplicas + Redis); lo único que cambia
es de dónde sale la imagen de la app.

## Tolerancia a fallos: qué pasa si se cae una instancia

El balanceo reparte carga; la **tolerancia a fallos** es lo que hace que la
caída de una instancia no se traduzca en errores para el usuario. Son dos
cosas distintas y en `nginx.conf` las configuran dos directivas distintas:

- `max_fails=1 fail_timeout=10s` (en el bloque `upstream`): cuando una
  instancia falla, Nginx la **saca de la rotación** por 10 segundos y
  después la vuelve a probar sola. Por eso no hace falta reiniciar nada
  cuando la instancia vuelve.
- `proxy_next_upstream` (en el `location`): si la request ya salió hacia
  una instancia caída, Nginx la **reintenta contra la siguiente** en vez
  de devolver el error. Esto es lo que hace que el cliente vea un 200.

Probalo vos mismo — tirá una instancia mientras el stack corre:

```bash
docker compose stop app2

# 30 requests contra el balanceador: deberian ser todas 200
for i in $(seq 1 30); do curl -s -o /dev/null -w "%{http_code} " http://localhost:8080/health; done

# y solo deberian responder app1 y app3
for i in $(seq 1 30); do curl -s -D - -o /dev/null http://localhost:8080/health | grep -i x-instance; done | sort | uniq -c

docker compose start app2   # vuelve sola a la rotacion, sin tocar nginx
```

Resultado medido en este repo: **30/30 respuestas `200`** durante la caída,
repartidas solo entre `app1` y `app3`; al levantar `app2` la distribución
volvió sola a 7/7/7.

## API REST

### Endpoints

| Método | Ruta        | Descripción                          |
|--------|-------------|---------------------------------------|
| GET    | `/health`   | Chequeo de salud (incluye `instance`) |
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
├── docker-compose.yml        # LOCAL: compila la imagen (build: .)
├── docker-compose.cloud.yml   # NUBE: baja la imagen del registry (image: ghcr.io/...)
├── nginx.conf                # Config del balanceador de carga (round robin)
├── .github/workflows/
│   ├── ci.yml                 # Tests (go vet + go test -race)
│   ├── sast.yml               # Análisis de seguridad (gosec)
│   └── publish.yml            # Build multi-arch + push a GHCR
└── .dockerignore
```

## Modelo de datos en Redis

- `gepa:seq:player` / `gepa:seq:team` / `gepa:seq:match`: contadores
  (`INCR`) usados para generar IDs autoincrementales.
- `gepa:players` / `gepa:teams` / `gepa:matches`: hashes de Redis donde cada
  campo es el ID de la entidad y el valor es el registro en JSON.
