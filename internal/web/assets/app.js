const state = {
  players: [],
  teams: [],
  matches: [],
};

const elements = {
  errorBanner: document.querySelector("#error-banner"),
  instanceValue: document.querySelector("#instance-value"),
  playersList: document.querySelector("#players-list"),
  teamsList: document.querySelector("#teams-list"),
  matchesList: document.querySelector("#matches-list"),
  teamA: document.querySelector("#team-a"),
  teamB: document.querySelector("#team-b"),
  matchSubmit: document.querySelector("#match-submit"),
};

async function request(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  let payload;
  try {
    payload = await response.json();
  } catch {
    payload = null;
  }
  if (!response.ok) {
    throw new Error(payload?.error || "No se pudo completar la solicitud.");
  }
  return payload;
}

function showError(error) {
  elements.errorBanner.textContent = error.message;
  elements.errorBanner.hidden = false;
}

function clearError() {
  elements.errorBanner.textContent = "";
  elements.errorBanner.hidden = true;
}

function addEmptyState(container, message) {
  const empty = document.createElement(container.tagName === "UL" ? "li" : "p");
  empty.className = "empty-state";
  empty.textContent = message;
  container.append(empty);
}

function renderPlayers() {
  elements.playersList.replaceChildren();
  if (!state.players.length) {
    addEmptyState(elements.playersList, "Todavía no hay jugadores registrados.");
    return;
  }
  state.players.forEach((player) => {
    const item = document.createElement("li");
    item.className = "entity-row";
    const name = document.createElement("span");
    name.textContent = player.name;
    const id = document.createElement("span");
    id.className = "entity-id";
    id.textContent = `#${player.id}`;
    item.append(name, id);
    elements.playersList.append(item);
  });
}

function renderTeams() {
  elements.teamsList.replaceChildren();
  if (!state.teams.length) {
    addEmptyState(elements.teamsList, "Crea dos equipos para abrir la fecha.");
  } else {
    state.teams.forEach((team) => {
      const item = document.createElement("li");
      item.className = "entity-row";
      const name = document.createElement("span");
      name.textContent = team.name;
      const id = document.createElement("span");
      id.className = "entity-id";
      id.textContent = `#${team.id}`;
      item.append(name, id);
      elements.teamsList.append(item);
    });
  }
  renderTeamOptions();
}

function renderTeamOptions() {
  [elements.teamA, elements.teamB].forEach((select) => {
    select.replaceChildren();
    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = state.teams.length ? "Elige un equipo" : "Sin equipos disponibles";
    placeholder.disabled = true;
    placeholder.selected = true;
    select.append(placeholder);
    state.teams.forEach((team) => {
      const option = document.createElement("option");
      option.value = String(team.id);
      option.textContent = team.name;
      select.append(option);
    });
  });
  elements.matchSubmit.disabled = state.teams.length < 2;
}

function teamName(id) {
  const team = state.teams.find((candidate) => candidate.id === id);
  return team ? team.name : `#${id}`;
}

function renderMatches() {
  elements.matchesList.replaceChildren();
  if (!state.matches.length) {
    addEmptyState(elements.matchesList, "Aún no hay resultados en el tablero.");
    return;
  }
  state.matches.forEach((match) => {
    const row = document.createElement("div");
    row.className = "scoreboard";
    const home = document.createElement("span");
    home.className = "scoreboard-team";
    home.textContent = teamName(match.team_a_id);
    const scoreline = document.createElement("span");
    scoreline.className = "scoreline";
    const scoreA = document.createElement("strong");
    scoreA.textContent = String(match.score_a);
    const separator = document.createElement("span");
    separator.className = "score-separator";
    separator.textContent = "—";
    const scoreB = document.createElement("strong");
    scoreB.textContent = String(match.score_b);
    scoreline.append(scoreA, separator, scoreB);
    const away = document.createElement("span");
    away.className = "scoreboard-team";
    away.textContent = teamName(match.team_b_id);
    row.append(home, scoreline, away);
    elements.matchesList.append(row);
  });
}

async function loadPlayers() {
  state.players = await request("/players");
  renderPlayers();
}

async function loadTeams() {
  state.teams = await request("/teams");
  renderTeams();
  renderMatches();
}

async function loadMatches() {
  state.matches = await request("/matches");
  renderMatches();
}

async function loadDashboard() {
  try {
    const [health, players, teams, matches] = await Promise.all([
      request("/health"),
      request("/players"),
      request("/teams"),
      request("/matches"),
    ]);
    elements.instanceValue.textContent = health.instance || "desconocida";
    state.players = players;
    state.teams = teams;
    state.matches = matches;
    renderPlayers();
    renderTeams();
    renderMatches();
  } catch (error) {
    elements.instanceValue.textContent = "No disponible";
    showError(error);
  }
}

async function createEntity(form, path, reload) {
  const input = form.querySelector("input");
  try {
    await request(path, {
      method: "POST",
      body: JSON.stringify({ name: input.value }),
    });
    form.reset();
    await reload();
    clearError();
  } catch (error) {
    showError(error);
  }
}

document.querySelector("#player-form").addEventListener("submit", (event) => {
  event.preventDefault();
  createEntity(event.currentTarget, "/players", loadPlayers);
});

document.querySelector("#team-form").addEventListener("submit", (event) => {
  event.preventDefault();
  createEntity(event.currentTarget, "/teams", loadTeams);
});

document.querySelector("#match-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const data = new FormData(form);
  try {
    await request("/matches", {
      method: "POST",
      body: JSON.stringify({
        team_a_id: Number(data.get("team_a_id")),
        team_b_id: Number(data.get("team_b_id")),
        score_a: Number(data.get("score_a")),
        score_b: Number(data.get("score_b")),
      }),
    });
    form.reset();
    await loadMatches();
    clearError();
  } catch (error) {
    showError(error);
  }
});

loadDashboard();
