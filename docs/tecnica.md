# Documentación técnica: partidos con eventos, tabla e idempotencia

Cómo está implementado. Los conceptos (qué es CQRS, idempotencia, etc.) y el
porqué de cada decisión están en [teoria.md](teoria.md).

## Mapa de archivos

| Archivo | Responsabilidad |
|---|---|
| `internal/model/match.go` | Tipos de evento, `Replay` (eventos a estado), `Check` (reglas de negocio), `StandingDeltas`. Lógica pura, sin Redis |
| `internal/store/matches.go` | **Lado de escritura**: `StartMatch`, `AppendEvent`, `CancelMatch`, `LoadMatch`. Script Lua de append con versión |
| `internal/store/projection.go` | **Lado de lectura**: proyector (`RunProjector`, `ProjectMatch`), `Rebuild`, consultas `ListMatches` y `Standings` |
| `internal/store/idempotency.go` | Registro de idempotency keys en Redis (`IdemBegin`, `IdemSave`, `IdemRelease`) |
| `internal/api/idempotency.go` | Middleware HTTP que aplica la idempotency key a todo `POST`/`DELETE` |
| `internal/api/api.go` | Rutas y handlers |
| `main.go` | Arranca el proyector en cada réplica (`serve`); comandos CLI `standings` y `rebuild` |
| `internal/web/assets/*` | Interfaz: partido en vivo, tabla, registro de requests |

## Claves de Redis

**Escritura (fuente de verdad):**

| Clave | Tipo | Contenido |
|---|---|---|
| `gepa:match:{id}:events` | Stream | Eventos de un partido, en orden. Campo `event` con el JSON. Su largo (`XLEN`) es la versión |
| `gepa:events` | Stream | Feed global: una entrada `{match_id}` por cada evento agregado. Lo consume el proyector |
| `gepa:match:ids` | Set | IDs de todos los partidos |
| `gepa:seq:match` | String | Contador (`INCR`) para IDs de partido |

**Lectura (descartables, se rearman con rebuild):**

| Clave | Tipo | Contenido |
|---|---|---|
| `gepa:read:matches` | Hash | `id` → partido proyectado en JSON (marcador, estado, relato) |
| `gepa:read:match_versions` | Hash | `id` → versión proyectada |
| `gepa:read:standings` | Hash | `{equipo}:{campo}` → número (`played`, `won`, `points`...) |
| `gepa:read:counted` | Set | Partidos ya sumados a la tabla |

**Idempotencia:**

| Clave | Tipo | Contenido |
|---|---|---|
| `gepa:idem:{key}` | String | `{fingerprint, status, body}`. TTL 30 s mientras está en curso, 24 h ya resuelta |

Jugadores y equipos no cambiaron (`gepa:players`, `gepa:teams`). La clave
vieja `gepa:matches` (partidos con resultado fijo) ya no se usa.

## API

| Método | Ruta | Tipo | Descripción |
|---|---|---|---|
| POST | `/matches` | Comando | Inicia un partido `{team_a_id, team_b_id}`. Responde `201 {match_id, version}` |
| POST | `/matches/{id}/events` | Comando | Agrega un evento. Responde `201 {match_id, version, seq}` |
| DELETE | `/matches/{id}` | Comando | Cancela (agrega `match_cancelled`). Idempotente: siempre `204` |
| GET | `/matches` | Consulta | Partidos no cancelados, con marcador, estado, versión y relato |
| GET | `/standings` | Consulta | Tabla de posiciones ordenada por puntos, DG y GF |
| POST | `/projections/rebuild` | Admin | Borra los modelos de lectura y los recalcula desde los eventos |

Body de `POST /matches/{id}/events`:

```json
{
  "type": "goal",
  "player_id": 1,
  "team_id": 0,
  "clock_sec": 1140,
  "expected_version": 4
}
```

- `type`: `goal`, `yellow_card`, `red_card`, `goal_annulled`, `match_finished`,
  `match_cancelled`, `clock_started`, `clock_paused`, `clock_advanced`.
- **El cliente no manda el minuto.** El servidor lo fija con el reloj del
  partido (ver [Reloj del partido](#reloj-del-partido)).
- `clock_sec`: opcional. Fija el evento en un instante ya transcurrido (el que
  marcaba el reloj cuando se tocó el botón). Tiene que estar entre el último
  evento y el reloj actual; si no, `400`.
- `speed`: solo para `clock_started`, segundos de partido por segundo real
  (1 a 600).
- `advance_sec`: solo para `clock_advanced`, cuánto adelantar (mayor que 0).
- `player_id` o `team_id`: si viene el jugador, el equipo se deduce de él.
- `ref_seq`: solo para `goal_annulled`, el `seq` del gol a anular.
- `expected_version`: opcional. Si no coincide, `409 {"error", "current_version"}`.
- El resultado de `match_finished` lo calcula el servidor; el cliente no lo manda.

Cabeceras de idempotencia (cualquier `POST`/`DELETE`):

| Cabecera | Dirección | Significado |
|---|---|---|
| `Idempotency-Key` | Request | ID de la acción (máx. 128 caracteres) |
| `Idempotent-Replayed: true` | Response | La respuesta es la guardada; no se volvió a ejecutar |

Respuestas especiales: `409` si la misma key sigue en curso, `422` si la key
se reusó con otro body o ruta.

## Flujo de un gol

1. La interfaz genera una key y manda `POST /matches/7/events` con
   `expected_version: 4`. Nginx lo manda, por ejemplo, a `app2`.
2. **Middleware:** `SET NX gepa:idem:{key}`. Es nueva, sigue.
3. **Comando:** `LoadMatch` lee el stream y hace `Replay`; la versión es 4,
   coincide. `Check` valida que el partido esté en juego y el equipo juegue.
4. **Append atómico (Lua):** si `XLEN` sigue en 4, `XADD` al stream del
   partido, `XADD` al feed y `SADD` al set de IDs. Responde `201 {version: 5}`.
5. **Middleware:** guarda la respuesta junto a la key.
6. **Proyector** (en cualquier réplica, por el consumer group): lee el feed,
   rearma el partido 7 y lo guarda si la versión 5 es más nueva. Si el
   partido estaba terminado, suma a la tabla (una sola vez). Hace `XACK`.
7. **Interfaz:** consulta `GET /matches` hasta ver la versión 5 y redibuja.

## Reloj del partido

El reloj también es Event Sourcing: no hay un contador que se actualice cada
segundo, sino eventos que dicen cuándo arrancó, a qué velocidad y cuándo se
pausó o adelantó.

| Evento | Efecto |
|---|---|
| `clock_started {speed}` | El reloj corre solo a `speed` segundos de partido por segundo real (la interfaz ofrece x10, x30, x60 y x120) |
| `clock_paused` | Se detiene donde está |
| `clock_advanced {advance_sec}` | Salta hacia adelante (+1', +5', +10'). Nunca hacia atrás |
| `match_finished` / `match_cancelled` | Lo detienen |

El estado proyectado guarda `clock_sec` (tiempo acumulado), `clock_running`,
`clock_speed` y `clock_since` (desde qué hora real corre). El tiempo actual es:

```
clock_sec + (ahora - clock_since) × clock_speed     (tope: 130 minutos)
```

Lo calculan igual el servidor (`model.Match.ClockAt`) y la interfaz
(`clockAt` en `app.js`). Por eso el modelo de lectura no cambia mientras el
reloj corre, y la interfaz lo hace avanzar sola cuatro veces por segundo.

**Reglas (en `model.Match.Stamp` y `Check`):**

- Cada evento recibe `clock_sec` y `minute` del reloj del **servidor** en el
  momento en que llega.
- Nada puede quedar antes del último evento (`last_event_sec`) ni después del
  reloj actual. El tiempo del partido solo avanza.
- Al tocar *Gol* o una tarjeta, la interfaz congela el minuto y abre el
  selector de jugador. El evento se registra en ese minuto aunque el reloj
  haya seguido corriendo mientras se elegía. Si en el medio se cargó un
  evento posterior, el servidor lo rechaza.

**Hora del servidor:** cada respuesta trae `X-Server-Time` (ms). La interfaz
la usa para calcular el reloj con la hora del servidor y no con la de la
computadora del usuario. Las 3 réplicas tienen que tener la hora
sincronizada (NTP). En Docker la comparten con el host.

## Proyector

- Corre como goroutine dentro de cada réplica (`go s.RunProjector(...)` en
  `runServe`). Consumer group `projector`, un consumidor por réplica con el
  nombre de la instancia.
- Lee con `XREADGROUP ... BLOCK 5000` y confirma con `XACK` después de
  proyectar. Si falla, no confirma y el mensaje queda pendiente.
- Cada vuelta hace `XAUTOCLAIM` de mensajes pendientes hace más de 30 s: si
  una réplica se cayó a mitad, otra toma su trabajo.
- El grupo se crea desde el inicio del feed (`0`): al arrancar por primera vez
  reprocesa la historia, que es seguro porque la proyección es idempotente.
- Dos scripts Lua garantizan que sea idempotente y seguro en paralelo:
  `saveMatchScript` (solo guarda una versión más nueva) y `standingsScript`
  (suma o resta un partido a la tabla una sola vez, usando `SADD`/`SREM`).

## Interfaz web

Organización de la pantalla, en el orden en que se usa durante un partido:

| Zona | Qué muestra | Por qué está ahí |
|---|---|---|
| Barra superior (fija) | Marca, accesos a cada sección e indicador **Atiende appN** | El indicador rota con cada respuesta (cabecera `X-Instance`): es el round robin en vivo, siempre visible |
| **En vivo** (columna principal, arriba) | Formulario *local vs visitante* para iniciar el partido y un **marcador LED** por partido en juego | Es la tarea principal: cargar eventos del partido |
| Marcador | Estado, versión (`v4`), cancelar/finalizar, marcador LED y **reloj del partido** (mm:ss) | Todo lo de un partido en un mismo bloque; la versión visible ayuda a explicar la concurrencia optimista |
| Cancha de fondo | El marcador en vivo está sobre una cancha dibujada. En pausa se ve apagada; con el reloj corriendo se enciende y una pelota la recorre | Hace visible si la simulación está en marcha |
| Animaciones | Gol: la pelota va al arco rival, el arco se ilumina y aparece "¡GOOOL!" con autor y minuto. Tarjeta: sube girando del lado del equipo, con el jugador. Gol anulado: "VAR" tachado | Se disparan con cada evento **nuevo** que llega al modelo de lectura, así que también se ven en otras pestañas. Con *reducir movimiento* activado se muestran quietas |
| Mesa de control | Reloj: *Correr/Pausar* con velocidad (x10 a x120) y *Adelantar* +1', +5', +10'. Debajo, un bloque por equipo con Gol, Amarilla y Roja | Los controles de tiempo arriba de las acciones, porque el minuto de cada acción sale del reloj |
| Selector de jugador | Al tocar Gol o una tarjeta: el minuto congelado, los jugadores del equipo, *Sin especificar* y un campo para sumar un jugador nuevo en el momento | Responde quién hizo el gol o recibió la tarjeta sin salir del partido |
| **Resultados** | Partidos finalizados, ganador resaltado, planilla plegada | Historia consultable sin ocupar lugar |
| **Planteles** (sobre el césped) | Una tarjeta por equipo, con alta de jugador en la misma tarjeta, y una tarjeta *Nuevo equipo* | Cada alta está donde se usa, sin elegir el equipo de un desplegable |
| **Tabla** (columna lateral, fija al scrollear) | `GET /standings`, líder resaltado, DG en color y botón *Reconstruir* | Se ve cómo cambia mientras se cargan partidos |
| **Registro** (columna lateral) | Cada comando con método, ruta, status, réplica, key y si fue repetido; toggle *Simular reintento* | Es la evidencia de idempotencia y round robin para la demo |

En pantallas angostas queda en una sola columna con este orden: en vivo,
tabla, resultados, planteles y registro. Los errores aparecen como un aviso
flotante abajo a la derecha que se cierra solo.

Comportamiento:

- **Toggle "Simular reintento":** manda cada comando dos veces con la misma
  key. La segunda aparece como "repetida, no se aplicó", muchas veces desde
  otra réplica.
- **Reintentos automáticos:** ante un error de red o un `502/503/504`, la
  interfaz reintenta hasta 3 veces con la misma key.
- **Consistencia eventual visible:** después de cada comando espera a que la
  lectura llegue a la versión nueva y lo anota en el registro.
- **Polling cada 3 s:** otras pestañas ven los cambios. Solo se redibuja si
  algo cambió, y lo que el usuario está escribiendo (nombres, velocidad
  elegida, selector abierto) se conserva entre redibujos.

## CLI

```bash
gepa match add 1 2 3 1   # carga un partido ya jugado: genera inicio, 4 goles y cierre
gepa match list          # lee el modelo de lectura (muestra estado y versión)
gepa match del 3         # cancela (evento compensatorio)
gepa standings           # tabla de posiciones
gepa rebuild             # rearma la tabla desde los eventos
```

La CLI no tiene un proyector corriendo, así que después de escribir llama a
`ProjectMatch` ella misma. Si un servidor lo proyecta de nuevo, no cambia nada.

## Guion de demo

Está en [demo.md](demo.md): qué decir y mostrar en cada bloque de la
presentación, con el mapa de dónde se ve e implementa cada concepto.

## Tests

`go test ./... -race` cubre:

- Partido en vivo hasta la tabla, incluido un gol anulado y el resultado
  "pasado en limpio" (`store/matches_test.go`).
- Proyección idempotente: proyectar 3 veces no suma de más.
- Cancelar un partido terminado lo descuenta de la tabla, y cancelar dos veces
  no resta de más.
- Versión vieja: `409` y el evento no se guarda.
- Rebuild: la tabla reconstruida es idéntica a la original.
- Reloj (`model/match_test.go`): avance manual, reloj automático a x60,
  pausa, tope de 130', no anotar antes del último evento ni en el futuro, y
  avance solo positivo. En la API, un gol con `clock_sec` anterior al último
  evento devuelve `400`.
- Idempotency key en la API: el reintento devuelve la misma respuesta y no
  duplica; otra key sí crea otro partido; otro body con la misma key da `422`;
  si falla, la key se libera (`api/api_test.go`).

## Limitaciones conocidas

- `DeleteTeam` sigue siendo un check-then-act (ver teoria.md, sección 4).
- `teamHasMatches` recorre los eventos de todos los partidos: alcanza a esta
  escala; con muchos partidos haría falta un índice equipo → partidos.
- El feed `gepa:events` no se recorta nunca (`XTRIM`): crece con cada evento.
- El rebuild no está protegido con autenticación: es un endpoint de demo.
