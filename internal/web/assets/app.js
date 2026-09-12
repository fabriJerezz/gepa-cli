const state = {
  players: [],
  teams: [],
  matches: [],
};

const elements = {
  errorBanner: document.querySelector("#error-banner"),
  instanceValue: document.querySelector("#instance-value"),
  squadsList: document.querySelector("#squads-list"),
  matchesList: document.querySelector("#matches-list"),
  playerForm: document.querySelector("#player-form"),
  playerTeam: document.querySelector("#player-team"),
  playerSubmit: document.querySelector("#player-submit"),
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
  if (response.status === 204) {
    return null;
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
  const empty = document.createElement("p");
  empty.className = "empty-state";
  empty.textContent = message;
  container.append(empty);
}

function renderSquads() {
  elements.squadsList.replaceChildren();
  if (!state.teams.length) {
    addEmptyState(elements.squadsList, "Todavía no hay plantel en esta cancha.");
    return;
  }

  const playersByTeam = new Map();
  state.players.forEach((player) => {
    const roster = playersByTeam.get(player.team_id) || [];
    roster.push(player);
    playersByTeam.set(player.team_id, roster);
  });

  state.teams.forEach((team) => {
    const card = document.createElement("article");
    card.className = "squad-card";

    const heading = document.createElement("div");
    heading.className = "squad-heading";
    const name = document.createElement("h3");
    name.textContent = team.name;
    const actions = document.createElement("div");
    actions.className = "squad-actions";
    const id = document.createElement("span");
    id.className = "squad-id";
    id.textContent = `#${team.id}`;
    const deleteTeam = document.createElement("button");
    deleteTeam.type = "button";
    deleteTeam.className = "delete-button";
    deleteTeam.textContent = "Eliminar";
    deleteTeam.setAttribute("aria-label", `Eliminar equipo ${team.name}`);
    deleteTeam.addEventListener("click", async () => {
      try {
        await request(`/teams/${team.id}`, { method: "DELETE" });
        await Promise.all([loadTeams(), loadPlayers()]);
        clearError();
      } catch (error) {
        showError(error);
      }
    });
    actions.append(id, deleteTeam);
    heading.append(name, actions);
    card.append(heading);

    const roster = playersByTeam.get(team.id) || [];
    if (!roster.length) {
      const empty = document.createElement("p");
      empty.className = "squad-empty";
      empty.textContent = "Todavía no hay plantel en esta cancha.";
      card.append(empty);
    } else {
      const list = document.createElement("ul");
      list.className = "roster";
      roster.forEach((player) => {
        const item = document.createElement("li");
        const playerLabel = document.createElement("span");
        playerLabel.textContent = `${player.name} · #${player.id}`;
        const deletePlayer = document.createElement("button");
        deletePlayer.type = "button";
        deletePlayer.className = "delete-button";
        deletePlayer.textContent = "Eliminar";
        deletePlayer.setAttribute("aria-label", `Eliminar jugador ${player.name}`);
        deletePlayer.addEventListener("click", async () => {
          try {
            await request(`/players/${player.id}`, { method: "DELETE" });
            await loadPlayers();
            clearError();
          } catch (error) {
            showError(error);
          }
        });
        item.append(playerLabel, deletePlayer);
        list.append(item);
      });
      card.append(list);
    }
    elements.squadsList.append(card);
  });
}

function fillTeamSelect(select, emptyLabel) {
  select.replaceChildren();
  const placeholder = document.createElement("option");
  placeholder.value = "";
    placeholder.textContent = state.teams.length ? "Elige un equipo" : emptyLabel;
  placeholder.disabled = true;
  placeholder.selected = true;
  select.append(placeholder);
  state.teams.forEach((team) => {
    const option = document.createElement("option");
    option.value = String(team.id);
    option.textContent = team.name;
    select.append(option);
  });
}

function renderTeamOptions() {
  fillTeamSelect(elements.playerTeam, "Primero crea un equipo");
  fillTeamSelect(elements.teamA, "No hay equipos disponibles");
  fillTeamSelect(elements.teamB, "No hay equipos disponibles");
  elements.playerSubmit.disabled = state.teams.length < 1;
  elements.matchSubmit.disabled = state.teams.length < 2;
}

function teamName(id) {
  const team = state.teams.find((candidate) => candidate.id === id);
  return team ? team.name : `#${id}`;
}

function renderMatches() {
  elements.matchesList.replaceChildren();
  if (!state.matches.length) {
    addEmptyState(elements.matchesList, "Todavía no hay partidos en el marcador.");
    return;
  }

  state.matches.forEach((match) => {
    const row = document.createElement("div");
    row.className = "match-row";
    const home = document.createElement("span");
    home.className = "match-team";
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
    away.className = "match-team";
    away.textContent = teamName(match.team_b_id);
    const deleteMatch = document.createElement("button");
    deleteMatch.type = "button";
    deleteMatch.className = "delete-button";
    deleteMatch.textContent = "Eliminar";
    deleteMatch.setAttribute("aria-label", `Eliminar partido #${match.id}`);
    deleteMatch.addEventListener("click", async () => {
      try {
        await request(`/matches/${match.id}`, { method: "DELETE" });
        await loadMatches();
        clearError();
      } catch (error) {
        showError(error);
      }
    });
    row.append(home, scoreline, away, deleteMatch);
    elements.matchesList.append(row);
  });
}

function renderAll() {
  renderSquads();
  renderTeamOptions();
  renderMatches();
}

async function loadPlayers() {
  state.players = await request("/players");
  renderSquads();
}

async function loadTeams() {
  state.teams = await request("/teams");
  renderSquads();
  renderTeamOptions();
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
    renderAll();
  } catch (error) {
    elements.instanceValue.textContent = "No disponible";
    showError(error);
  }
}

elements.playerForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const data = new FormData(form);
  try {
    await request("/players", {
      method: "POST",
      body: JSON.stringify({
        name: data.get("name"),
        team_id: Number(data.get("team_id")),
      }),
    });
    form.reset();
    await loadPlayers();
    clearError();
  } catch (error) {
    showError(error);
  }
});

document.querySelector("#team-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const data = new FormData(form);
  try {
    await request("/teams", {
      method: "POST",
      body: JSON.stringify({ name: data.get("name") }),
    });
    form.reset();
    await loadTeams();
    clearError();
  } catch (error) {
    showError(error);
  }
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
