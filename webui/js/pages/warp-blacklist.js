// Page: WARP 黑名单 — 查看、手动补封和提前解封被判死的公网出口段。
import { api, hasSession } from "../api.js";
import { t, onChange } from "../i18n.js";
import { $, el, emptyRow, formatNumber, localTime, toast } from "../ui.js";

let loading = false;
let lastData = null;

// remaining renders the seconds the API reports as a coarse countdown. The page
// reloads on every visit, so this never needs to tick on its own.
function remaining(seconds) {
  const total = Math.max(0, Number(seconds) || 0);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (hours >= 24) {
    const days = Math.floor(hours / 24);
    return t(`${days} 天 ${hours % 24} 小时`, "warp.daysHours", days, hours % 24);
  }
  if (hours > 0) return t(`${hours} 小时 ${minutes} 分`, "warp.hoursMinutes", hours, minutes);
  return t(`${minutes} 分`, "warp.minutes", minutes);
}

async function load() {
  if (loading) return;
  loading = true;
  $("#warp-blacklist-refresh").disabled = true;
  try {
    const data = await api("/api/warp/blacklist");
    if (!hasSession()) return;
    lastData = data;
    render(data);
  } catch (error) {
    $("#warp-blacklist-status").textContent = t(
      `读取失败：${error.message}`,
      "warp.loadFailed",
      error.message,
    );
  } finally {
    loading = false;
    $("#warp-blacklist-refresh").disabled = false;
  }
}

function render(data) {
  const entries = data.entries || [];
  $("#warp-blacklist-count").textContent = t(`${entries.length} 个网段`, "warp.count", entries.length);
  $("#warp-blacklist-status").textContent = data.state_path
    ? t(`状态文件：${data.state_path}`, "warp.statePath", data.state_path)
    : t("状态未持久化：重启后名单会丢失", "warp.noState");
  const host = $("#warp-blacklist-list");
  host.replaceChildren();
  for (const entry of entries) {
    const release = el("button", {
      type: "button",
      class: "btn btn-sm btn-ghost",
      text: t("解封", "warp.release"),
    });
    release.addEventListener("click", () => releaseEntry(entry.subnet, release));
    host.append(
      el("tr", {}, [
        el("td", { class: "mono", text: entry.subnet }),
        el("td", { text: entry.until ? localTime(entry.until) : "—" }),
        el("td", { text: remaining(entry.remaining_seconds) }),
        el("td", {
          class: `status ${entry.manual ? "warn" : "ok"}`,
          text: entry.manual ? t("手动", "warp.manual") : t("配额", "warp.auto"),
        }),
        el("td", { text: formatNumber(entry.active) }),
        el("td", {}, [release]),
      ]),
    );
  }
  if (!entries.length) emptyRow(host, 6, t("当前没有封禁中的网段", "warp.empty"));
}

async function releaseEntry(subnet, button) {
  button.disabled = true;
  try {
    await api("/api/warp/blacklist/release", {
      method: "POST",
      body: JSON.stringify({ subnet }),
    });
    toast(t(`已解封 ${subnet}`, "warp.released", subnet));
    await load();
  } catch (error) {
    toast(error.message, true);
  } finally {
    button.disabled = false;
  }
}

async function ban(event) {
  event.preventDefault();
  const button = $("#warp-blacklist-ban");
  const subnet = $("#warp-blacklist-subnet").value.trim();
  const hours = Number($("#warp-blacklist-hours").value || 24);
  button.disabled = true;
  try {
    const data = await api("/api/warp/blacklist/ban", {
      method: "POST",
      body: JSON.stringify({ subnet, hours }),
    });
    toast(t(`已封禁 ${data.entry.subnet}`, "warp.banned", data.entry.subnet));
    $("#warp-blacklist-subnet").value = "";
    await load();
  } catch (error) {
    toast(error.message, true);
  } finally {
    button.disabled = false;
  }
}

async function reloadFile() {
  const button = $("#warp-blacklist-reload");
  button.disabled = true;
  try {
    const data = await api("/api/warp/blacklist/reload", { method: "POST", body: "{}" });
    toast(
      t(`已重载：新增 ${data.added}，移除 ${data.removed}`, "warp.reloaded", data.added, data.removed),
    );
    await load();
  } catch (error) {
    toast(error.message, true);
  } finally {
    button.disabled = false;
  }
}

export default {
  id: "warp-blacklist",
  title: "WARP 黑名单",
  sub: "免费额度按公网出口段计数；这里查看、补封或提前解封被判死的段。",
  init() {
    $("#warp-blacklist-form").addEventListener("submit", ban);
    $("#warp-blacklist-refresh").addEventListener("click", load);
    $("#warp-blacklist-reload").addEventListener("click", reloadFile);
    onChange(() => {
      if (lastData) render(lastData);
    });
  },
  show: load,
  refresh: load,
  reset() {
    lastData = null;
    $("#warp-blacklist-list").replaceChildren();
    $("#warp-blacklist-count").textContent = "";
    $("#warp-blacklist-status").textContent = "";
  },
};
