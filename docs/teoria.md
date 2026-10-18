# Documentación teórica: CQRS, Event Sourcing e idempotencia

Este documento explica **los conceptos** y **por qué** los aplicamos en FutGo.
El detalle de cómo está implementado (claves de Redis, endpoints, archivos)
está en [tecnica.md](tecnica.md).

> Aclaración honesta: para una app de este tamaño, un CRUD alcanzaba. Usamos
> estos patrones porque el objetivo del trabajo es mostrarlos funcionando en
> un caso donde se entienden y se pueden probar, sobre una arquitectura real
> de varias réplicas detrás de Nginx.

## Glosario rápido

| Término | Qué es | Para qué sirve |
|---|---|---|
| **Réplica** | Una de las 3 copias idénticas de la app (`app1`, `app2`, `app3`) | Repartir la carga y tolerar caídas |
| **Round robin** | Nginx manda cada request a la réplica siguiente, en orden | Repartir la carga de forma pareja |
| **Evento** | Un hecho que ya ocurrió y no cambia ("gol de Messi a los 33'") | Guardar la historia completa |
| **Event store** | El lugar donde se guardan los eventos, en orden y sin editar | Ser la fuente de verdad |
| **Stream de Redis** | Un registro de Redis donde solo se agregan entradas al final, cada una con un ID creciente | Implementar el event store y la cola del proyector |
| **Comando** | Una operación que pide cambiar algo ("registrar un gol") | Escribir |
| **Consulta (query)** | Una operación que solo lee ("dame la tabla") | Leer |
| **Modelo de lectura** | Datos ya calculados y guardados para responder rápido una consulta | Que leer sea barato |
| **Proyector** | Proceso que lee eventos y actualiza los modelos de lectura | Traducir el formato de escritura al de lectura |
| **Replay** | Volver a aplicar todos los eventos desde el primero | Reconstruir el estado |
| **Consistencia eventual** | La lectura refleja la escritura unos milisegundos después, no al instante | Es el costo de separar escritura y lectura |
| **Idempotente** | Una operación que da el mismo resultado si se ejecuta 1 o 10 veces | Que los reintentos sean seguros |
| **Idempotency key** | Un ID único que el cliente le pone a cada acción | Detectar que una request es un reintento |
| **Race condition** | Error que depende de qué proceso llega primero cuando dos trabajan a la vez | Lo que hay que evitar con varias réplicas |
| **Check-then-act** | Patrón de dos pasos: verificar y después actuar | Es la causa típica de race conditions |
| **Concurrencia optimista** | El cliente dice "esperaba la versión N"; si ya cambió, se rechaza | Evitar que dos escrituras se pisen sin usar bloqueos |
| **Operación atómica** | Una operación que se ejecuta entera o no se ejecuta, sin que nada se meta en el medio | Eliminar la ventana del check-then-act |
| **Consumer group** | Grupo de lectores de un stream donde cada mensaje se entrega a uno solo | Repartir el trabajo del proyector entre las réplicas |
| **At-least-once** | Garantía de entrega "al menos una vez": un mensaje puede llegar repetido | Es lo que da Redis Streams; obliga a que el proyector sea idempotente |
| **Invariante** | Regla que el sistema garantiza siempre, venga el pedido de donde venga | Acá: el tiempo del partido solo avanza |
| **NTP** | Protocolo que sincroniza la hora de las máquinas | Que todas las réplicas sellen los eventos con la misma hora |
| **Desnormalizar** | Guardar datos repetidos y ya combinados en un solo registro | Leer sin *joins* (combinar tablas), a costa de duplicar |
| **CDC (Change Data Capture)** | Leer el log interno de cambios de una base (ej. el WAL de PostgreSQL) y avisar cada fila modificada | Sincronizar un modelo de lectura sin tocar el código que escribe |
| **Outbox** | Tabla donde, en la misma transacción que el cambio, se anota "cambió X" | Que ningún aviso de sincronización se pierda |
| **OLTP / OLAP** | Bases para muchas transacciones chicas / para consultas analíticas grandes | Escritura operativa / análisis de negocio |
| **ETL / ELT** | Proceso que extrae, transforma y carga datos de un sistema a otro | Alimentar un data warehouse |
| **Fan-out** | "Repartir" un dato a muchos destinos | Armar los timelines de una red social |

## 1. Event Sourcing

**Qué es:** guardar como fuente de verdad la **lista de eventos** que
ocurrieron, en vez del estado actual. El estado se calcula aplicando los
eventos en orden.

**Antes:** un partido era una fila con el resultado final (`3 - 1`). Para
corregir un gol había que borrar y recrear, y se perdía qué pasó.

**Ahora:** un partido es su historia:

```
v1  match_started   Tigres vs Leones
v2  goal            Pérez (Tigres) 23'
v3  yellow_card     Leones 30'
v4  goal            Tigres 41'
v5  goal_annulled   anula el evento 4
v6  match_finished  resultado 1 - 0
```

El marcador `1 - 0` no está guardado en ningún lado del lado de escritura:
se calcula recorriendo los eventos (función `Replay`).

**Por qué lo usamos:**

- **Historia completa:** un gol anulado no desaparece, queda un evento que lo
  anula (*evento compensatorio*). Lo mismo pasa con cancelar un partido.
- **Reconstrucción:** como los eventos nunca se borran, cualquier vista
  derivada (como la tabla) se puede tirar y volver a calcular. El botón
  "Reconstruir" de la tabla lo demuestra.
- **Cierre del partido:** el evento `match_finished` guarda el resultado
  final calculado por el servidor. Es el "pasar en limpio" del partido.

## 2. CQRS (Command Query Responsibility Segregation)

**Qué es:** usar un modelo para **escribir** y otro distinto para **leer**.

CQRS y Event Sourcing son **ideas independientes** que se combinan bien:

| | Ejemplo |
|---|---|
| CQRS sin Event Sourcing | Escribir en tablas normales y copiar a un índice de búsqueda |
| Event Sourcing sin CQRS | Guardar eventos y recalcular el estado en cada lectura |
| Los dos juntos (nuestro caso) | Guardar eventos y que un proyector arme las vistas de lectura |

**Por qué separar:** escribir y leer tienen necesidades distintas.

- **Escribir** necesita validar reglas ("no se puede meter un gol en un
  partido terminado"). Conviene una estructura simple y estricta: los eventos.
- **Leer** necesita velocidad y formas variadas: tabla de posiciones,
  marcador en vivo. Conviene una estructura ya calculada para cada pantalla.

**Cómo queda en FutGo:**

```
              COMANDOS (escritura)                    CONSULTAS (lectura)
  POST /matches, POST /matches/{id}/events      GET /matches, GET /standings
                 │                                         ▲
                 ▼                                         │
       stream del partido  ──►  feed de eventos  ──►  PROYECTOR  ──►  modelos de lectura
       (fuente de verdad)       (cola)                (en cada réplica)  (tabla, marcadores)
```

No hacen falta dos bases de datos distintas: son **dos modelos**, en este
caso en el mismo Redis con claves distintas. Separarlos en servidores o
tecnologías distintas es un paso posterior, cuando la escala lo pide.

**El costo, consistencia eventual:** el comando responde apenas guarda el
evento; la tabla se actualiza unos milisegundos después, cuando el proyector
lo procesa. La interfaz lo maneja esperando a que la lectura llegue a la
versión que devolvió el comando, y lo muestra en el registro ("lectura
actualizada a v4 en 3 ms").

**Lo que NO tiene CQRS:** jugadores y equipos siguen siendo CRUD simple. No
todo el sistema necesita el patrón; se aplica donde aporta.

## 3. Idempotencia

**Qué es:** una operación es idempotente si ejecutarla una o varias veces deja
el mismo resultado. "Poner el marcador en 2-1" es idempotente; "sumar un gol"
no lo es.

**Por qué importa acá:** con varias réplicas y una red real, las requests se
repiten: doble click, el navegador reintenta tras un corte, un timeout de
Nginx después de que el backend ya guardó. Sin protección, cada repetición
crea un partido o un gol de más.

### Dos estrategias complementarias

| | Idempotency key | Versión esperada |
|---|---|---|
| Qué manda el cliente | `Idempotency-Key: <uuid>` | `"expected_version": 3` |
| Qué identifica | **Un intento de acción** | **El estado que vio el cliente** |
| Ante un duplicado | Devuelve la respuesta original (éxito) | Rechaza con `409 Conflict` |
| Dónde la usamos | Todos los `POST` y `DELETE` | Eventos de un partido |
| Problema extra que resuelve | — | Dos usuarios pisándose |

#### Idempotency key

La regla: **una acción del usuario = una key; un reintento de esa misma
acción = la misma key.**

Así se distingue un error de un caso real sin adivinar ni poner límites de
tiempo:

| Situación | Keys | Resultado |
|---|---|---|
| Dos partidos reales Tigres vs Leones el mismo día | `k1`, `k2` | 2 partidos |
| Reintento del navegador tras un corte | `k1`, `k1` | 1 partido |
| Dos goles en 10 segundos | `k1`, `k2` | 2 goles |

Un cooldown ("máximo un partido A vs B por día") sería peor: bloquea casos
reales y deja pasar duplicados fuera de la ventana.

Funcionamiento en el servidor:

1. Llega la request con la key. El servidor hace `SET NX` en Redis
   ("guardá solo si no existe"), que es **atómico**: si dos réplicas reciben
   la misma key a la vez, solo una gana.
2. Si ganó, procesa la request y guarda la respuesta junto a la key (24 h).
3. Si la key ya existía, **no procesa nada** y devuelve la respuesta
   guardada, con la cabecera `Idempotent-Replayed: true`.
4. Si la misma key llega con otro body, responde `422`: es un error del
   cliente, no un reintento.
5. Si la request falló (4xx/5xx), libera la key: no hubo efectos, así que
   reintentar es seguro.

**Decisión clave:** las keys viven en **Redis y no en la memoria de cada
réplica**. Con round robin, el reintento probablemente caiga en otra réplica,
y esa tiene que saber que la primera ya lo procesó. La demo lo muestra: la
misma key mandada a `app1`, `app2` y `app3` crea un solo partido.

**Relación con Nginx:** Nginx, por defecto, no reintenta un `POST` contra otra
réplica si ya llegó a enviarlo, porque no puede saber si el backend lo aplicó.
Ese reintento queda del lado del cliente, que lo hace seguro con la misma key.

#### Versión esperada (concurrencia optimista)

Cada partido tiene una **versión**: la cantidad de eventos que tiene. El
cliente no necesita saber qué eventos hubo, solo cuántos. Al mandar un evento
dice "lo decidí viendo la versión 3".

- Si el partido sigue en la versión 3, el evento entra y pasa a la 4.
- Si ya está en la 4 (otro usuario cargó algo), responde `409` con la versión
  actual. La interfaz refresca el partido y avisa.

La versión dice *que* el estado cambió, no *quién* lo cambió. Por eso
complementa a la key en vez de reemplazarla.

**No es un snapshot:** un snapshot es una "foto" del estado completo que se
guarda para no hacer replay desde cero en partidos con miles de eventos. Es
una optimización del servidor, no tiene que ver con idempotencia, y acá no
hace falta.

#### El proyector también es idempotente

Redis Streams entrega los mensajes **al menos una vez**: si una réplica
procesa un evento y se cae antes de confirmarlo, otra lo va a procesar de
nuevo. Si el proyector simplemente sumara 3 puntos por cada mensaje, la tabla
se duplicaría.

Lo resolvimos así:

- **Marcador del partido:** el proyector no suma goles uno por uno. Rearma el
  partido entero desde sus eventos (`Replay`) y lo guarda solo si su versión
  es más nueva que la guardada. Proyectarlo dos veces da lo mismo.
- **Tabla:** al sumar un partido terminado, lo anota en un set de "partidos
  ya contados" dentro de la misma operación atómica. Si ya estaba, no suma.

Esta misma propiedad es la que permite que la CLI proyecte por su cuenta y que
el rebuild conviva con el proyector sin duplicar nada.

## 4. Race conditions y check-then-act

**Check-then-act:** verificar una condición y después actuar en un paso
separado. Con varias réplicas, entre el "check" y el "act" otra réplica puede
cambiar el estado.

**Ejemplo que evitamos:** dos réplicas leen el partido en la versión 3 y las
dos escriben un evento como la versión 4. Uno de los dos se perdería o el
orden quedaría inconsistente.

**Cómo lo evitamos:** el chequeo de versión y la escritura del evento están en
**un solo script Lua** de Redis. Redis ejecuta cada script de forma atómica,
así que no hay ventana entre el check y el act.

**Lo que queda sin resolver (a propósito):** borrar un equipo sigue siendo un
check-then-act (verifica que no tenga jugadores ni partidos y después borra).
Teóricamente, si justo en ese instante se crea un partido con ese equipo, el
partido queda apuntando a un equipo borrado. Se arreglaría con el mismo
patrón (script atómico o `WATCH`/`MULTI` de Redis). Lo dejamos documentado
como ejemplo de dónde aparece el problema.

## 5. El tiempo del partido también es un evento

El reloj del partido se modela igual que los goles: eventos de "arrancó a
tal velocidad", "se pausó" y "se adelantó 5 minutos". No hay un contador
guardado que se actualice cada segundo.

**Por qué así y no un contador:**

- **Todas las réplicas y pestañas ven el mismo minuto.** Con un contador en
  memoria, cada réplica tendría su propio reloj. Con eventos, cualquiera
  calcula el minuto con la misma fórmula: tiempo acumulado + (ahora − desde
  cuándo corre) × velocidad.
- **El modelo de lectura no se reescribe cada segundo.** Guarda desde cuándo
  y a qué velocidad corre, y cada cliente lo hace avanzar solo.
- **Se puede reconstruir.** El replay del partido devuelve también el reloj.

**El servidor es la autoridad del tiempo.** El cliente no manda "minuto 33":
el servidor sella cada evento con su propio reloj. La única excepción es
fijar un instante **ya transcurrido** (el minuto que marcaba el reloj cuando
se tocó *Gol*, mientras se elige el jugador). Aun así, el servidor valida
que no sea anterior al último evento ni posterior al reloj actual.

Eso es un **invariante**: una regla que el sistema garantiza siempre, venga
el pedido de donde venga. Acá el invariante es "el tiempo del partido solo
avanza". Lo garantiza el lado de escritura, no la interfaz: aunque alguien
llame a la API con `curl`, no puede anotar un gol en el minuto 5 si ya hubo
uno en el 30.

**Relación con DevOps:** si el tiempo lo calcula el servidor, todas las
réplicas tienen que tener la hora sincronizada (con **NTP**, el protocolo
estándar para sincronizar relojes de máquinas). Si `app2` tuviera la hora
corrida un minuto, los eventos que atiende quedarían un minuto corridos. En
Docker no pasa porque los contenedores usan el reloj del host.

## 5. CQRS más allá de FutGo

### Dos estructuras en paralelo: cuál es la permanente

CQRS mantiene **dos estructuras de datos a la vez**, pero no tienen el mismo
peso:

| | Escritura (eventos) | Lectura (tabla, marcadores) |
|---|---|---|
| Duración | Permanente: nunca se borra ni se edita | Descartable: es una vista derivada, como un caché |
| Si se pierde | Se pierde la historia; es lo único que hay que respaldar | Se reconstruye con el rebuild |
| Rol | Fuente de verdad | Respuesta rápida a cada pantalla |

Usar solo una no alcanza. Con **solo eventos**, cada consulta recalcula
todo y se vuelve lenta al crecer. Con **solo tablas armadas**, se pierde el
porqué de cada dato y no se pueden crear vistas nuevas con datos viejos.

El costo de tener dos es la duplicación de datos, la consistencia eventual y
el proyector como pieza extra. Analogía: los eventos son el **libro
contable** y la tabla es el **saldo**. El saldo se recalcula desde el libro;
al revés no se puede.

### CQRS sin Event Sourcing: buscador de un e-commerce

- **Escritura:** PostgreSQL normalizado (`productos`, `categorias`,
  `vendedores`, `stock`). Valida reglas, usa transacciones y no repite datos.
  Guarda el **estado actual**, no la historia.
- **Lectura:** Elasticsearch (motor de búsqueda) con **un documento
  desnormalizado por producto** (título, precio, categoría, vendedor, stock,
  talles). Una búsqueda con filtros y errores de tipeo responde en ms, algo
  que en PostgreSQL requeriría varios *joins* sobre millones de filas.
- **Sincronización:** con **CDC** (ej. Debezium) o con una tabla **outbox**.
  Cumple el mismo rol que nuestro proyector.

Lo que lo hace CQRS es lo mismo que en FutGo: un modelo para escribir, otro
para leer y un proceso que los sincroniza con consistencia eventual. La
diferencia es que no guarda historia: no se puede saber el precio de ayer
sin agregar una tabla aparte.

### Data warehouse: el mismo principio a escala empresarial

Un data warehouse aplica la misma idea:
**OLTP** (escritura) → **ETL/ELT** (sincronización) → **OLAP** (lectura).
Pero normalmente no se lo llama CQRS:

| | CQRS | Data warehouse |
|---|---|---|
| Alcance | Una aplicación o módulo | Muchos sistemas de la organización |
| Quién lee | Los usuarios de la misma app | Analistas, reportes, BI |
| Retraso | Milisegundos o segundos | Minutos u horas |

CQRS es una decisión de **arquitectura de una aplicación**. El data warehouse
es una decisión de **arquitectura de datos de la organización**: comparten
el principio, pero no el propósito.

### Red social: fan-out on write y on read

El timeline de una red social es un modelo de lectura precalculado.

**Fan-out on write** (cuentas comunes): al publicar, un proceso copia el post
al timeline de cada seguidor. Abrir la app es una sola lectura barata.

**Fan-out on read** (celebridades): copiar un post a 100 millones de
timelines es inviable. El post se guarda solo en la lista de posts de la
celebridad. Cuando un seguidor abre la app:

1. Lee su timeline precalculado (seguidos comunes).
2. Busca a qué celebridades sigue (suelen ser pocas).
3. Lee los últimos N posts de cada una. Esa lista es igual para todos sus
   seguidores, así que vive en caché y leerla es casi gratis.
4. Mezcla todo en una lista de candidatos.
5. Aplica el ranking del algoritmo y muestra el resultado.

| | Fan-out on write | Fan-out on read |
|---|---|---|
| Trabajo al publicar | Una escritura por seguidor | Una sola escritura |
| Trabajo al leer | Una lectura | Una más por celebridad, y la mezcla |
| Conviene para | Pocos seguidores | Muchísimos seguidores |

El sistema real es **híbrido** (Twitter lo usó así): write para la mayoría,
read para las pocas cuentas que harían explotar el write, con un umbral de
seguidores que decide el camino. El **ranking se aplica al leer**, porque
depende de señales que cambian todo el tiempo: el modelo de lectura guarda
**candidatos**, y la consulta los ordena.
