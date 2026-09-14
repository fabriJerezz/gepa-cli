# FutGo-cli

[![CI](https://github.com/fabriJerezz/gepa-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/fabriJerezz/gepa-cli/actions/workflows/ci.yml)
[![SAST](https://github.com/fabriJerezz/gepa-cli/actions/workflows/sast.yml/badge.svg)](https://github.com/fabriJerezz/gepa-cli/actions/workflows/sast.yml)

**App en producción:** https://futgo-cli.onrender.com


CLI simple en Go para administrar **jugadores** (nombre y equipo), **equipos** y
**partidos** (con resultado entre dos equipos). Además de la línea de
comandos, expone una **API REST** que opera sobre los mismos datos, y sirve
una **interfaz web** que se abre en el navegador y consume esa misma API.

Los datos se persisten en **Redis**, así que la CLI, la API y la interfaz web
(a través de la API) leen y escriben sobre la misma base de datos.

## Requisitos

- Docker y Docker Compose (forma recomendada), **o**
- Go 1.22+ y un Redis accesible en `localhost:6379` (para correr sin Docker).

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
| `app1`, `app2`, `app3`       | 3 réplicas idénticas que sirven la interfaz web y la API REST, sin estado propio |
| `nginx`                      | Balanceador de carga (round robin), único punto de entrada público |

Punto de entrada de la aplicación (a través de Nginx). Abierta en el
navegador, esta URL muestra la [interfaz web](#interfaz-web); la misma
dirección es también la base de la API REST (`/health`, `/teams`,
`/players`, `/matches`):

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
curl -X POST localhost:8081/players -d '{"name":"Compartido entre instancias","team_id":1}'
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
docker compose run --rm app1 team add "Argentina"
docker compose run --rm app1 player add 1 "Lionel Messi"
docker compose run --rm app1 player list
```

Apagar todo:

```bash
docker compose down       # conserva los datos (volumen redis-data)
docker compose down -v    # borra también los datos
```

## Interfaz web

Además de la CLI y la API, la app sirve una **interfaz web** para gestionar
equipos, jugadores y partidos desde el navegador. No tiene lógica ni datos
propios: todo lo que muestra y todo lo que modifica pasa por los mismos
endpoints de la [API REST](#api-rest), así que un equipo creado desde la
interfaz es exactamente el mismo que ves con `curl` o desde la CLI.

| Entorno | URL |
|---------|-----|
| Local (Docker Compose, a través de Nginx) | http://localhost:8080 |
| Producción (Render) | https://futgo-cli.onrender.com |

### Decisión de diseño: el front va embebido en el binario

Los archivos de la interfaz (`internal/web/assets/`: `index.html`, `app.css`
y `app.js`) se compilan **dentro del binario de Go** con la directiva
`go:embed` (ver `internal/web/web.go`). `web.Mount` registra sus rutas (`/` y
`/assets/`) sobre el mismo `http.ServeMux` que arma `api.NewServer` para la
API, así que un único proceso atiende las dos cosas. Eso conviene por tres
motivos:

- **No hace falta un contenedor extra ni un servidor de estáticos.** No hay
  una imagen aparte para el front ni un servidor dedicado a servir archivos:
  la imagen de la app ya los trae adentro. Y como van dentro del binario, no
  dependen de que existan en el filesystem del contenedor, que es `scratch`
  y no tiene nada más que el ejecutable.
- **Front y API comparten origen.** La página se sirve desde el mismo host y
  puerto que la API, y `app.js` le pega con rutas relativas (`/teams`,
  `/players`, `/matches`). Para el navegador es todo el mismo origen, así que
  no hay CORS que resolver.
- **Cada réplica es autosuficiente.** Las 3 instancias detrás de Nginx sirven
  la interfaz y la API a la vez: no aparece un servicio de front que sea otro
  punto de falla ni que haya que escalar por separado. En Render pasa lo
  mismo con su única instancia.

### El indicador de instancia

En el encabezado de la interfaz hay un indicador que muestra qué réplica
respondió: `app1`, `app2` o `app3` en local, `render` en producción. Lo saca
del campo `instance` de `GET /health`, que la interfaz consulta al cargar la
página — es el mismo dato de [¿Cómo sé a qué instancia le
pegué?](#cómo-sé-a-qué-instancia-le-pegué), pero visible sin abrir una
terminal.

> El indicador refleja la réplica que atendió ese `/health` puntual. Las
> demás requests de la página también pasan por Nginx y pueden caer en otra
> instancia; como todas comparten el mismo Redis, los datos que ves son los
> mismos igual.

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
./gepa-cli player add 1 "Lionel Messi"
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
  para x86 no arranca en un CPU ARM. Publicar las dos variantes dentro de la
  misma imagen la vuelve portable entre arquitecturas: las máquinas de
  desarrollo con Apple Silicon son `arm64`, Render corre sobre `amd64`, y si
  en el futuro se retoma el camino de desplegar en una VM ARM se puede hacer
  sin rebuildear nada. El workflow usa QEMU para emular la arquitectura que
  no es la del runner; después `docker pull` baja automáticamente la que
  corresponde a cada máquina.
- **Sin secrets**: GHCR se autentica con el `GITHUB_TOKEN` que GitHub le
  inyecta al workflow. Con Docker Hub habría que crear un access token a
  mano y guardarlo como secret del repo.

Se publican dos tags: `:latest` (último main, el que usa el cloud) y
`:sha-<commit>` (inmutable, para volver a una versión exacta).

### Local vs. nube: dos composes

| Archivo | Apps | Para qué |
|---------|------|----------|
| `docker-compose.yml` | `build: .` | Desarrollo: compila desde el código que tenés al lado |
| `docker-compose.cloud.yml` | `image: ghcr.io/...` | Despliegue sobre un servidor propio: baja la imagen publicada, no necesita el código |

Son la misma arquitectura (nginx + 3 réplicas + Redis); lo único que cambia
es de dónde sale la imagen de la app.

`docker-compose.cloud.yml` es el camino para levantar el stack completo en
una VM propia y sigue siendo válido para eso, pero **no es lo que corre hoy
en producción**: el despliegue en la nube usa la imagen de GHCR directo en
Render, sin compose (ver [Desplegar en la
nube](#desplegar-en-la-nube-render)).

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
| POST   | `/players`  | Crea un jugador (`{"name": "...", "team_id": 1}`) |
| DELETE | `/players/{id}` | Elimina un jugador                    |
| GET    | `/teams`    | Lista todos los equipos               |
| POST   | `/teams`    | Crea un equipo (`{"name": "..."}`)    |
| DELETE | `/teams/{id}` | Elimina un equipo sin relaciones      |
| GET    | `/matches`  | Lista todos los partidos              |
| POST   | `/matches`  | Crea un partido                       |
| DELETE | `/matches/{id}` | Elimina un partido                    |

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
curl -X POST localhost:8080/teams -d '{"name":"Argentina"}'
curl -X POST localhost:8080/teams -d '{"name":"Francia"}'
curl localhost:8080/teams

curl -X POST localhost:8080/players -d '{"name":"Lionel Messi","team_id":1}'
curl localhost:8080/players

curl -X POST localhost:8080/matches -d '{"team_a_id":1,"team_b_id":2,"score_a":3,"score_b":3}'
curl localhost:8080/matches
```

## Desplegar en la nube (Render)

La app está publicada en **https://futgo-cli.onrender.com**, corriendo en
[Render](https://render.com) como un único **Web Service** del plan Free en
la región **Oregon (US West)**.

Llegamos a Render después de que tres proveedores de VMs nos rechazaran la
cuenta (el detalle está [más
abajo](#el-camino-hasta-render-tres-proveedores-rechazados)). Más allá de que
no exige tarjeta de crédito, lo que lo hace encajar con este proyecto es que
puede desplegar **directamente desde una imagen ya publicada en un
registry** — que es exactamente el artefacto que produce nuestro CD. Render
no compila nada: baja de GHCR la misma imagen que construyó y testeó el
pipeline.

**1. Crear primero la base de datos (Key Value).** En el dashboard →
*New* → *Key Value*, plan **Free**, región **Oregon**. Al terminar, Render
muestra la dirección interna del servicio, con forma `redis://red-xxxx:6379`.

> Creá el Key Value en la **misma región** que el Web Service: la dirección
> interna solo es alcanzable desde servicios de la misma región.

**2. Crear el Web Service desde la imagen.** *New* → *Web Service* → la
opción de desplegar una **imagen existente de un registry**, con:

```
ghcr.io/fabrijerezz/gepa-cli:latest
```

Plan **Free**, región **Oregon**. El paquete es público en GHCR, así que
Render no necesita credenciales para bajarlo.

**3. Configurar las variables de entorno** del Web Service:

| Variable | Valor | Para qué |
|----------|-------|----------|
| `REDIS_ADDR` | `<host interno del Key Value>:6379` | A qué Redis se conecta la app |
| `PORT` | `8080` | A qué puerto del contenedor rutea Render |
| `INSTANCE_NAME` | `render` | Nombre que la app reporta en `X-Instance` y `/health` |

Dos detalles que cuesta descubrir y que son la diferencia entre que el
servicio levante o quede reiniciándose en loop:

> **`REDIS_ADDR` va sin esquema.** Render entrega la dirección del Key Value
> como una URL completa (`redis://red-xxxx:6379`), pero nuestro código lee
> `REDIS_ADDR` con formato `host:puerto` plano (ver `extractRedisAddrFlag` en
> `main.go`). Hay que pegarla **sin el prefijo `redis://`**. Si el valor no
> llega bien, el binario cae al default `localhost:6379` y el contenedor se
> muere con `connection refused`.

> **`PORT=8080` no la lee la app, la lee Render.** El binario escucha en el
> `:8080` fijo que define el `CMD` del Dockerfile y nunca mira la variable
> `PORT`. Declarársela a Render es lo que le indica a qué puerto del
> contenedor mandar el tráfico; sin eso, el servicio queda publicado pero
> apuntando al puerto equivocado.

**4. Probar:**

```bash
curl https://futgo-cli.onrender.com/health
```

El body trae el `instance` configurado (`{"status":"ok","instance":"render"}`):
es el mismo mecanismo que en local distingue `app1`/`app2`/`app3`, acá con
una sola instancia llamada `render`.

> **Plan Free:** el servicio se suspende tras ~15 minutos sin tráfico. La
> primera request después de una suspensión tarda entre 30 y 60 segundos
> mientras Render vuelve a levantar el contenedor; las siguientes responden
> normal. Si estás probando la URL y parece caída, esperá ese rato antes de
> asumir que algo se rompió.

**Actualizar a una versión nueva:** una vez que el CI publicó la imagen,
alcanza con un *Manual Deploy* desde el dashboard, que vuelve a bajar
`:latest` de GHCR. No hay nada que compilar ni que copiar a ningún servidor.

> **¿Y el `git clone` del repo en el servidor?** Acá no hay ninguno, y vale
> entender por qué: es la distinción entre **código** y **configuración**. El
> código viaja dentro de la imagen — Render solo hace `pull`, nunca compila,
> ni siquiera tiene Go. La configuración, en un despliegue sobre una VM, vive
> en archivos del repo (`docker-compose.cloud.yml`, `nginx.conf`) y por eso
> ahí sí hay que clonarlo; en Render, en cambio, la configuración son las
> tres variables de entorno del servicio. Como corre un solo contenedor y sin
> proxy propio adelante, no queda ningún archivo de config que traer.

### Por qué una instancia en la nube y tres en local

No son dos versiones de lo mismo con distinta ambición: son las dos cosas
distintas que pide el enunciado, cada una en el entorno donde tiene sentido
demostrarla.

La **arquitectura completa —proxy y réplicas— corre en el entorno local**:
`docker-compose.yml` + `nginx.conf` levantan Nginx balanceando tres réplicas
idénticas contra un Redis compartido. Ahí es donde se demuestra el round
robin, la tolerancia a fallos cuando se cae una instancia, y que las apps son
realmente sin estado (todo eso está documentado más arriba, con los comandos
para reproducirlo).

De la nube, en cambio, el enunciado pide **al menos una instancia funcional
desplegada desde el registro**, y las réplicas figuran como mejora opcional.
Eso es exactamente lo que hace el Web Service de Render: prueba que el
artefacto publicado en GHCR se despliega y funciona fuera de nuestra máquina,
contra una base de datos gestionada. Agregarle réplicas y un Nginx propio
sería repetir en la nube una demostración que el entorno local ya cubre
entera.

### El camino hasta Render: tres proveedores rechazados

El plan original era desplegar sobre una VM — de ahí sale
`docker-compose.cloud.yml`, que sigue siendo el camino válido para ese
escenario. Ninguno de los tres proveedores que intentamos nos dejó llegar a
tener la VM:

- **Azure for Students:** la creación de la VM fallaba por cuota/capacidad.
- **Oracle Cloud:** el registro fue rechazado por el sistema antifraude, con
  un error genérico y sin detalle, a pesar de que la tarjeta se verificó
  correctamente.
- **Google Cloud:** rechazó la autorización temporal de USD 50 que hace sobre
  la tarjeta al crear la cuenta.

Render destrabó el problema por dos motivos: no exige tarjeta de crédito para
el plan Free, y despliega desde una imagen de registry, así que el trabajo ya
hecho en el CD se reusó tal cual.

## Estructura del proyecto

```
.
├── main.go                   # CLI: parseo de subcomandos y arranque del servidor
├── internal/
│   ├── model/                # Structs de dominio: Player, Team, Match
│   ├── store/                # Persistencia en Redis
│   ├── api/                  # Handlers HTTP de la API REST
│   └── web/                  # Interfaz web embebida en el binario
│       ├── web.go            # go:embed de assets/ y montaje sobre el mux
│       └── assets/
│           ├── index.html    # Estructura de la página
│           ├── app.css       # Estilos
│           └── app.js        # Consume la API REST y renderiza los datos
├── Dockerfile                # Build multi-stage de la imagen de la app
├── docker-compose.yml        # LOCAL: compila la imagen (build: .)
├── docker-compose.cloud.yml  # NUBE: baja la imagen del registry (image: ghcr.io/...)
├── nginx.conf                # Config del balanceador de carga (round robin)
├── .github/workflows/
│   ├── ci.yml                # Tests (go vet + go test -race)
│   ├── sast.yml              # Análisis de seguridad (gosec)
│   └── publish.yml           # Build multi-arch + push a GHCR
└── .dockerignore
```

## Modelo de datos en Redis

- `gepa:seq:player` / `gepa:seq:team` / `gepa:seq:match`: contadores
  (`INCR`) usados para generar IDs autoincrementales.
- `gepa:players` / `gepa:teams` / `gepa:matches`: hashes de Redis donde cada
  campo es el ID de la entidad y el valor es el registro en JSON.
