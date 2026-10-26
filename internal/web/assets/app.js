const state = {
  players: [],
  teams: [],
  matches: [],
  standings: [],
  // Lo que el usuario dejó escrito o elegido (jugador, minuto, nombres), para
  // no perderlo cuando el polling vuelve a dibujar la pantalla.
  drafts: new Map(),
  scores: new Map(),
  // Gol o tarjeta esperando que se elija el jugador: guarda el minuto en que
  // se tocó el botón, así el evento queda en ese instante.
  pending: null,
  // Modo del selector "Simular reenvío": off, key o version.
  resendMode: "off",
  // Diferencia entre la hora del servidor y la del navegador, en ms.
  clockOffset: 0,
  // Última versión vista de cada partido y animación en curso: así se anima
  // cada gol o tarjeta nuevos, vengan de esta pestaña o de otra.
  seenVersion: new Map(),
  animations: new Map(),
  // Partidos que esta pestaña ya mandó a cerrar al cumplirse el tiempo.
  finishing: new Set(),
  lastSnapshot: "",
};

const POLL_MS = 3000;

const elements = {
  errorBanner: document.querySelector("#error-banner"),
  errorText: document.querySelector("#error-text"),
  errorClose: document.querySelector("#error-close"),
  instancePill: document.querySelector(".instance-pill"),
  instanceValue: document.querySelector("#instance-value"),
  squadsList: document.querySelector("#squads-list"),
  liveList: document.querySelector("#live-list"),
  resultsList: document.querySelector("#results-list"),
  liveCount: document.querySelector("#live-count"),
  resultsCount: document.querySelector("#results-count"),
  teamsCount: document.querySelector("#teams-count"),
  matchForm: document.querySelector("#match-form"),
  teamA: document.querySelector("#team-a"),
  teamB: document.querySelector("#team-b"),
  matchSubmit: document.querySelector("#match-submit"),
  standingsBody: document.querySelector("#standings-body"),
  rebuildButton: document.querySelector("#rebuild-button"),
  resendModes: document.querySelector("#resend-modes"),
  resendHint: document.querySelector("#resend-hint"),
  requestLog: document.querySelector("#request-log"),
};

class HttpError extends Error {
  constructor(status, payload) {
    super(payload?.error || "No se pudo completar la solicitud.");
    this.status = status;
    this.payload = payload;
  }
}

/* --- Comunicación con la API ------------------------------------------- */

function newKey() {
  if (crypto.randomUUID) {
    return crypto.randomUUID();
  }
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

// send hace UNA request HTTP. Además actualiza el indicador de instancia con
// la cabecera X-Instance de cada respuesta, así se ve el round robin en vivo.
async function send(path, options = {}) {
  const response = await fetch(path, options);
  const instance = response.headers.get("X-Instance");
  const serverTime = Number(response.headers.get("X-Server-Time"));
  if (serverTime) {
    state.clockOffset = serverTime - Date.now();
  }
  if (instance && instance !== elements.instanceValue.textContent) {
    elements.instanceValue.textContent = instance;
    elements.instancePill.classList.remove("blink");
    void elements.instancePill.offsetWidth;
    elements.instancePill.classList.add("blink");
  }
  let payload = null;
  if (response.status !== 204) {
    try {
      payload = await response.json();
    } catch {
      payload = null;
    }
  }
  return { response, payload, instance };
}

// request es para las consultas (GET).
async function request(path) {
  const { response, payload } = await send(path);
  if (!response.ok) {
    throw new HttpError(response.status, payload);
  }
  return payload;
}

// command manda un comando (POST/DELETE) con una Idempotency-Key nueva por
// cada acción del usuario. Si la red falla o el proxy devuelve 502/503/504,
// reintenta con LA MISMA key: el servidor reconoce el reintento y no lo
// aplica dos veces.
//
// El selector "Simular reenvío" manda una segunda copia para la demo:
//   - "key": misma key. Es el mismo intento repetido (doble click): el
//     servidor devuelve la respuesta guardada y no lo aplica de nuevo.
//   - "version": key nueva pero la misma expected_version. Es otra acción
//     decidida sobre un estado viejo: el servidor la rechaza con 409.
// La interfaz sigue con el resultado de la primera copia; la segunda solo
// queda en el registro.
async function command(method, path, body) {
  const key = newKey();
  const options = {
    method,
    headers: { "Content-Type": "application/json", "Idempotency-Key": key },
    body: body === undefined ? undefined : JSON.stringify(body),
  };
  const result = await sendWithRetry(method, path, options, key);

  if (state.resendMode === "key") {
    await sendWithRetry(method, path, options, key);
  } else if (state.resendMode === "version" && body?.expected_version !== undefined) {
    const otherKey = newKey();
    const copy = await sendWithRetry(method, path, {
      ...options,
      headers: { ...options.headers, "Idempotency-Key": otherKey },
    }, otherKey);
    if (copy.response.status === 409) {
      logNote(`La copia traía la versión ${body.expected_version}, pero el partido ya iba por la ${copy.payload?.current_version}: 409, no se aplicó`);
    }
  }

  const { response, payload } = result;
  if (!response.ok) {
    throw new HttpError(response.status, payload);
  }
  return payload;
}

async function sendWithRetry(method, path, options, key) {
  for (let attempt = 1; ; attempt += 1) {
    try {
      const result = await send(path, options);
      logRequest(method, path, result, key);
      if (![502, 503, 504].includes(result.response.status) || attempt === 3) {
        return result;
      }
    } catch (error) {
      logNote(`${method} ${path}: error de red, reintento con la misma key`);
      if (attempt === 3) {
        throw error;
      }
    }
    await sleep(300 * attempt);
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// waitForVersion espera a que el modelo de lectura refleje el comando recién
// enviado. El comando escribe un evento; el proyector actualiza la vista unos
// milisegundos después (consistencia eventual).
async function waitForVersion(matchId, version) {
  const started = performance.now();
  for (let i = 0; i < 30; i += 1) {
    const matches = await request("/matches");
    const match = matches.find((candidate) => candidate.id === matchId);
    if (!match || match.version >= version) {
      logNote(`Lectura actualizada a v${version} en ${Math.round(performance.now() - started)} ms`);
      return;
    }
    await sleep(100);
  }
  logNote(`La lectura todavía no llegó a v${version}; se actualizará con el polling`);
}

// sendEvent manda un evento con la versión del partido que el usuario está
// viendo. Si otra pestaña (o réplica) ya agregó otro evento, el servidor
// responde 409 y no se aplica nada.
async function sendEvent(match, payload) {
  try {
    const result = await command("POST", `/matches/${match.id}/events`, {
      ...payload,
      expected_version: match.version,
    });
    await waitForVersion(match.id, result.version);
    await refresh();
  } catch (error) {
    if (error.status === 409 && error.payload?.current_version !== undefined) {
      await refresh();
      throw new Error(
        `Otro usuario modificó este partido mientras tanto (tenías la versión ${match.version}, ya va por la ${error.payload.current_version}). Actualicé el marcador: revisá y volvé a intentar.`,
      );
    }
    throw error;
  }
}

/* --- Registro de requests ------------------------------------------------ */

function logRequest(method, path, { response, instance }, key) {
  const replayed = response.headers.get("Idempotent-Replayed") === "true";
  const item = el("li", replayed ? "log-replay" : response.ok ? "log-ok" : "log-error");
  const detail = el("span", "log-detail");
  detail.append(
    "Atendió ",
    el("b", "", instance || "?"),
    ` · key ${key.slice(0, 8)}`,
    replayed ? " · repetida, no se aplicó" : "",
  );
  item.append(el("span", "method", method), el("span", "path", path), el("span", "status", String(response.status)), detail);
  pushLog(item);
}

function logNote(text) {
  pushLog(el("li", "log-note", text));
}

function pushLog(item) {
  elements.requestLog.querySelector(".log-empty")?.remove();
  elements.requestLog.prepend(item);
  while (elements.requestLog.children.length > 16) {
    elements.requestLog.lastElementChild.remove();
  }
}

/* --- Helpers de UI -------------------------------------------------------- */

let errorTimer;

function showError(error) {
  elements.errorText.textContent = error.message;
  elements.errorBanner.hidden = false;
  clearTimeout(errorTimer);
  errorTimer = setTimeout(clearError, 9000);
}

function clearError() {
  elements.errorBanner.hidden = true;
}

function el(tag, className, text) {
  const element = document.createElement(tag);
  if (className) {
    element.className = className;
  }
  if (text !== undefined) {
    element.textContent = text;
  }
  return element;
}

function emptyState(message) {
  return el("p", "empty-state", message);
}

// button crea un botón que se deshabilita mientras su acción está en curso
// y muestra el error si falla.
function button(label, className, onClick, ariaLabel) {
  const element = el("button", className);
  element.type = "button";
  if (typeof label === "string") {
    element.textContent = label;
  } else {
    element.append(...label);
  }
  if (ariaLabel) {
    element.setAttribute("aria-label", ariaLabel);
    element.title = ariaLabel;
  }
  element.addEventListener("click", async () => {
    element.disabled = true;
    try {
      await onClick();
      clearError();
    } catch (error) {
      showError(error);
    } finally {
      element.disabled = false;
    }
  });
  return element;
}

// draftInput crea un input cuyo valor sobrevive a los redibujos.
function draftInput(key, attributes) {
  const input = el("input");
  Object.assign(input, attributes);
  input.dataset.draft = key;
  input.value = state.drafts.get(key) || "";
  input.addEventListener("input", () => state.drafts.set(key, input.value));
  return input;
}

// inlineForm arma un formulario de una línea (input + botón) que limpia su
// borrador al enviarse bien.
function inlineForm(input, label, onSubmit) {
  const form = el("form", "inline-form");
  const submit = el("button", "btn btn-ghost btn-sm", label);
  submit.type = "submit";
  form.append(input, submit);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!input.value.trim()) {
      input.focus();
      return;
    }
    submit.disabled = true;
    try {
      await onSubmit(input.value.trim());
      state.drafts.delete(input.dataset.draft);
      input.value = "";
      clearError();
      await refresh();
    } catch (error) {
      showError(error);
    } finally {
      submit.disabled = false;
    }
  });
  return form;
}

function teamName(id) {
  const team = state.teams.find((candidate) => candidate.id === id);
  return team ? team.name : `Equipo #${id}`;
}

function playerName(id) {
  const player = state.players.find((candidate) => candidate.id === id);
  return player ? player.name : `Jugador #${id}`;
}

function cardChip(color) {
  return el("span", `card-chip ${color}`);
}

/* --- Planteles ------------------------------------------------------------ */

function renderSquads() {
  elements.squadsList.replaceChildren();
  elements.teamsCount.textContent = String(state.teams.length);

  const playersByTeam = new Map();
  state.players.forEach((player) => {
    const roster = playersByTeam.get(player.team_id) || [];
    roster.push(player);
    playersByTeam.set(player.team_id, roster);
  });

  state.teams.forEach((team) => {
    const card = el("article", "squad-card");

    const heading = el("div", "squad-heading");
    const name = el("h3", "", team.name);
    name.append(el("small", "", `#${team.id}`));
    heading.append(
      name,
      button("Eliminar", "text-button", async () => {
        await command("DELETE", `/teams/${team.id}`);
        await refresh();
      }, `Eliminar equipo ${team.name}`),
    );
    card.append(heading);

    const roster = playersByTeam.get(team.id) || [];
    if (!roster.length) {
      card.append(el("p", "squad-empty", "Sin jugadores todavía."));
    } else {
      const list = el("ul", "roster");
      roster.forEach((player) => {
        const item = el("li");
        item.append(
          el("span", "", player.name),
          button("✕", "icon-button", async () => {
            await command("DELETE", `/players/${player.id}`);
            await refresh();
          }, `Eliminar jugador ${player.name}`),
        );
        list.append(item);
      });
      card.append(list);
    }

    const input = draftInput(`player-${team.id}`, {
      type: "text",
      placeholder: "Nuevo jugador",
      autocomplete: "off",
      ariaLabel: `Nombre del nuevo jugador de ${team.name}`,
    });
    card.append(inlineForm(input, "Sumar", (playerNameValue) => command("POST", "/players", { name: playerNameValue, team_id: team.id })));
    elements.squadsList.append(card);
  });

  const newTeam = el("article", "squad-card new-team");
  const body = el("div", "new-team-body");
  const input = draftInput("new-team", {
    type: "text",
    placeholder: "Ej. Las Estrellas",
    autocomplete: "off",
    ariaLabel: "Nombre del nuevo equipo",
  });
  body.append(el("p", "", "Nuevo equipo"), inlineForm(input, "Crear", (teamNameValue) => command("POST", "/teams", { name: teamNameValue })));
  newTeam.append(body);
  elements.squadsList.append(newTeam);
}

function fillTeamSelect(select, placeholderText) {
  const previous = select.value;
  select.replaceChildren();
  const placeholder = el("option", "", state.teams.length ? placeholderText : "Sin equipos");
  placeholder.value = "";
  placeholder.disabled = true;
  placeholder.selected = true;
  select.append(placeholder);
  // Un equipo juega un solo partido a la vez: los que están en vivo no se
  // pueden elegir (el servidor igual lo rechaza con 409).
  const busy = new Set(state.matches.filter((match) => match.status === "live").flatMap((match) => [match.team_a_id, match.team_b_id]));
  state.teams.forEach((team) => {
    const option = el("option", "", busy.has(team.id) ? `${team.name} (jugando)` : team.name);
    option.value = String(team.id);
    option.disabled = busy.has(team.id);
    select.append(option);
  });
  if (state.teams.some((team) => String(team.id) === previous && !busy.has(team.id))) {
    select.value = previous;
  }
}

function renderTeamOptions() {
  fillTeamSelect(elements.teamA, "Local");
  fillTeamSelect(elements.teamB, "Visitante");
  elements.matchSubmit.disabled = state.teams.length < 2;
}

/* --- Reloj del partido ---------------------------------------------------- */

const REGULATION_SEC = 90 * 60;
const EXTRA_TIME_SEC = 120 * 60;
const SPEEDS = [10, 30, 60, 120];

// serverNow es la hora del servidor estimada con la cabecera X-Server-Time:
// el reloj del partido no depende de la hora de la computadora del usuario.
function serverNow() {
  return Date.now() + state.clockOffset;
}

// clockAt replica en el navegador el cálculo de model.Match.ClockAt: el
// modelo de lectura guarda desde cuándo y a qué velocidad corre el reloj, y
// la interfaz lo hace avanzar sola sin pedirle nada al servidor.
function clockAt(match, nowMs = serverNow()) {
  let sec = match.clock_sec || 0;
  if (match.clock_running) {
    const since = Date.parse(match.clock_since);
    if (nowMs > since) {
      sec += Math.floor(((nowMs - since) / 1000) * match.clock_speed);
    }
  }
  return Math.min(sec, limitSec(match));
}

// limitSec replica model.Match.LimitSec: 120' si va empatado o ya se pasó
// del 90', 90' si no.
function limitSec(match) {
  return match.score_a === match.score_b || match.clock_sec > REGULATION_SEC ? EXTRA_TIME_SEC : REGULATION_SEC;
}

// sentOff: jugadores expulsados en el partido (no pueden tener más eventos).
function sentOff(match) {
  return new Set(match.events.filter((event) => event.type === "red_card" && event.player_id).map((event) => event.player_id));
}

function formatClock(sec) {
  return `${String(Math.floor(sec / 60)).padStart(2, "0")}:${String(sec % 60).padStart(2, "0")}`;
}

function minuteLabel(sec) {
  return `${Math.floor(sec / 60)}'`;
}

// tickClocks actualiza los relojes visibles cuatro veces por segundo sin
// redibujar el resto de la pantalla, y cierra los partidos que cumplieron el
// tiempo.
function tickClocks() {
  document.querySelectorAll("[data-clock-match]").forEach((node) => {
    const match = state.matches.find((candidate) => candidate.id === Number(node.dataset.clockMatch));
    if (match) {
      node.textContent = formatClock(clockAt(match));
    }
  });
  state.matches
    .filter((match) => match.status === "live" && clockAt(match) >= limitSec(match) && !state.finishing.has(match.id))
    .forEach((match) => {
      state.finishing.add(match.id);
      // Si otra pestaña lo cerró primero, el servidor rechaza este: no es error.
      sendEvent(match, { type: "match_finished" }).catch(() => refresh().catch(() => {}));
    });
}

/* --- Partidos ------------------------------------------------------------- */

function timelineItem(event, match) {
  const item = el("li");
  const mark = el("span", "mark");
  const what = el("span", "what");
  const milestone = (symbol, text) => {
    item.classList.add("milestone");
    mark.textContent = symbol;
    what.textContent = text;
  };

  switch (event.type) {
    case "match_started":
      milestone("▶", "Inicio del partido");
      break;
    case "match_finished":
      milestone("■", `Final · ${event.score_a || 0} - ${event.score_b || 0}`);
      break;
    case "match_cancelled":
      milestone("✕", "Partido cancelado");
      break;
    case "clock_started":
      milestone("⏵", `Reloj en marcha · x${event.speed}`);
      break;
    case "clock_paused":
      milestone("⏸", "Reloj en pausa");
      break;
    case "clock_advanced":
      milestone("⏩", `Se adelantó el reloj +${Math.round(event.advance_sec / 60)}'`);
      break;
    case "goal_annulled":
      mark.textContent = "🚫";
      what.append(el("strong", "", "Gol anulado"), el("span", "", ` · evento v${event.ref_seq}`));
      break;
    default: {
      if (event.type === "goal") {
        mark.textContent = "⚽";
      } else {
        mark.append(cardChip(event.type === "red_card" ? "red" : "yellow"));
      }
      const label = { goal: "Gol", yellow_card: "Amarilla", red_card: "Roja" }[event.type] || event.type;
      const who = event.player_id ? playerName(event.player_id) : label;
      what.append(el("strong", "", who), el("span", "", ` · ${event.player_id ? `${label}, ` : ""}${teamName(event.team_id)}`));
    }
  }
  if (event.annulled) {
    item.classList.add("annulled");
  }

  const action = match.status === "live" && event.type === "goal" && !event.annulled
    ? button("Anular", "text-button", () => sendEvent(match, { type: "goal_annulled", ref_seq: event.seq }), `Anular gol v${event.seq}`)
    : el("span");
  const sec = event.clock_sec ?? (event.minute || 0) * 60;
  item.append(el("span", "minute", minuteLabel(sec)), mark, what, el("span", "seq", `v${event.seq}`), action);
  return item;
}

function matchSheet(match, open) {
  const details = el("details", "match-sheet");
  details.open = open;
  details.append(el("summary", "", `Planilla del partido · ${match.events.length} eventos`));
  const list = el("ol", "timeline");
  // En vivo, lo último arriba: es lo que se mira.
  const events = match.status === "live" ? [...match.events].reverse() : match.events;
  events.forEach((event) => list.append(timelineItem(event, match)));
  details.append(list);
  return details;
}

function led(match) {
  const board = el("div", "led");
  board.setAttribute("aria-label", `${match.score_a} a ${match.score_b}`);
  board.append(el("span", "led-digit", String(match.score_a)), el("span", "led-sep", ":"), el("span", "led-digit", String(match.score_b)));
  const key = `${match.score_a}-${match.score_b}`;
  const previous = state.scores.get(match.id);
  if (previous !== undefined && previous !== key) {
    board.classList.add("bump");
  }
  state.scores.set(match.id, key);
  return board;
}

/* --- Cancha y animaciones ------------------------------------------------- */

const SVG_NS = "http://www.w3.org/2000/svg";

// Duración de cada animación, en ms. Solo se animan estos eventos.
const ANIMATION_MS = { goal: 2600, yellow_card: 2000, red_card: 2000, goal_annulled: 2000 };

function svg(tag, attributes) {
  const node = document.createElementNS(SVG_NS, tag);
  Object.entries(attributes).forEach(([name, value]) => node.setAttribute(name, value));
  return node;
}

// pitchLayer dibuja la cancha (vista de costado: el local ataca hacia la
// derecha) detrás del marcador. Cuando el reloj corre, una pelota recorre
// la cancha: es la "simulación" en marcha.
function pitchLayer(match) {
  const layer = el("div", "pitch-layer");
  layer.setAttribute("aria-hidden", "true");
  const lines = svg("svg", { class: "pitch-lines", viewBox: "0 0 100 64", preserveAspectRatio: "none" });
  [
    svg("rect", { x: 2, y: 2, width: 96, height: 60 }),
    svg("line", { x1: 50, y1: 2, x2: 50, y2: 62 }),
    svg("ellipse", { cx: 50, cy: 32, rx: 7, ry: 11 }),
    svg("rect", { x: 2, y: 16, width: 13, height: 32 }),
    svg("rect", { x: 85, y: 16, width: 13, height: 32 }),
    svg("rect", { x: 2, y: 25, width: 5, height: 14 }),
    svg("rect", { x: 93, y: 25, width: 5, height: 14 }),
  ].forEach((shape) => lines.append(shape));
  layer.append(lines, el("span", "pitch-shade"));
  if (clockIsRunning(match)) {
    // Con la simulación en marcha se llenan las tribunas de los costados.
    layer.classList.add("with-stands");
    layer.append(el("span", "pitch-ball"), el("span", "stand stand-top"), el("span", "stand stand-bottom"));
  }
  return layer;
}

// detectNewEvents compara cada partido en vivo con la última versión vista
// y deja programada la animación del evento nuevo más reciente. La primera
// vez que se ve un partido no se anima nada (no es un evento "nuevo").
function detectNewEvents(live) {
  live.forEach((match) => {
    const seen = state.seenVersion.get(match.id);
    state.seenVersion.set(match.id, match.version);
    if (seen === undefined) {
      return;
    }
    const fresh = match.events.filter((event) => event.seq > seen && ANIMATION_MS[event.type]);
    if (fresh.length) {
      state.animations.set(match.id, { event: fresh[fresh.length - 1], start: performance.now() });
    }
  });
}

// burst arma la animación de un evento. Si la pantalla se redibuja a mitad,
// se vuelve a montar con un delay negativo para que siga desde donde iba.
function burst(match, event, elapsed) {
  const home = event.team_id === match.team_a_id;
  const who = event.player_id ? playerName(event.player_id) : teamName(event.team_id);
  const overlay = el("div", `burst burst-${event.type.replace("_", "-")} ${home ? "side-home" : "side-away"}`);
  overlay.setAttribute("aria-hidden", "true");
  overlay.style.setProperty("--delay", `${-elapsed}ms`);

  if (event.type === "goal") {
    overlay.append(el("span", "burst-flash"), el("span", "burst-ball", "⚽"));
    const text = el("div", "burst-text");
    text.append(el("strong", "", "¡GOOOL!"), el("span", "", `${who} · ${minuteLabel(event.clock_sec || 0)}`));
    overlay.append(text);
  } else if (event.type === "goal_annulled") {
    const text = el("div", "burst-text");
    text.append(el("strong", "", "VAR"), el("span", "", "Gol anulado"));
    overlay.append(text);
  } else {
    const card = el("div", "burst-card");
    card.append(el("span", `burst-card-face ${event.type === "red_card" ? "red" : "yellow"}`));
    const text = el("div", "burst-card-text");
    text.append(el("strong", "", event.type === "red_card" ? "Roja" : "Amarilla"), el("span", "", who));
    card.append(text);
    overlay.append(card);
  }
  setTimeout(() => overlay.remove(), ANIMATION_MS[event.type] - elapsed);
  return overlay;
}

// clockIsRunning: el reloj corre y todavía no llegó al tope (90' o 120').
function clockIsRunning(match) {
  return match.clock_running && clockAt(match) < limitSec(match);
}

function matchClock(match) {
  const box = el("div", `match-clock${clockIsRunning(match) ? " running" : ""}`);
  const value = el("span", "clock-value", formatClock(clockAt(match)));
  value.dataset.clockMatch = String(match.id);
  value.setAttribute("aria-label", "Tiempo de partido");
  let status = match.clock_running ? `En marcha · x${match.clock_speed}` : "Reloj en pausa";
  if (clockAt(match) >= limitSec(match)) {
    status = "Tiempo cumplido";
  }
  box.append(value, el("span", "clock-status", status));
  return box;
}

// clockControls: el reloj solo avanza. Se puede adelantar a mano (+1', +5',
// +10') o dejarlo correr solo a una velocidad mayor que la real.
function clockControls(match) {
  const draft = state.drafts.get(`clock-${match.id}`) || { speed: 60 };
  state.drafts.set(`clock-${match.id}`, draft);
  const atLimit = clockAt(match) >= limitSec(match);

  const row = el("div", "clock-controls");

  const auto = el("div", "control-group");
  auto.append(el("span", "control-label", "Automático"));
  if (match.clock_running) {
    auto.append(button("⏸ Pausar", "btn btn-ghost btn-sm", () => sendEvent(match, { type: "clock_paused" })));
  } else {
    const play = button("⏵ Correr", "btn btn-play btn-sm", () => sendEvent(match, { type: "clock_started", speed: draft.speed }));
    play.disabled = atLimit;
    auto.append(play);
  }
  const speeds = el("div", "segmented");
  speeds.setAttribute("role", "radiogroup");
  speeds.setAttribute("aria-label", "Velocidad del reloj");
  SPEEDS.forEach((speed) => {
    const option = el("button", speed === (match.clock_running ? match.clock_speed : draft.speed) ? "active" : "", `x${speed}`);
    option.type = "button";
    option.setAttribute("role", "radio");
    option.setAttribute("aria-checked", String(option.classList.contains("active")));
    option.title = `${speed} segundos de partido por cada segundo real`;
    option.disabled = match.clock_running;
    option.addEventListener("click", () => {
      draft.speed = speed;
      renderMatches();
    });
    speeds.append(option);
  });
  auto.append(speeds);

  const manual = el("div", "control-group");
  manual.append(el("span", "control-label", "Adelantar"));
  [1, 5, 10].forEach((minutes) => {
    const step = button(`+${minutes}'`, "btn btn-ghost btn-sm", () => sendEvent(match, { type: "clock_advanced", advance_sec: minutes * 60 }), `Adelantar ${minutes} minutos`);
    step.disabled = atLimit;
    manual.append(step);
  });

  row.append(auto, manual);
  return row;
}

const EVENT_NAMES = { goal: "Gol", yellow_card: "Amarilla", red_card: "Roja" };

// teamActions: un bloque por equipo con gol y tarjetas. Al tocar uno se
// congela el minuto del reloj y se abre el selector de jugador.
function teamActions(match) {
  const grid = el("div", "team-actions");
  [match.team_a_id, match.team_b_id].forEach((teamId) => {
    const side = el("div", "team-side");
    side.append(el("span", "control-label", teamName(teamId)));
    const buttons = el("div", "team-buttons");
    const open = (type) => () => {
      state.pending = { matchId: match.id, type, teamId, clockSec: clockAt(match), version: match.version };
      renderMatches();
    };
    buttons.append(
      button("⚽ Gol", "btn btn-goal", open("goal")),
      button([cardChip("yellow"), "Amarilla"], "btn btn-card", open("yellow_card")),
      button([cardChip("red"), "Roja"], "btn btn-card", open("red_card")),
    );
    side.append(buttons);
    grid.append(side);
  });
  return grid;
}

// whoPicker pregunta quién hizo el gol o recibió la tarjeta. Se puede elegir
// un jugador del plantel, no especificarlo, o sumar uno nuevo en el momento.
function whoPicker(match, pending) {
  const box = el("div", "who-picker");
  box.setAttribute("role", "dialog");
  box.setAttribute("aria-label", "Quién");

  const record = async (playerId) => {
    try {
      await sendEvent({ ...match, version: pending.version }, {
        type: pending.type,
        team_id: pending.teamId,
        player_id: playerId,
        clock_sec: pending.clockSec,
      });
    } finally {
      state.pending = null;
      renderMatches();
    }
  };

  const head = el("div", "picker-head");
  const title = el("p", "picker-title");
  title.append(
    pending.type === "goal" ? "⚽ " : cardChip(pending.type === "red_card" ? "red" : "yellow"),
    `${EVENT_NAMES[pending.type]} de `,
    el("strong", "", teamName(pending.teamId)),
    el("span", "picker-minute", ` · ${minuteLabel(pending.clockSec)}`),
  );
  head.append(title, button("Cancelar", "text-button", async () => {
    state.pending = null;
    renderMatches();
  }));
  box.append(head, el("p", "picker-question", pending.type === "goal" ? "¿Quién hizo el gol?" : "¿Quién recibió la tarjeta?"));

  const chips = el("div", "player-chips");
  const expelled = sentOff(match);
  state.players
    .filter((player) => player.team_id === pending.teamId && !expelled.has(player.id))
    .forEach((player) => chips.append(button(player.name, "chip", () => record(player.id))));
  chips.append(button("Sin especificar", "chip chip-muted", () => record(0)));
  box.append(chips);

  const input = draftInput(`picker-${pending.teamId}`, {
    type: "text",
    placeholder: "Otro jugador (se suma al plantel)",
    autocomplete: "off",
    ariaLabel: `Nuevo jugador de ${teamName(pending.teamId)}`,
  });
  const form = inlineForm(input, "Sumar y anotar", async (name) => {
    const player = await command("POST", "/players", { name, team_id: pending.teamId });
    state.players = [...state.players, player];
    await record(player.id);
  });
  box.append(form);
  return box;
}

function cancelMatch(match) {
  return async () => {
    await command("DELETE", `/matches/${match.id}`);
    await waitForVersion(match.id, match.version + 1);
    await refresh();
  };
}

function scoreboard(match) {
  const card = el("article", "scoreboard");

  const bar = el("div", "scoreboard-bar");
  const meta = el("div", "scoreboard-meta");
  meta.append(el("span", "live-tag", "EN VIVO"), el("span", "", `Partido #${match.id}`), el("span", "version-chip", `v${match.version}`));
  const actions = el("div", "scoreboard-actions");
  actions.append(
    button("Cancelar", "btn btn-danger btn-sm", cancelMatch(match), `Cancelar partido #${match.id}`),
    button("Finalizar", "btn btn-ghost btn-sm", () => sendEvent(match, { type: "match_finished" }), `Finalizar partido #${match.id}`),
  );
  bar.append(meta, actions);

  const main = el("div", `scoreboard-main${clockIsRunning(match) ? " running" : ""}`);
  main.append(pitchLayer(match));
  const home = el("div", "scoreboard-team", teamName(match.team_a_id));
  home.append(el("small", "", "Local"));
  const away = el("div", "scoreboard-team away", teamName(match.team_b_id));
  away.append(el("small", "", "Visitante"));
  const center = el("div", "scoreboard-center");
  center.append(led(match), matchClock(match));
  main.append(home, center, away);

  const animation = state.animations.get(match.id);
  if (animation) {
    const elapsed = performance.now() - animation.start;
    if (elapsed < ANIMATION_MS[animation.event.type]) {
      main.append(burst(match, animation.event, elapsed));
    } else {
      state.animations.delete(match.id);
    }
  }

  const pending = state.pending?.matchId === match.id ? state.pending : null;
  const deck = el("div", "control-deck");
  deck.append(clockControls(match), pending ? whoPicker(match, pending) : teamActions(match));

  card.append(bar, main, deck, matchSheet(match, true));
  return card;
}

function result(match) {
  const card = el("article", "result");
  const row = el("div", "result-row");
  const outcome = Math.sign(match.score_a - match.score_b);
  const home = el("span", `result-team ${outcome > 0 ? "winner" : outcome < 0 ? "loser" : ""}`, teamName(match.team_a_id));
  const away = el("span", `result-team away ${outcome < 0 ? "winner" : outcome > 0 ? "loser" : ""}`, teamName(match.team_b_id));
  const meta = el("div", "result-meta");
  meta.append(
    el("span", "version-chip", `v${match.version}`),
    button("Cancelar", "text-button", cancelMatch(match), `Cancelar partido #${match.id} (descuenta de la tabla)`),
  );
  row.append(home, el("span", "result-score", `${match.score_a} – ${match.score_b}`), away, meta);
  card.append(row, matchSheet(match, false));
  return card;
}

function renderMatches() {
  // Recordar qué planillas de resultados abrió el usuario.
  const opened = new Set([...elements.resultsList.querySelectorAll("details[open]")].map((d) => d.dataset.match));

  const live = state.matches.filter((match) => match.status === "live").sort((a, b) => b.id - a.id);
  const finished = state.matches.filter((match) => match.status === "finished").sort((a, b) => b.id - a.id);
  elements.liveCount.textContent = String(live.length);
  elements.resultsCount.textContent = String(finished.length);

  detectNewEvents(live);
  elements.liveList.replaceChildren();
  if (!live.length) {
    elements.liveList.append(emptyState(state.teams.length < 2
      ? "Creá al menos dos equipos en Planteles para iniciar un partido."
      : "No hay partidos en juego. Elegí local y visitante e iniciá el partido."));
  }
  live.forEach((match) => elements.liveList.append(scoreboard(match)));

  elements.resultsList.replaceChildren();
  if (!finished.length) {
    elements.resultsList.append(emptyState("Todavía no terminó ningún partido."));
  }
  finished.forEach((match) => {
    const card = result(match);
    const details = card.querySelector("details");
    details.dataset.match = String(match.id);
    details.open = opened.has(String(match.id));
    elements.resultsList.append(card);
  });
}

/* --- Tabla ---------------------------------------------------------------- */

function renderStandings() {
  elements.standingsBody.replaceChildren();
  if (!state.standings.length) {
    const row = el("tr");
    const cell = el("td", "empty-state", "Sin equipos todavía.");
    cell.colSpan = 10;
    row.append(cell);
    elements.standingsBody.append(row);
    return;
  }
  state.standings.forEach((standing, index) => {
    const row = el("tr", index === 0 && standing.points > 0 ? "leader" : "");
    const diff = standing.goal_diff;
    const cells = [
      [index + 1, "pos-col"],
      [standing.team_name, "team-col"],
      [standing.played],
      [standing.won],
      [standing.drawn],
      [standing.lost],
      [standing.goals_for, "hide-narrow"],
      [standing.goals_against, "hide-narrow"],
      [diff > 0 ? `+${diff}` : diff, diff > 0 ? "diff-pos" : diff < 0 ? "diff-neg" : ""],
      [standing.points, "points-col"],
    ];
    cells.forEach(([value, className]) => row.append(el("td", className, String(value))));
    elements.standingsBody.append(row);
  });
}

/* --- Ciclo de refresco ------------------------------------------------------ */

function renderAll() {
  // Si el usuario está escribiendo en un input con borrador, se le devuelve
  // el foco después de redibujar.
  const active = document.activeElement?.dataset?.draft;
  renderSquads();
  renderTeamOptions();
  renderMatches();
  renderStandings();
  if (active) {
    const input = document.querySelector(`[data-draft="${active}"]`);
    input?.focus();
    input?.setSelectionRange(input.value.length, input.value.length);
  }
}

// refresh vuelve a leer todo. Solo redibuja si algo cambió, para no pisar lo
// que el usuario está tocando en cada vuelta del polling.
async function refresh() {
  const [players, teams, matches, standings] = await Promise.all([
    request("/players"),
    request("/teams"),
    request("/matches"),
    request("/standings"),
  ]);
  const snapshot = JSON.stringify([players, teams, matches, standings]);
  if (snapshot === state.lastSnapshot) {
    return;
  }
  Object.assign(state, { players, teams, matches, standings, lastSnapshot: snapshot });
  renderAll();
}

async function poll() {
  try {
    await refresh();
  } catch (error) {
    elements.instanceValue.textContent = "—";
    showError(error);
  } finally {
    setTimeout(poll, POLL_MS);
  }
}

elements.matchForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const data = new FormData(elements.matchForm);
  elements.matchSubmit.disabled = true;
  try {
    const created = await command("POST", "/matches", {
      team_a_id: Number(data.get("team_a_id")),
      team_b_id: Number(data.get("team_b_id")),
    });
    await waitForVersion(created.match_id, created.version);
    await refresh();
    clearError();
  } catch (error) {
    showError(error);
  } finally {
    renderTeamOptions();
  }
});

elements.rebuildButton.addEventListener("click", async () => {
  elements.rebuildButton.disabled = true;
  try {
    const rebuilt = await command("POST", "/projections/rebuild");
    logNote(`Tabla y marcadores recalculados desde los eventos de ${rebuilt.matches} partidos`);
    state.lastSnapshot = "";
    await refresh();
    clearError();
  } catch (error) {
    showError(error);
  } finally {
    elements.rebuildButton.disabled = false;
  }
});

elements.errorClose.addEventListener("click", clearError);

const RESEND_HINTS = {
  off: "Cada comando se manda una vez.",
  key: "La copia es el mismo intento (como un doble click): lleva la misma Idempotency-Key y el servidor no la vuelve a aplicar.",
  version: "La copia es otra acción decidida sobre el mismo estado: lleva otra key pero la misma versión del partido, y el servidor la rechaza con 409. Solo se aplica a los eventos de un partido.",
};

elements.resendModes.querySelectorAll("button").forEach((option) => {
  option.addEventListener("click", () => {
    state.resendMode = option.dataset.mode;
    elements.resendModes.querySelectorAll("button").forEach((other) => {
      other.classList.toggle("active", other === option);
      other.setAttribute("aria-checked", String(other === option));
    });
    elements.resendHint.textContent = RESEND_HINTS[state.resendMode];
  });
});

elements.requestLog.append(el("li", "log-empty", "Todavía no se mandó ningún comando. Probá registrar un gol."));

/* --- Vistas ------------------------------------------------------------ */

// showView muestra la vista del #hash (En vivo si no hay o no existe). Las
// vistas son solo HTML ya cargado: cambiar de vista no pide nada al servidor.
function showView() {
  const views = [...document.querySelectorAll("[data-view]")];
  const name = location.hash.slice(1);
  const current = views.find((view) => view.dataset.view === name) || views[0];
  views.forEach((view) => {
    view.hidden = view !== current;
  });
  document.querySelectorAll(".topnav a").forEach((link) => {
    if (link.hash === `#${current.dataset.view}`) {
      link.setAttribute("aria-current", "page");
    } else {
      link.removeAttribute("aria-current");
    }
  });
  window.scrollTo(0, 0);
}

window.addEventListener("hashchange", showView);
showView();

poll();
setInterval(tickClocks, 250);
