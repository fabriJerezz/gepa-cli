# Guion de la presentación (15 minutos)

Este documento es lo que hay que **decir y mostrar** en la presentación. Para
cada concepto indica dónde se ve en la app, dónde está implementado, por qué
se decidió así y qué cambió respecto de la versión anterior.

- La teoría completa de cada concepto está en [teoria.md](teoria.md).
- El detalle de implementación está en [tecnica.md](tecnica.md).

## Agenda

| # | Bloque | Concepto | Tiempo | Acumulado |
|---|---|---|---|---|
| 0 | Qué cambió y por qué | Contexto | 1:00 | 1:00 |
| 1 | Nginx y las 3 réplicas | Round robin, tolerancia a fallos | 2:00 | 3:00 |
| 2 | Un partido como eventos | Event Sourcing | 2:30 | 5:30 |
| 3 | El reloj del partido | Invariante, tiempo del servidor | 1:30 | 7:00 |
| 4 | Escribir y leer por separado | CQRS, proyector, consistencia eventual | 2:30 | 9:30 |
| 5 | Pedidos repetidos | Idempotencia, concurrencia optimista | 3:00 | 12:30 |
| 6 | Race conditions | Check-then-act, operaciones atómicas | 1:00 | 13:30 |
| 7 | Cierre | Costos y límites | 1:00 | 14:30 |

Quedan 30 segundos de margen. Si el tiempo aprieta, el bloque 6 se puede
decir en una sola frase.

## Preparación (antes de presentar)

1. Levantar el stack desde cero. **Ojo:** `down -v` borra los datos de Redis.

   ```bash
   docker compose down -v
   docker compose up --build -d
   ```

2. Cargar equipos y jugadores:

   ```bash
   curl -X POST localhost:8080/teams -d '{"name":"Tigres"}'
   curl -X POST localhost:8080/teams -d '{"name":"Leones"}'
   curl -X POST localhost:8080/teams -d '{"name":"Halcones"}'
   curl -X POST localhost:8080/players -d '{"name":"Juan Pérez","team_id":1}'
   curl -X POST localhost:8080/players -d '{"name":"Carlos Gómez","team_id":2}'
   curl -X POST localhost:8080/players -d '{"name":"Pedro Ruiz","team_id":2}'
   ```

3. Cargar un partido ya jugado, para que la tabla no arranque vacía:

   ```bash
   docker compose run --rm -q app1 match add 1 3 2 0
   ```

4. Abrir **dos pestañas** en http://localhost:8080 y una **terminal** en la
   carpeta del proyecto.

## Bloque 0 · Qué cambió y por qué (1:00)

**Qué decir:**

> FutGo arma equipos y anota resultados de partidos. En la entrega anterior
> el foco era la infraestructura: Nginx repartiendo pedidos entre 3 réplicas.
> En esta entrega aplicamos CQRS, Event Sourcing e idempotencia. Para que
> tuvieran un sentido real, cambiamos cómo se registra un partido: antes se
> cargaba solo el resultado final; ahora se carga en vivo, evento por evento,
> y con eso se arma la tabla de posiciones.

**Versión anterior vs. ahora:**

| | Antes | Ahora |
|---|---|---|
| Partido | Una fila con el resultado final (`3 - 1`) | Una secuencia de eventos: inicio, goles, tarjetas, anulaciones, final |
| Corregir un gol | Borrar el partido y crearlo de nuevo | Agregar un evento "gol anulado" |
| Borrar un partido | Se borraba el dato | Se agrega un evento "partido cancelado" |
| Tabla de posiciones | No existía | Se calcula a partir de los eventos |
| Minuto de un evento | No existía | Lo fija el reloj del servidor y nunca retrocede |
| Pedido repetido (doble click, reintento) | Creaba un duplicado | Se aplica una sola vez |
| Dos usuarios editando a la vez | El último pisaba al primero | El segundo recibe un aviso de conflicto |

> Aclaración honesta: para una app de este tamaño un CRUD alcanzaba. Usamos
> estos patrones para mostrarlos funcionando sobre una arquitectura real de
> varias réplicas.

## Bloque 1 · Nginx y las 3 réplicas (2:00)

**Qué mostrar:**

1. En la interfaz, el indicador **Atiende appN** arriba a la derecha. Cambia
   cada 3 segundos porque cada consulta cae en una réplica distinta.
2. En la terminal, tirar una réplica y ver que nadie lo nota:

   ```bash
   docker compose stop app2
   for i in $(seq 1 12); do curl -s -o /dev/null -w "%{http_code} " localhost:8080/health; done
   docker compose start app2
   ```

   Son todas `200`, y el indicador solo muestra `app1` y `app3` mientras
   `app2` está caída.

**Qué decir:**

> Nginx reparte en round robin. Las réplicas no guardan estado propio: todo
> vive en Redis. Por eso cualquier réplica puede atender cualquier pedido.
> Si una se cae, Nginx la saca de la rotación y reintenta contra otra.
>
> Este punto es la base de todo lo que sigue: como un pedido puede caer en
> cualquier réplica, todo lo que hagamos para evitar duplicados o conflictos
> tiene que vivir en Redis y no en la memoria de una réplica.

**Dónde está:** `nginx.conf` (`upstream`, `proxy_next_upstream`),
`docker-compose.yml`, cabecera `X-Instance` en `internal/api/api.go`.

## Bloque 2 · Un partido como eventos: Event Sourcing (2:30)

**Qué mostrar:**

1. Elegir Tigres vs Leones y tocar **Iniciar partido**. Aparece el marcador
   sobre la cancha.
2. Tocar **+10'**, después **Gol** de Tigres y elegir a Juan Pérez. Se ve la
   animación y el gol en la **planilla del partido**.
3. En la planilla, tocar **Anular** sobre ese gol. El marcador vuelve a 0-0,
   pero el gol sigue en la planilla, tachado.
4. En la terminal, mostrar la historia guardada (reemplazar `2` por el número
   de partido que muestra la interfaz):

   ```bash
   docker compose exec redis redis-cli XRANGE gepa:match:2:events - +
   ```

**Qué decir:**

> Event Sourcing significa guardar **lo que pasó** en vez de **cómo quedó**.
> El marcador no está guardado en ningún lado del lado de escritura: se
> calcula recorriendo los eventos del partido. Eso se llama *replay*.
>
> Un gol anulado no se borra: se agrega un evento que lo compensa. La
> historia queda completa. Lo mismo pasa al cancelar un partido.
>
> Los eventos se guardan en un **stream de Redis**: una estructura donde solo
> se agrega al final. Cada partido tiene su propio stream.

**Por qué:** guarda la historia completa, permite corregir sin perder datos y
permite reconstruir cualquier vista desde cero (se demuestra en el bloque 4).

**Dónde está:**

- `internal/model/match.go`: `Replay` (eventos a estado) y `Check` (reglas: no
  hay goles en un partido terminado, solo se anula un gol, etc.).
- `internal/store/matches.go`: `StartMatch`, `AppendEvent`, `CancelMatch`.
- En la interfaz: la planilla de cada partido y el número de versión (`v4`).

## Bloque 3 · El reloj del partido (1:30)

**Qué mostrar:**

1. Elegir **x60** y tocar **Correr**: la cancha se enciende, una pelota la
   recorre y el reloj avanza un minuto por segundo.
2. Tocar **Amarilla** de Leones, esperar unos segundos y elegir a Carlos
   Gómez. Queda registrada en el minuto en que se tocó el botón, no en el que
   se eligió el jugador.
3. En la terminal, intentar anotar un gol en el minuto 1 (`clock_sec` = 60):

   ```bash
   curl -s -X POST localhost:8080/matches/2/events -d '{"type":"goal","team_id":1,"clock_sec":60}'
   ```

   Responde `400`: *no se puede anotar antes del último evento*.

**Qué decir:**

> El reloj también son eventos: "arrancó a tal velocidad", "se pausó", "se
> adelantó 5 minutos". No hay un contador guardado que cambie cada segundo.
> Cualquier réplica calcula el minuto con la misma fórmula, y la interfaz lo
> hace avanzar sola.
>
> El cliente nunca dice "esto pasó en el minuto 33": el servidor sella cada
> evento con su reloj. Eso garantiza un **invariante**, una regla que se
> cumple siempre: el tiempo del partido solo avanza. Lo garantiza el
> servidor, así que ni siquiera con `curl` se puede romper.
>
> Desde DevOps, esto exige que las réplicas tengan la hora sincronizada con
> **NTP**. En Docker no hay problema porque usan el reloj del host.

**Dónde está:** `internal/model/match.go` (`ClockAt`, `Stamp`), cabecera
`X-Server-Time` en `internal/api/api.go`, `clockAt` en
`internal/web/assets/app.js`.

## Bloque 4 · Escribir y leer por separado: CQRS (2:30)

**Qué mostrar:**

1. Tocar **Finalizar** en el partido. La **tabla de posiciones** se actualiza.
2. En el **Registro de requests**, mostrar la línea *Lectura actualizada a vN
   en X ms*.
3. Mostrar que la tabla es descartable:

   ```bash
   docker compose exec redis redis-cli DEL gepa:read:standings gepa:read:counted
   ```

   La tabla queda en cero. Tocar **Reconstruir** en la tabla: vuelve
   exactamente igual, recalculada desde los eventos.
4. Mostrar que el proyector se reparte entre las réplicas:

   ```bash
   docker compose exec redis redis-cli XINFO CONSUMERS gepa:events projector
   ```

**Qué decir:**

> CQRS significa usar un modelo para **escribir** y otro para **leer**. Los
> comandos (iniciar partido, registrar gol) solo agregan eventos. Las
> consultas (la tabla, los marcadores) leen datos ya calculados.
>
> Entre los dos está el **proyector**: un proceso que corre dentro de cada
> réplica, lee los eventos nuevos y actualiza la tabla. Las 3 réplicas forman
> un *consumer group* de Redis: cada evento lo procesa una sola.
>
> El costo es la **consistencia eventual**: la tabla se actualiza unos
> milisegundos después del comando. Por eso la interfaz espera a que la
> lectura alcance la versión nueva, y lo muestra en el registro.
>
> CQRS y Event Sourcing son ideas distintas que se combinan bien: los eventos
> son la fuente de verdad y la tabla es una vista que se puede tirar y
> rearmar.

**Por qué:** escribir necesita validar reglas; leer necesita velocidad y
formas variadas (tabla, marcador). Separarlos deja optimizar cada lado.
Jugadores y equipos quedaron como CRUD simple: el patrón se aplica donde
aporta.

**Dónde está:**

- Comandos: `POST /matches`, `POST /matches/{id}/events`, `DELETE /matches/{id}`.
  Consultas: `GET /matches`, `GET /standings` (rutas en `internal/api/api.go`).
- `internal/store/projection.go`: `RunProjector`, `ProjectMatch`, `Rebuild`,
  `Standings`.
- `main.go`: `go s.RunProjector(...)` arranca el proyector en cada réplica.

## Bloque 5 · Pedidos repetidos: idempotencia (3:00)

**Qué mostrar:**

1. En el **Registro de requests**, activar **Simular reintento**. Iniciar un
   partido y registrar un gol. Cada comando sale dos veces con la misma key:
   la segunda figura como *repetida, no se aplicó*, muchas veces atendida por
   otra réplica. El marcador sube **uno**.
2. La misma key contra las 3 réplicas, directo:

   ```bash
   for port in 8081 8082 8083; do
     curl -s -D - -o /dev/null -X POST localhost:$port/matches \
       -H 'Idempotency-Key: demo-1' -d '{"team_a_id":2,"team_b_id":3}' \
       | grep -iE 'x-instance|idempotent-replayed'
   done
   ```

   Solo la primera crea el partido; las otras dos responden
   `Idempotent-Replayed: true`.
3. **Concurrencia optimista.** Sobre el partido en vivo del paso 1 (en este
   guion, el #3), mirar su versión en la interfaz (por ejemplo `v2`) y mandar
   dos veces el mismo pedido con esa versión:

   ```bash
   curl -s -X POST localhost:8080/matches/3/events -d '{"type":"yellow_card","team_id":1,"expected_version":2}'
   curl -s -X POST localhost:8080/matches/3/events -d '{"type":"yellow_card","team_id":1,"expected_version":2}'
   ```

   El primero entra; el segundo responde `409` con la versión actual.

**Qué decir:**

> Una operación es **idempotente** si da lo mismo ejecutarla una vez o diez.
> Con varias réplicas, los pedidos se repiten: doble click, el navegador
> reintenta tras un corte, un timeout de Nginx después de que el backend ya
> guardó.
>
> Usamos dos estrategias:
>
> - **Idempotency key:** el cliente le pone un ID a cada acción. Si el mismo ID
>   llega dos veces, el servidor devuelve la respuesta guardada sin volver a
>   ejecutar. Así se distingue un reintento de dos partidos reales entre los
>   mismos equipos: son acciones distintas, con keys distintas.
> - **Versión esperada:** el cliente dice "lo decidí viendo la versión 5". Si
>   el partido ya cambió, se rechaza. Esto además evita que dos usuarios se
>   pisen.
>
> Las keys viven en **Redis y no en la memoria de una réplica**: con round
> robin el reintento cae en otra réplica, y esa tiene que saber que ya se
> procesó. Por eso Nginx, por defecto, no reintenta un POST: no sabe si el
> backend lo aplicó. El reintento seguro lo hace el cliente, con la misma key.
>
> El proyector también es idempotente: Redis Streams puede entregar un evento
> dos veces, y la tabla no puede sumar los puntos dos veces.

**Dónde está:**

- `internal/api/idempotency.go`: middleware aplicado a todo `POST`/`DELETE`.
- `internal/store/idempotency.go`: `SET NX` en Redis, respuesta guardada 24 h.
- `internal/web/assets/app.js`: `command` (una key por acción) y
  `sendWithRetry` (reintenta con la misma key).
- `internal/store/projection.go`: `standingsScript` (suma cada partido una sola
  vez) y `saveMatchScript` (no pisa una versión nueva con una vieja).

## Bloque 6 · Race conditions (1:00)

**Qué decir:**

> Una **race condition** es un error que depende de qué proceso llega
> primero. El caso típico es el **check-then-act**: verificar y después
> actuar en dos pasos. Con 3 réplicas, dos pueden leer el partido en la
> versión 5 y escribir las dos como si fueran la 6.
>
> Lo evitamos con un **script Lua** de Redis que chequea la versión y agrega
> el evento en una sola operación atómica: nadie se mete en el medio.
>
> Dejamos un caso sin resolver a propósito para mostrarlo: borrar un equipo
> sigue siendo un check-then-act.

**Dónde está:** `appendScript` en `internal/store/matches.go`. El caso
pendiente es `DeleteTeam` en `internal/store/store.go`.

## Bloque 7 · Cierre (1:00)

**Qué decir:**

> - **Event Sourcing** nos dio historia completa y vistas reconstruibles.
> - **CQRS** separó la validación de la lectura rápida, a cambio de
>   consistencia eventual.
> - **Idempotencia** hace que los reintentos, inevitables con varias
>   réplicas, sean seguros.
>
> Todo corre con un solo Redis, que sigue siendo un punto único de falla. El
> siguiente paso sería Redis con réplicas (Sentinel o Cluster).
>
> Todo está cubierto por tests que corren en CI en cada push, incluidos los
> casos de reintento, conflicto de versión y reloj.

## Mapa de conceptos

| Concepto | Dónde se ve en la app | Dónde está implementado | Teoría |
|---|---|---|---|
| Round robin | Indicador *Atiende appN* | `nginx.conf` | README, sección Nginx |
| Tolerancia a fallos | `docker compose stop app2` | `nginx.conf` (`proxy_next_upstream`) | README, sección Tolerancia a fallos |
| Event Sourcing | Planilla del partido, gol anulado tachado | `model/match.go` (`Replay`), `store/matches.go` | [teoria.md §1](teoria.md#1-event-sourcing) |
| Evento compensatorio | Anular gol, cancelar partido | `goal_annulled`, `match_cancelled` | [teoria.md §1](teoria.md#1-event-sourcing) |
| Reloj como evento / invariante | Reloj del marcador, `+1' +5' +10'`, `curl` con `clock_sec` | `model/match.go` (`ClockAt`, `Stamp`) | [teoria.md §5](teoria.md#5-el-tiempo-del-partido-también-es-un-evento) |
| CQRS | Tabla de posiciones | `api/api.go` (rutas), `store/projection.go` | [teoria.md §2](teoria.md#2-cqrs-command-query-responsibility-segregation) |
| Proyector y consumer group | `XINFO CONSUMERS` | `store/projection.go` (`RunProjector`) | [tecnica.md](tecnica.md#proyector) |
| Consistencia eventual | *Lectura actualizada a vN en X ms* | `app.js` (`waitForVersion`) | [teoria.md §2](teoria.md#2-cqrs-command-query-responsibility-segregation) |
| Rebuild | Botón *Reconstruir* | `store/projection.go` (`Rebuild`) | [teoria.md §1](teoria.md#1-event-sourcing) |
| Idempotency key | Toggle *Simular reintento*, `curl` a 3 réplicas | `api/idempotency.go`, `store/idempotency.go` | [teoria.md §3](teoria.md#3-idempotencia) |
| Concurrencia optimista | `409` con `expected_version` | `store/matches.go` (`appendScript`) | [teoria.md §3](teoria.md#3-idempotencia) |
| Proyector idempotente | La tabla no duplica puntos | `store/projection.go` (`standingsScript`) | [teoria.md §3](teoria.md#3-idempotencia) |
| Race condition | (explicado) | `appendScript`; pendiente en `DeleteTeam` | [teoria.md §4](teoria.md#4-race-conditions-y-check-then-act) |

## Si algo falla en vivo

| Problema | Qué hacer |
|---|---|
| La interfaz no carga | `docker compose ps`; si falta algo, `docker compose up -d` |
| Un `curl` usa un número de partido que no existe o ya terminó | Mirar el número en la interfaz (*Partido #N*) y reemplazarlo |
| El `409` no aparece | Usar la versión que muestra la interfaz en ese momento (`vN`) |
| La tabla no se actualiza | Tocar **Reconstruir**: la recalcula desde los eventos |

## Preguntas probables

**¿Por qué no usar una base de datos para escribir y otra para leer?**
CQRS separa modelos, no necesariamente bases. Acá los dos viven en el mismo
Redis con claves distintas. Separarlos en servidores es un paso posterior,
cuando la escala lo pide.

**¿Qué pasa si una réplica se cae a mitad de proyectar un evento?**
El evento queda pendiente en el consumer group. A los 30 segundos otra
réplica lo reclama (`XAUTOCLAIM`) y lo procesa. Como la proyección es
idempotente, procesarlo de nuevo no rompe nada.

**¿Por qué no un límite de "un partido A vs B por día" para evitar duplicados?**
Bloquearía partidos reales y dejaría pasar duplicados fuera de la ventana. La
idempotency key identifica la acción, no el contenido.

**¿Cuánto tiempo se guardan las keys?**
24 horas. Si la réplica se cae a mitad del pedido, la reserva se libera sola a
los 30 segundos para que el cliente pueda reintentar.
