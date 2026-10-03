import React, { useEffect, useRef, useState } from "react";
import { clientDisplay, gatewayDisplay, keyDisplay, routeMemberDisplay } from "./v13-state";
import { V13View } from "./V13View";
import { RequestAccounting } from "./BudgetConsole";

type API = <T = any>(
  path: string,
  method?: string,
  body?: unknown,
) => Promise<T>;
type Props = {
  api: API;
  page: string;
  sources: any[];
  logins: Record<string, { status: string; message?: string; authorization_url?: string }>;
  onLogin: (source: any) => void;
  onCancelLogin: (source: any) => Promise<void>;
  keys: any[];
  requests: any[];
  hasMoreRequests: boolean;
  onMoreRequests: () => Promise<void>;
  routes: any[];
  aliases: any[];
  status: any;
  settings: any;
  usage: any;
  detail: any;
  filter: string;
  busy: boolean;
  pending: string[];
  onPage: (page: string) => void;
  onManage: (page: string, id?: string) => void;
  onAdd: (preset?: string) => void;
  onEdit: (source: any) => void;
  onDetail: (id: string) => void;
  onCloseDetail: () => void;
  onReconcile: () => Promise<void>;
  onFilter: (status: string) => void;
  onRefresh: () => Promise<void>;
  run: (work: () => Promise<void>, id?: string) => Promise<void>;
  onError: (message: string) => void;
};
const tabs = [
  ["概览", "overview", "Overview"],
  ["来源", "sources", "Sources"],
  ["模型", "models", "Models"],
  ["路由", "routes", "Routes"],
  ["API Keys", "keys", "Keys"],
  ["请求", "requests", "Requests"],
  ["用量", "usage", "Usage"],
];
const dots: Record<string, string> = {
  passed: "var(--ok)",
  succeeded: "var(--ok)",
  failed: "var(--err)",
  rejected: "var(--err)",
  needs_reauth: "var(--err)",
  unverified: "var(--warn)",
  interrupted: "var(--warn)",
  stale: "var(--warn)",
  streaming: "var(--accent)",
  dispatching: "var(--accent)",
};
const labels: Record<string, [string, string]> = {
  passed: ["已验证", "Verified"],
  succeeded: ["完成", "Completed"],
  failed: ["失败", "Failed"],
  unverified: ["结果未知", "Unknown"],
  interrupted: ["中断", "Interrupted"],
  cancelled: ["已取消", "Cancelled"],
  streaming: ["流式中", "Streaming"],
  dispatching: ["请求中", "Active"],
  stale: ["已过期", "Stale"],
  untested: ["未验证", "Untested"],
  unknown: ["未知", "Unknown"],
  logged_in: ["已登录", "Signed in"],
  configured: ["凭据已设置", "Configured"],
  not_configured: ["未配置", "Not configured"],
  logged_out: ["未登录", "Signed out"],
  needs_reauth: ["需重新登录", "Sign in again"],
  available: ["已观测", "Observed"],
  unsupported: ["不支持", "Unsupported"],
};
const text = (s: string, en = false) => labels[s]?.[en ? 1 : 0] ?? s ?? "—";
const number = (v: number | null | undefined) =>
  v == null ? "—" : v.toLocaleString();
const compact = (v: number | null | undefined) =>
  v == null
    ? "—"
    : Intl.NumberFormat("en", {
        notation: "compact",
        maximumFractionDigits: 2,
      }).format(v);
const date = (v?: string | null) => (v ? new Date(v).toLocaleString() : "—");
const cost = (u: any) =>
  Object.entries(u?.estimated_cost_by_currency ?? {})
    .map(
      ([currency, amount]) =>
        `${currency === "USD" ? "$" : currency + " "}${amount}`,
    )
    .join(" · ") || "—";
const seg = (active: boolean) => ({
  bg: active
    ? "linear-gradient(180deg,color-mix(in oklch, var(--accent) 20%, var(--panel)),var(--panel))"
    : "transparent",
  fg: active ? "var(--ink)" : "var(--ink2)",
  sh: active
    ? "inset 0 0 0 1px color-mix(in oklch, var(--accent) 45%, transparent),0 0 18px -6px var(--accent)"
    : "none",
  bd: active
    ? "color-mix(in oklch, var(--accent) 60%, transparent)"
    : "var(--line)",
});
function logo(provider: string) {
  const key = /^claudecode/.test(provider)
    ? "claudecode-color"
    : /claude|anthropic/.test(provider)
      ? "claude-color"
      : /deepseek/.test(provider)
        ? "deepseek-color"
        : /ollama|local/.test(provider)
          ? "ollama"
          : /gemini|google|vertex/.test(provider)
            ? "gemini-color"
            : /opencode/.test(provider)
              ? "opencode"
              : provider === "codex"
                ? "codex-color"
                : "openai";
  return {
    logo: `/assets/${key}.svg`,
    mono: ["openai", "ollama", "opencode"].includes(key) ? "1" : "0",
  };
}
function requestRow(r: any) {
  return {
    ...r,
    id: r.id,
    key: r.client_name || (r.origin === "admin_test" ? "管理测试" : "—"),
    proto: r.protocol || "responses",
    model: r.requested_model || "—",
    sent: r.sent_model || r.reported_model || "—",
    time: r.started_at
      ? new Date(r.started_at).toLocaleTimeString("en-GB")
      : "—",
    dur: r.duration_ms == null ? "—" : `${(r.duration_ms / 1000).toFixed(2)}s`,
    tok: `${compact(r.usage?.input_tokens)} / ${compact(r.usage?.output_tokens)}`,
    dot: dots[r.status] || "var(--ink3)",
    stC: dots[r.status] || "var(--ink2)",
    stZh: text(r.status),
    stEn: text(r.status, true),
    ttft: r.first_content_at && r.started_at ? `${((Date.parse(r.first_content_at) - Date.parse(r.started_at)) / 1000).toFixed(2)}s` : "—",
    cost: r.estimated_cost != null && r.price_snapshot ? `${r.price_snapshot.currency} ${r.estimated_cost}` : r.partial_estimated_cost != null && r.price_snapshot ? `${r.price_snapshot.currency} ${r.partial_estimated_cost} (partial)` : "—",
    deliv: r.delivery_status || "unknown",
    obs: r.observation_status || "unknown",
  };
}
export function V13Console(p: Props) {
  const [mode, setMode] = useState(() => {
    try {
      return localStorage.getItem("cove.ui.mode") === "light"
        ? "light"
        : "dark";
    } catch {
      return "dark";
    }
  });
  const [lang, setLang] = useState(() => {
    try {
      return localStorage.getItem("cove.ui.lang") === "en" ? "en" : "zh";
    } catch {
      return "zh";
    }
  });
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [models, setModels] = useState<any[]>([]),
    [clients, setClients] = useState<any[]>([]),
    [budgets, setBudgets] = useState<any[]>([]);
  const [mq, setMq] = useState(""),
    [sourceFilter, setSourceFilter] = useState("all"),
    [sort, setSort] = useState("default");
  const [palette, setPalette] = useState(false),
    [paletteQuery, setPaletteQuery] = useState(""),
    [paletteIndex, setPaletteIndex] = useState(0);
  const [adding, setAdding] = useState(false);
  const [keyClock, setKeyClock] = useState(Date.now);
  useEffect(() => {
    if (p.page !== "API Keys") return;
    setKeyClock(Date.now());
    const timer = window.setInterval(() => setKeyClock(Date.now()), 5000);
    return () => window.clearInterval(timer);
  }, [p.page]);
  const [picker, setPicker] = useState<any>(null),
    [pq, setPq] = useState("");
  const currentPicker = clients.find(c => c.kind === picker?.kind) || picker;
  const selectedPickerModel = currentPicker?.configuration?.state === "configured" ? currentPicker.configuration.model : "";
  const [range, setRange] = useState("14d"),
    [rangeUsage, setRangeUsage] = useState<any>({}),
    [today, setToday] = useState<any>({}),
    [hour, setHour] = useState<any>({}),
    [bars, setBars] = useState<any[]>([]),
    [keyUse, setKeyUse] = useState<any[]>([]);
  const [previews, setPreviews] = useState<Record<string, any>>({}),
    [latency, setLatency] = useState<
      Record<string, { duration: number; status: string }>
    >({});
  const [nextPreview, setNextPreview] = useState<any>(null);
  const nextRevision = useRef(0);
  const codingAlias = p.aliases.find((a) => a.public_model === "coding");
  const nextPreviewInputs = JSON.stringify([p.routes, p.sources, p.aliases, p.settings.version]);
  useEffect(() => {
    const revision = ++nextRevision.current;
    setNextPreview(null);
    if (p.page !== "路由" || !codingAlias) return;
    void p.api(`routes/${codingAlias.route_id}/preview`, "POST", {
      model: "coding", protocol: "responses",
    }).then((result) => {
      if (revision === nextRevision.current) setNextPreview(result);
    }).catch((error) => {
      if (revision === nextRevision.current) {
        setNextPreview({ error: error.message });
        p.onError(`coding 预览失败：${error.message}`);
      }
    });
    return () => { nextRevision.current++; };
  }, [p.page, nextPreviewInputs]);
  const loadRevision = useRef(0),
    usageRevision = useRef(0);
  useEffect(() => {
    document.documentElement.dataset.mode = mode;
    document.documentElement.dataset.lang = lang;
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
    try {
      localStorage.setItem("cove.ui.mode", mode);
      localStorage.setItem("cove.ui.lang", lang);
    } catch {
      /* Appearance still works for this session. */
    }
  }, [mode, lang]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((v) => !v);
        setPaletteQuery("");
        setPaletteIndex(0);
      }
      if (e.key === "Escape") {
        setPalette(false);
        setPicker(null);
        setAdding(false);
        p.onCloseDetail();
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [p.onCloseDetail]);
  useEffect(() => {
    const revision = ++loadRevision.current;
    const paths = ["models", "clients", "budgets"];
    void Promise.allSettled(paths.map((path) => p.api(path))).then(
      (results) => {
        if (revision !== loadRevision.current) return;
        const setters = [setModels, setClients, setBudgets];
        results.forEach((r, i) => {
          if (r.status === "fulfilled") setters[i](r.value.items);
          else {
            if (paths[i] === "clients") setClients(old => old.map(c => ({...c, configuration: {...c.configuration, state: "unavailable", reason: r.reason.message}})));
            p.onError(`${paths[i]}：${r.reason.message}`);
          }
        });
      },
    );
    const now = new Date(),
      start = new Date(now);
    start.setHours(0, 0, 0, 0);
    void Promise.allSettled([
      p.api(
        `usage?from=${encodeURIComponent(start.toISOString())}&to=${encodeURIComponent(now.toISOString())}`,
      ),
      p.api(
        `usage?from=${encodeURIComponent(new Date(now.getTime() - 3600000).toISOString())}&to=${encodeURIComponent(now.toISOString())}`,
      ),
    ]).then((results) => {
      if (revision !== loadRevision.current) return;
      results.forEach((r, i) => {
        if (r.status === "fulfilled") (i === 0 ? setToday : setHour)(r.value);
        else p.onError(`用量读取失败：${r.reason.message}`);
      });
    });
    return () => {
      loadRevision.current++;
    };
  }, [p.usage]);
  useEffect(() => {
    if (p.page !== "用量") return;
    const revision = ++usageRevision.current,
      now = new Date(),
      days = range === "24h" ? 1 : Number(range.slice(0, -1));
    const start = new Date(now.getTime() - days * 86400000);
    const intervals = Array.from({ length: days }, (_, i) => ({
      from: new Date(start.getTime() + i * 86400000),
      to: i === days - 1 ? now : new Date(start.getTime() + (i + 1) * 86400000),
    }));
    const query = (from: Date, to: Date) =>
      `from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`;
    const paths = [
      `usage?${query(start, now)}`,
      ...intervals.map((i) => `usage?${query(i.from, i.to)}`),
      ...p.keys.map(
        (k) =>
          `usage?${query(start, now)}&client_key_id=${encodeURIComponent(k.id)}`,
      ),
    ];
    void Promise.allSettled(paths.map((path) => p.api(path))).then(
      (results) => {
        if (revision !== usageRevision.current) return;
        const first = results[0];
        if (first.status === "fulfilled") setRangeUsage(first.value);
        else setRangeUsage({});
        const rows = results
          .slice(1, days + 1)
          .map((r, i) => ({
            d: intervals[i].from.toLocaleDateString(undefined, {
              day: "2-digit",
              month: days === 1 ? "short" : undefined,
            }),
            known:
              r.status === "fulfilled"
                ? r.value.known_input_tokens + r.value.known_output_tokens
                : null,
            unknown:
              r.status === "fulfilled" ? r.value.usage_unknown_requests : null,
          }));
        const max = Math.max(1, ...rows.map((r) => r.known ?? 0));
        setBars(
          rows.map((r) => ({
            ...r,
            k: r.known == null ? "—" : (r.known / 1e6).toFixed(2),
            hk: r.known == null ? "0%" : `${(r.known / max) * 100}%`,
            hu: r.unknown ? "8%" : "0%",
            hasU: r.unknown == null || r.unknown > 0,
          })),
        );
        const total =
          first.status === "fulfilled"
            ? first.value.known_input_tokens + first.value.known_output_tokens
            : null;
        setKeyUse(
          results
            .slice(days + 1)
            .map((r, i) => ({
              key: p.keys[i].name,
              req: r.status === "fulfilled" ? number(r.value.requests) : "—",
              tok:
                r.status === "fulfilled"
                  ? compact(
                      r.value.known_input_tokens + r.value.known_output_tokens,
                    )
                  : "—",
              unk:
                r.status === "fulfilled"
                  ? number(r.value.usage_unknown_requests)
                  : "—",
              cost: r.status === "fulfilled" ? cost(r.value) : "—",
              pct:
                r.status === "fulfilled" && total
                  ? `${Math.round(((r.value.known_input_tokens + r.value.known_output_tokens) / total) * 100)}%`
                  : "—",
            })),
        );
        const failures = results.filter(
          (r): r is PromiseRejectedResult => r.status === "rejected",
        );
        if (failures.length)
          p.onError(`用量读取失败：${failures[0].reason.message}`);
      },
    );
    return () => {
      usageRevision.current++;
    };
  }, [p.page, p.usage, range]);
  useEffect(() => {
    if (!palette && !picker && !adding && !p.detail) return;
    const previous = document.activeElement as HTMLElement | null;
    const overlay = document.querySelector<HTMLElement>(
      '[data-screen-label="Command palette"], [data-screen-label="Picker · Model"], [data-screen-label="Sheet · Add source"], [data-screen-label="Drawer · Request"]',
    );
    if (!overlay) return;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const focusable = () =>
      Array.from(
        overlay.querySelectorAll<HTMLElement>(
          "button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], summary",
        ),
      ).filter((el) => el.getClientRects().length > 0);
    focusable()[0]?.focus();
    const trap = (e: KeyboardEvent) => {
      if (e.key !== "Tab") return;
      const nodes = focusable(),
        first = nodes[0],
        last = nodes[nodes.length - 1];
      if (!first) {
        e.preventDefault();
        return;
      }
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    overlay.addEventListener("keydown", trap);
    return () => {
      document.body.style.overflow = overflow;
      overlay.removeEventListener("keydown", trap);
      if (previous?.isConnected) previous.focus();
    };
  }, [palette, picker, adding, p.detail]);
  async function mutate(path: string, method: string, body: any, id: string) {
    await p.run(async () => {
      try { await p.api(path, method, body); }
      catch (error) {
        if ((error as Error & {status?: number}).status === 409) {
          try { await p.onRefresh(); } catch { /* Preserve the original conflict. */ }
        }
        throw error;
      }
      await p.onRefresh();
    }, id);
  }
  const manage = (page = p.page, id?: string) => p.onManage(page, id);
  const go = (page: string) => {
    setPalette(false);
    setPicker(null);
    p.onPage(page);
    window.scrollTo(0, 0);
  };
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timer);
  }, [copied]);
  const copyBase = () =>
    p.run(async () => {
      await navigator.clipboard.writeText(`http://${p.status.listen}/v1`);
      setCopied(true);
    });
  const viewSources = p.sources.map((s) => {
    const windows = s.quota?.windows ?? [],
      current = s.quota?.status === "available";
    const dot = p.status.sources_stale ? "var(--warn)" : !s.enabled
      ? "var(--ink3)"
      : dots[s.auth_status] ||
        dots[s.quota?.call_health] ||
        (s.verification?.status === "passed" ? "var(--ok)" : "var(--ink3)");
    const busy = p.busy || p.pending.includes(s.id),
      toggle = () =>
        mutate(
          `sources/${s.id}`,
          "PATCH",
          { version: s.version, enabled: !s.enabled },
          s.id,
        );
    return {
      ...s,
      ...logo(s.provider + " " + s.kind + " " + s.name),
      on: s.enabled,
      busy,
      toggleLabel: `${s.name} ${s.enabled ? "停用" : "启用"}`,
      open: !!open[s.id],
      chev: open[s.id] ? "expand_less" : "expand_more",
      dim: s.enabled ? "1" : "0.45",
      dot,
      models: String(s.models.length),
      typeZh:
        s.kind === "codex_subscription"
          ? "ChatGPT 订阅"
          : s.kind === "none"
            ? "本地"
            : "API Key",
      typeEn:
        s.kind === "codex_subscription"
          ? "ChatGPT plan"
          : s.kind === "none"
            ? "Local"
            : "API key",
      proto: s.native_protocol,
      acct: s.account_id || "—",
      base: s.base_url,
      gen: `gen ${s.binding_generation}`,
      auth: s.kind,
      authS: text(s.auth_status),
      health: p.status.sources_stale ? text("unknown", lang === "en") : s.enabled
        ? text(s.quota?.call_health || s.verification?.status || "unknown")
        : "disabled",
      vZh: text(p.status.sources_stale ? "unknown" : s.verification?.status),
      vEn: text(p.status.sources_stale ? "unknown" : s.verification?.status, true),
      quotaZh: text(s.quota?.status || "unknown"),
      quotaEn: text(s.quota?.status || "unknown", true),
      codex: s.kind === "codex_subscription",
      loginWaiting: ["pending", "exchanging", "awaiting_confirmation"].includes(p.logins[s.id]?.status),
      loginDisabled: busy || ["pending", "exchanging"].includes(p.logins[s.id]?.status),
      loginZh: p.logins[s.id]?.status === "awaiting_confirmation" ? "确认更换账号" :
        ["pending", "exchanging"].includes(p.logins[s.id]?.status) ? "正在登录…" :
        s.auth_status === "logged_in" ? "更换 ChatGPT 账号" : "登录 ChatGPT",
      loginEn: p.logins[s.id]?.status === "awaiting_confirmation" ? "Confirm account change" :
        ["pending", "exchanging"].includes(p.logins[s.id]?.status) ? "Signing in…" :
        s.auth_status === "logged_in" ? "Change ChatGPT account" : "Sign in to ChatGPT",
      loginMessage: p.logins[s.id]?.status !== "idle" ? p.logins[s.id]?.message : "",
      loginUrl: p.logins[s.id]?.status === "pending" ? p.logins[s.id]?.authorization_url : undefined,
      onLogin: () => p.logins[s.id]?.status === "awaiting_confirmation" ? manage("来源", s.id) : p.onLogin(s),
      onCancelLogin: () => void p.onCancelLogin(s),
      hasBars: windows.length > 0,
      bars: windows.map((w: any) => ({
        l: w.limit_name || w.dimension,
        w: current && w.used_percent != null ? `${w.used_percent}%` : "0%",
        v: current && w.used_percent != null ? `${w.used_percent}%` : "—",
      })),
      tgBg: s.enabled ? "var(--ok)" : "var(--line)",
      tgX: s.enabled ? "16px" : "2px",
      adjBg: s.allow_parameter_adjustment ? "var(--ok)" : "var(--line)",
      adjX: s.allow_parameter_adjustment ? "16px" : "2px",
      onToggle: toggle,
      onExpand: () => setOpen((o) => ({ ...o, [s.id]: !o[s.id] })),
      toggleAdj: () =>
        mutate(
          `sources/${s.id}`,
          "PATCH",
          {
            version: s.version,
            allow_parameter_adjustment: !s.allow_parameter_adjustment,
          },
          s.id,
        ),
      latL:
        latency[s.id] == null
          ? lang === "zh"
            ? "测试"
            : "Test"
          : `${latency[s.id].duration} ms`,
      latC:
        latency[s.id] == null
          ? "var(--ink2)"
          : latency[s.id].status === "succeeded"
            ? "var(--okT)"
            : "var(--errT)",
      onLat: () =>
        p.run(async () => {
          const result = await p.api(`sources/${s.id}/test`, "POST", {
            model: s.models[0],
          });
          if (result.request?.duration_ms != null)
            setLatency((l) => ({
              ...l,
              [s.id]: {
                duration: result.request.duration_ms,
                status: result.request.status,
              },
            }));
          await p.onRefresh();
          p.onDetail(result.request.id);
        }, s.id),
      actions: {
        text: () => manage("来源", s.id),
        tools: () => manage("来源", s.id),
        models: () => manage("模型"),
        quota: () => manage("来源", s.id),
        edit: () => p.onEdit(s),
        delete: () => manage("来源", s.id),
      },
    };
  });
  const viewClients = clients.map((c) => ({
    ...c,
    ...logo(c.kind === "claude" ? "claudecode" : c.kind),
    id: c.kind,
    ver: c.version,
    ...clientDisplay(c, lang === "en"),
    canPick: c.status === "installed",
    cfgVis: c.status === "not_installed" ? "hidden" : "visible",
    dim: c.status === "not_installed" ? "0.5" : "1",
    onPick: () => {
      setPicker(c);
      setPq("");
    },
    onCfg: () => manage("工具", c.configuration?.model ? `${c.kind}:${c.configuration.model}` : c.kind),
  }));
  const pool = viewSources
    .filter((s) => s.codex)
    .map((s) => ({
      ...s,
      plan: s.name,
      noQ: !s.quota?.windows?.length || s.quota?.status !== "available",
      stZh: s.health,
      stEn: s.health,
      tgBg: s.enabled
        ? "linear-gradient(135deg,var(--accent),var(--accent2))"
        : "var(--line)",
      q: (s.quota?.windows ?? []).map((w: any) => ({
        zh: w.limit_name || w.dimension,
        en: w.limit_name || w.dimension,
        v:
          s.quota.status === "available" && w.used_percent != null
            ? `${w.used_percent}%`
            : "—",
        w:
          s.quota.status === "available" && w.used_percent != null
            ? `${w.used_percent}%`
            : "0%",
        fill:
          w.used_percent >= 90
            ? "var(--err)"
            : w.used_percent >= 70
              ? "var(--warn)"
              : "linear-gradient(90deg,var(--accent),var(--accent2))",
        rZh: w.reset_at ? date(w.reset_at) : "—",
        rEn: w.reset_at ? date(w.reset_at) : "—",
      })),
    }));
  const caps = [
    ["text_fields", "text_json"],
    ["stream", "stream"],
    ["build", "tools"],
    ["call_split", "parallel_tools"],
    ["image", "vision"],
    ["data_object", "json_schema"],
    ["psychology", "reasoning"],
  ];
  const viewModels = models.map((m) => ({
    ...m,
    priceOrder:
      m.price?.input_per_million == null
        ? Infinity
        : Number(m.price.input_per_million),
    m: m.upstream_model,
    ctx: m.context_limit == null ? "—" : compact(m.context_limit),
    disc: m.discovery || "manual",
    v: text(m.verification || "untested", lang === "en"),
    vC: dots[m.verification] || "var(--ink3)",
    vDot: dots[m.verification] || "var(--ink3)",
    price: m.price
      ? `${m.price.currency} ${m.price.input_per_million} · ${m.price.output_per_million}`
      : p.sources.find((s) => s.id === m.source_id)?.kind ===
          "codex_subscription"
        ? "subscription"
        : "—",
    caps: caps.map(([icon, feature]) => ({
      icon,
      c: (m.verification_results ?? []).some(
        (r: any) =>
          r.feature === feature &&
          r.status === "passed" &&
          r.source_generation ===
            p.sources.find((s) => s.id === m.source_id)?.binding_generation &&
          m.verification !== "stale",
      )
        ? "var(--okT)"
        : "var(--ink3)",
      title: `${feature} · ${(m.verification_results ?? []).some((r: any) => r.feature === feature && r.status === "passed" && r.source_generation === p.sources.find((s) => s.id === m.source_id)?.binding_generation && m.verification !== "stale") ? "verified" : "unverified"}`,
    })),
  }));
  const groups = p.sources
    .filter((s) => sourceFilter === "all" || sourceFilter === s.id)
    .map((s) => ({
      ...logo(s.provider + " " + s.kind),
      name: s.name,
      items: viewModels
        .filter(
          (m) =>
            m.source_id === s.id &&
            m.m.toLowerCase().includes(mq.toLowerCase()),
        )
        .sort((a, b) =>
          sort === "ctx"
            ? (b.context_limit ?? 0) - (a.context_limit ?? 0)
            : sort === "price"
              ? a.priceOrder - b.priceOrder
              : 0,
        ),
    }))
    .filter((g) => g.items.length)
    .map((g) => ({ ...g, n: String(g.items.length) }));
  const viewRoutes = p.routes.map((r) => {
    const alias = p.aliases.find((a) => a.route_id === r.id);
    const signature = JSON.stringify([r.version, alias?.public_model, p.settings.version, r.runtime]);
    const cachedPreview = previews[r.id];
    const preview = cachedPreview?.signature === signature ? cachedPreview : undefined;
    const total = r.members.reduce((t: number, m: any) => t + m.weight, 0);
    const candidate = (c: any, i: number) => {
      const s = p.sources.find((s) => s.id === c.source_id);
      return {
        ...logo(s?.provider || "openai"),
        src: s?.name || c.source_id,
        m: c.upstream_model,
        rank: String(i + 1),
        zh: c.reason || "",
        en: c.reason || "",
      };
    };
    return {
      ...r,
      selZh: r.strategy,
      selEn: r.strategy,
      att: r.max_attempts,
      compatZh: r.allow_parameter_adjustment ? "允许调整" : "严格",
      compatEn: r.allow_parameter_adjustment ? "Adjustments" : "Strict",
      runtimeTitle: r.runtime ? `路由并发 ${r.runtime.active_requests}/${r.max_concurrent ?? "∞"} · 排队 ${r.runtime.queued_requests} · 仅配置预览` : "路由运行状态尚未读取",
      busy: p.busy || p.pending.includes(r.id),
      previewed: !!preview,
      pvLabelZh: preview ? "收起" : cachedPreview ? "更新预览" : "预览",
      pvLabelEn: preview ? "Hide" : cachedPreview ? "Refresh" : "Preview",
      pvIn:
        preview?.requested_model ||
        p.aliases.find((a) => a.route_id === r.id)?.public_model ||
        "—",
      pickZh: preview?.selected_candidate
        ? `选择 ${preview.selected_candidate.upstream_model}`
        : preview?.error || "没有可用候选",
      pickEn: preview?.selected_candidate
        ? `Selected ${preview.selected_candidate.upstream_model}`
        : preview?.error || "No candidate",
      ok: (preview?.candidates ?? [])
        .filter((c: any) => c.eligible)
        .map(candidate),
      no: (preview?.candidates ?? [])
        .filter((c: any) => !c.eligible)
        .map(candidate),
      members: r.members.map((m: any) => {
        const model = models.find((v) => v.id === m.model_id),
          source = p.sources.find((s) => s.id === model?.source_id);
        return {
          ...logo(source?.provider || "openai"),
          p: m.priority,
          src: source?.name || "—",
          m: model?.upstream_model || m.model_id,
          ww: total ? `${(m.weight / total) * 100}%` : "0%",
          share:
            r.strategy === "weighted_round_robin" && total
              ? `${Math.round((m.weight / total) * 100)}%`
              : `W${m.weight}`,
          ...routeMemberDisplay(r, m, source, p.status, lang === "en"),
        };
      }),
      onPreview: () => {
        if (preview) {
          setPreviews((old) => {
            const next = { ...old };
            delete next[r.id];
            return next;
          });
          return;
        }
        void p.run(async () => {
          if (!alias) throw new Error("先添加公开模型别名，再预览路由。");
          const result = await p.api(`routes/${r.id}/preview`, "POST", {
            model: alias.public_model,
            protocol: "responses",
          });
          setPreviews((old) => ({ ...old, [r.id]: {...result, signature} }));
        }, r.id);
      },
    };
  });
  const commands = [
    ...tabs.map(([zh, , en]) => ({
      icon: "arrow_forward",
      zh: `打开 ${zh}`,
      en: `Open ${en}`,
      run: () => go(zh),
    })),
    ...[
      ["设置", "Settings"],
      ["工具", "Clients"],
      ["预算", "Budgets"],
      ["运维", "Operations"],
    ].map(([zh, en]) => ({
      icon: "settings",
      zh,
      en,
      run: () => {
        setPalette(false);
        manage(zh);
      },
    })),
    {
      icon: "content_copy",
      zh: "复制 Base URL",
      en: "Copy base URL",
      run: () => {
        setPalette(false);
        void copyBase();
      },
    },
  ].filter((c) =>
    `${c.zh} ${c.en}`.toLowerCase().includes(paletteQuery.toLowerCase()),
  );
  const pickModel = (model: string) => {
    setPicker(null);
    p.onManage("工具", `${picker.kind}:${model}`);
  };
  const request = p.detail?.request,
    attempts = p.detail?.attempts ?? [];
  const empty = (message: string, en: string) => (
    <div className="v13-empty">
      <span data-l="zh">{message}</span>
      <span data-l="en">{en}</span>
    </div>
  );
  const v: Record<string, any> = {
    pg: Object.fromEntries([
      ...tabs.map(([zh, id]) => [id, p.page === zh]),
      ["settings", p.page === "设置"],
    ]),
    tabs: tabs.map(([zh, , en]) => ({
      zh: zh === "API Keys" ? "Keys" : zh,
      en,
      ...seg(p.page === zh),
      onClick: () => go(zh),
    })),
    setBg: p.page === "设置" ? "var(--sunk)" : "transparent",
    goSettings: () => go("设置"),
    modeIcon: mode === "dark" ? "light_mode" : "dark_mode",
    toggleMode: () => setMode((m) => (m === "dark" ? "light" : "dark")),
    langLabel: lang === "zh" ? "EN" : "中",
    toggleLang: () => setLang((l) => (l === "zh" ? "en" : "zh")),
    listen: p.status.listen || "—",
    baseURL: `http://${p.status.listen}/v1`,
    liveLabel: `${p.status.listen?.split(":").pop() || "—"} · ${p.status.runtime_stale ? "—" : p.status.active_requests ?? "—"} live`,
    activeRequests: p.status.runtime_stale ? "—" : p.status.active_requests ?? "—",
    maxConcurrent: p.settings.limits?.max_concurrent ?? "—",
    ...gatewayDisplay(p.status, p.sources),
    requestCount: number(today.requests),
    tokenCount:
      today.requests == null
        ? "—"
        : compact(today.known_input_tokens + today.known_output_tokens),
    cost: cost(today),
    usageTokenCount:
      rangeUsage.requests == null
        ? "—"
        : compact(
            rangeUsage.known_input_tokens + rangeUsage.known_output_tokens,
          ),
    copyBase,
    baseIcon: copied ? "check" : "content_copy",
    goSources: () => go("来源"),
    detectClients: () =>
      p.run(async () => {
        try {
          const c = await p.api("clients");
          setClients(c.items);
        } catch (error) {
          setClients(old => old.map(c => ({...c, configuration: {...c.configuration, state: "unavailable", reason: (error as Error).message}})));
          throw error;
        }
      }),
    sources: viewSources,
    clients: viewClients,
    xPool: pool,
    flowClients: viewClients
      .slice(0, 4)
      .map((c, i) => ({ ...c, top: `${[45, 95, 145, 195][i] - 18}px` })),
    flowSources: viewSources
      .slice(0, 5)
      .map((s, i) => ({ ...s, top: `${[40, 80, 120, 160, 200][i] - 16}px` })),
    flC: Object.fromEntries(
      Array.from({ length: 4 }, (_, i) => [
        `c${i}`,
        {
          s:
            viewClients[i]?.dot === "var(--warn)"
              ? "var(--warn)"
              : "var(--line)",
          d: "2 4",
          a: "none",
        },
      ]),
    ),
    flS: Object.fromEntries(
      Array.from({ length: 5 }, (_, i) => [
        `s${i}`,
        {
          s: viewSources[i]?.on && viewSources[i]?.dot === "var(--ok)" ? "var(--accent2)" : "var(--line)",
          d: "3 5",
          a: viewSources[i]?.on && viewSources[i]?.dot === "var(--ok)" ? "covedash 1s linear infinite" : "none",
        },
      ]),
    ),
    alerts: [
      ...viewSources
        .filter(
          (s) =>
            s.enabled &&
            ["needs_reauth", "rejected", "logged_out"].includes(s.auth_status),
        )
        .map((s) => ({
          zh: s.name,
          en: s.name,
          subZh: text(s.auth_status),
          subEn: text(s.auth_status, true),
          c: "var(--err)",
          onClick: () => go("来源"),
        })),
      ...p.requests
        .filter((r) => ["unverified", "interrupted"].includes(r.status))
        .slice(0, 3)
        .map((r) => ({
          zh: text(r.status),
          en: text(r.status, true),
          subZh: r.id,
          subEn: r.id,
          c: "var(--warn)",
          onClick: () => p.onDetail(r.id),
        })),
    ],
    modelGroups: groups,
    modelSrcs: [
      {
        n: "all",
        all: true,
        notAll: false,
        ...seg(sourceFilter === "all"),
        onClick: () => setSourceFilter("all"),
      },
      ...p.sources.map((s) => ({
        n: s.name,
        all: false,
        notAll: true,
        ...logo(s.provider),
        ...seg(sourceFilter === s.id),
        onClick: () => setSourceFilter(s.id),
      })),
    ],
    mq,
    onMq: (e: React.ChangeEvent<HTMLInputElement>) => setMq(e.target.value),
    noModels: groups.length === 0,
    msorts: [
      ["default", "默认", "Default"],
      ["price", "价格", "Price"],
      ["ctx", "上下文", "Context"],
    ].map(([id, zh, en]) => ({
      zh,
      en,
      ...seg(sort === id),
      onClick: () => setSort(id),
    })),
    routes: viewRoutes,
    aliases: p.aliases.map((a) => ({
      pub: a.public_model,
      target: p.routes.find((r) => r.id === a.route_id)?.name || a.route_id,
      n: String(p.routes.find((r) => r.id === a.route_id)?.members.length ?? 0),
    })),
    schedulingUnavailable:
      !p.settings.version || typeof p.settings.limits?.allow_paid_fallback !== "boolean" ||
      p.busy || p.pending.includes("subscription-scheduling"),
    schedulingReason: "订阅优先适用于路由；固定来源 Key 保持固定。仅使用当前有效额度观测，等于阈值也跳过；未知额度不视为耗尽。",
    xPaid: p.settings.limits?.allow_paid_fallback === true,
    xPaidBg: p.settings.limits?.allow_paid_fallback ? "var(--accent)" : "var(--line)",
    xPaidX: p.settings.limits?.allow_paid_fallback ? "16px" : "2px",
    xTogglePaid: () => mutate("settings", "PATCH", {
      version: p.settings.version,
      changes: { allow_paid_fallback: !p.settings.limits.allow_paid_fallback },
    }, "subscription-scheduling"),
    xThr: [5, 10, 20].map((n) => ({
      k: `${n}%`,
      active: p.settings.limits?.subscription_quota_threshold === n,
      ...seg(p.settings.limits?.subscription_quota_threshold === n),
      onClick: () => mutate("settings", "PATCH", {
        version: p.settings.version,
        changes: { subscription_quota_threshold: n },
      }, "subscription-scheduling"),
    })),
    xNext: (() => {
      const chosen = nextPreview?.selected_candidate;
      const source = p.sources.find((s) => s.id === chosen?.source_id);
      return {
        ...logo(source?.provider || "openai"),
        name: chosen ? `${source?.name || chosen.source_id} · ${chosen.upstream_model}` : "—",
        zh: !codingAlias ? "请先创建 coding 公开模型名" : !nextPreview ? "正在读取预览…" :
          chosen ? "配置预览；实际选择取决于请求内容与 Key 权限" : nextPreview.error || "没有可用候选",
        en: !codingAlias ? "Create a public model named coding first" : !nextPreview ? "Loading preview…" :
          chosen ? "Configuration preview; actual selection depends on the request and Key permissions" : nextPreview.error || "No available candidate",
        c: chosen ? "var(--ok)" : nextPreview?.error ? "var(--warn)" : "var(--ink3)",
      };
    })(),
    keys: p.keys.map((k) => ({
      ...k,
      fp: k.fingerprint,
      ...keyDisplay(k, keyClock),
      target:
        p.routes.find((r) => r.id === k.route_id)?.name ||
        p.sources.find((s) => s.id === k.source_id)?.name ||
        k.route_id ||
        k.source_id,
      protos:
        k.protocol_allowlist == null
          ? "all protocols"
          : k.protocol_allowlist.length
            ? k.protocol_allowlist.join(" · ")
            : "no protocols",
      models:
        k.model_allowlist == null
          ? "all models"
          : k.model_allowlist.length
            ? k.model_allowlist.join(" · ")
            : "no models",
      limZh: k.limits?.rpm ? `${k.limits.rpm} RPM` : "未设 RPM 限制",
      limEn: k.limits?.rpm ? `${k.limits.rpm} RPM` : "No RPM limit",
      expZh: k.expires_at ? `到期 ${date(k.expires_at)}` : "不过期",
      expEn: k.expires_at ? `Expires ${date(k.expires_at)}` : "No expiry",
      lastZh: k.last_seen_at ? date(k.last_seen_at) : "尚未使用",
      lastEn: k.last_seen_at ? date(k.last_seen_at) : "Never used",

      busy: p.busy || p.pending.includes(k.id),
      onManage: () => manage("API Keys", k.id),
      onRevoke: () => mutate(`client-keys/${k.id}`, "DELETE", { version: k.version }, k.id),
    })),
    reqs: p.requests.map((r) => ({
      ...requestRow(r),
      onClick: () => p.onDetail(r.id),
    })),
    hasMoreRequests: p.hasMoreRequests,
    moreRequestsBusy: p.busy || p.pending.includes("request-pagination"),
    moreRequests: p.onMoreRequests,
    reqFilters: [
      ["", "全部", "All"],
      ["failed", "失败", "Failed"],
      ["unverified", "未知", "Unknown"],
      ["streaming", "流式中", "Streaming"],
    ].map(([id, zh, en]) => ({
      zh,
      en,
      n: id ? number(p.usage.states?.[id]) : number(p.usage.requests),
      ...seg(p.filter === id),
      onClick: () => p.onFilter(id),
    })),
    reqStats: [
      { zh: "请求 · 1 小时", en: "Requests · 1h", v: number(hour.requests) },
      {
        zh: "错误率 · 1 小时",
        en: "Error rate · 1h",
        v: hour.requests
          ? `${(((hour.states?.failed ?? 0) / hour.requests) * 100).toFixed(1)}%`
          : hour.requests === 0
            ? "0%"
            : "—",
      },
      {
        zh: "TTFT 平均 · 1 小时",
        en: "TTFT mean · 1h",
        v: hour.ttft_known_requests
          ? `${(hour.mean_ttft_ms / 1000).toFixed(2)}s`
          : "—",
      },
      {
        zh: "平均耗时 · 1 小时",
        en: "Latency mean · 1h",
        v: hour.requests
          ? `${(hour.mean_duration_ms / 1000).toFixed(2)}s`
          : "—",
      },
    ].map((m) => ({ ...m, c: "var(--ink)", sc: "var(--line)", spark: [] })),
    bars,
    barColumns: `repeat(${Math.max(1, bars.length)},minmax(0,1fr))`,
    ranges: ["24h", "7d", "14d", "30d"].map((l) => ({
      l,
      ...seg(range === l),
      onClick: () => setRange(l),
    })),
    keyUse,
    budgets: budgets.map((b) => ({
      zh: b.name,
      en: b.name,
      modeZh: b.mode === "strict" ? "严格" : "软限制",
      modeEn: b.mode,
      limit: `${b.currency} ${b.amount_limit}`,
      settled: `${b.currency} ${b.settled}`,
      reserved: b.reserved,
      pending: b.pending,
      w: Number(b.amount_limit)
        ? `${Math.min(100, (Number(b.settled) / Number(b.amount_limit)) * 100)}%`
        : "0%",
    })),
    quota: p.sources.flatMap((s) =>
      (s.quota?.windows?.length ? s.quota.windows : [{}]).map((w: any) => ({
        acct: s.name,
        winZh: w.limit_name || w.dimension || "",
        winEn: w.limit_name || w.dimension || "",
        has: s.quota?.status === "available" && w.used_percent != null,
        noPct: s.quota?.status !== "available" || w.used_percent == null,
        valZh:
          s.quota?.status === "available" && w.used_percent != null
            ? `${w.used_percent}%`
            : text(s.quota?.status || "unknown"),
        valEn:
          s.quota?.status === "available" && w.used_percent != null
            ? `${w.used_percent}%`
            : text(s.quota?.status || "unknown", true),
        w:
          s.quota?.status === "available" && w.used_percent != null
            ? `${w.used_percent}%`
            : "0%",
        subZh: w.reset_at ? `重置 ${date(w.reset_at)}` : "暂无可靠观测",
        subEn: w.reset_at
          ? `Resets ${date(w.reset_at)}`
          : "No reliable observation",
      })),
    ),
    dataDir: p.status.data_dir || "—",
    timeouts: `${p.settings.limits?.header_timeout_seconds ?? "—"}s · ${p.settings.limits?.idle_timeout_seconds ?? "—"}s · ${p.settings.limits?.total_timeout_seconds ?? "—"}s`,
    retentionDays: String(p.settings.retention_days ?? 7),
    onRetention: () => manage("设置"),
    versionLabel: `Cove ${p.status.version || "—"}`,
    managePage: () => manage(),
    manageSettings: () => manage("设置"),
    manageOperations: () => manage("运维"),
    openBackup: () => manage("运维"),
    openKey: () => manage("API Keys"),
    openAdd: () => setAdding(true),
    mAdd: adding,
    addGrid: true,
    customAdd: () => {
      setAdding(false);
      p.onAdd();
    },
    presets: [
      ["codex", "ChatGPT 订阅", "OAuth 登录", "Sign in"],
      ["openai", "OpenAI", "Responses", "Responses"],
      ["anthropic", "Anthropic", "Messages", "Messages"],
      ["deepseek", "DeepSeek", "Chat", "Chat"],
      ["gemini", "Gemini", "Gemini", "Gemini"],
      ["ollama", "Ollama", "本地，无需 Key", "Local, no key"],
    ].map(([id, name, subZh, subEn]) => ({
      ...logo(id === "codex" ? "openai" : id),
      zh: name,
      en: name,
      subZh,
      subEn,
      onClick: () => {
        setAdding(false);
        p.onAdd(id);
      },
    })),
    stop: (e: React.MouseEvent) => e.stopPropagation(),
    close: () => {
      setPicker(null);
      setAdding(false);
      p.onCloseDetail();
    },
    xPal: palette,
    xPalQ: paletteQuery,
    xOpenPal: () => {
      setPalette(true);
      setPaletteQuery("");
      setPaletteIndex(0);
    },
    xClosePal: () => setPalette(false),
    xPalRef: (el: HTMLInputElement | null) => {
      el?.focus();
    },
    xOnPalQ: (e: React.ChangeEvent<HTMLInputElement>) => {
      setPaletteQuery(e.target.value);
      setPaletteIndex(0);
    },
    xOnPalKey: (e: React.KeyboardEvent) => {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        setPaletteIndex((i) =>
          Math.max(
            0,
            Math.min(commands.length - 1, i + (e.key === "ArrowDown" ? 1 : -1)),
          ),
        );
      } else if (e.key === "Enter") {
        e.preventDefault();
        commands[paletteIndex]?.run();
      }
    },
    xCmds: commands.map((c, i) => ({
      ...c,
      bg: i === paletteIndex ? "var(--hover)" : "transparent",
    })),
    xNoCmds: !commands.length,
    mPicker: !!picker,
    picker: {
      ...logo(picker?.kind || "openai"),
      name: picker?.name || "",
      cur: selectedPickerModel || "—",
    },
    pq,
    onPq: (e: React.ChangeEvent<HTMLInputElement>) => setPq(e.target.value),
    hasPAliases: p.aliases.some((a) =>
      a.public_model.toLowerCase().includes(pq.toLowerCase()),
    ),
    pAliases: p.aliases
      .filter((a) => a.public_model.toLowerCase().includes(pq.toLowerCase()))
      .map((a) => ({
        pub: a.public_model,
        target: a.route_id,
        sel: selectedPickerModel === a.public_model,
        mark: selectedPickerModel === a.public_model ? "check" : "",
        onClick: () => pickModel(a.public_model),
      })),
    pGroups: p.sources
      .map((s) => ({
        ...logo(s.provider),
        name: s.name,
        items: models
          .filter(
            (m) =>
              m.enabled &&
              m.source_id === s.id &&
              m.upstream_model.toLowerCase().includes(pq.toLowerCase()),
          )
          .map((m) => ({
            id: m.id,
            m: m.upstream_model,
            ctx: compact(m.context_limit),
            sel: selectedPickerModel === m.upstream_model,
            mark: selectedPickerModel === m.upstream_model ? "check" : "",
            onClick: () => pickModel(m.upstream_model),
          })),
      }))
      .filter((g) => g.items.length),
    mReq: !!request,
    reqD: {
      ...requestRow(request ?? {}),
      hasAtt: attempts.length > 0,
      noAtt: !attempts.length,
      whyZh: request?.error_summary || "暂无尝试记录",
      whyEn: request?.error_summary || "No attempts recorded",
      attempts: attempts.map((a: any, i: number) => ({
        ...logo(
          p.sources.find((s) => s.id === a.source_id)?.provider || "openai",
        ),
        src: a.source_name,
        m: a.sent_model,
        dur:
          a.duration_ms == null ? "—" : `${(a.duration_ms / 1000).toFixed(2)}s`,
        dot: dots[a.status] || "var(--ink3)",
        c: dots[a.status] || "var(--ink2)",
        lineVis: i === attempts.length - 1 ? "hidden" : "visible",
        res: a.status,
        zh: a.error_summary || "",
        en: a.error_summary || "",
        usage: `${number(a.usage?.input_tokens)} / ${number(a.usage?.output_tokens)}`,
      })),
    },
    requestExtra: request && (
      <div className="request-extra">
        {["streaming", "dispatching"].includes(request.status) && (
          <button
            disabled={p.busy || p.pending.includes(request.id)}
            onClick={() =>
              p.run(
                async () => {
                  await p.api(`requests/${request.id}/cancel`, "POST", {
                    version: request.version,
                  });
                  await p.onRefresh();
                  await p.onReconcile();
                },
                request.id,
              )
            }
          >
            取消请求
          </button>
        )}
        {p.detail.accounting && (
          <RequestAccounting
            api={p.api}
            requestId={request.id}
            accounting={p.detail.accounting}
            onReconciled={p.onReconcile}
          />
        )}
        <details>
          <summary>技术详情</summary>
          <pre>{JSON.stringify(p.detail, null, 2)}</pre>
        </details>
      </div>
    ),
    emptyStates: {
      "01": !pool.length
        ? empty(
            "尚无订阅账号，可在来源页添加。",
            "No subscription accounts. Add a source to begin.",
          )
        : null,
      "02": !viewSources.length ? (
        <div className="v13-empty">
          <span data-l="zh">连接你的第一个模型来源</span>
          <span data-l="en">Connect your first model source</span>
          <button onClick={() => setAdding(true)}>
            <span data-l="zh">添加来源</span>
            <span data-l="en">Add source</span>
          </button>
        </div>
      ) : null,
      "04": !viewRoutes.length
        ? empty(
            "尚无路由，打开管理创建路由与公开模型名。",
            "No routes. Open management to create a route and public name.",
          )
        : null,
      "05": !p.keys.length
        ? empty(
            "尚无 API Key，点击创建 Key 接入客户端。",
            "No API keys. Create a key to connect a client.",
          )
        : null,
      "06": !p.requests.length
        ? empty("尚无匹配的请求。", "No matching requests.")
        : null,
    },
  };
  return <V13View v={v} />;
}
