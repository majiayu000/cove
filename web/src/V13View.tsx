// Layout faithfully transcribed from the supplied Cove Console v13.dc.html.
import React from "react";

export function V13View({ v }: { v: Record<string, any> }) {
  return (
    <>
      <div
        style={{
          position: "fixed",
          top: "0",
          left: "0",
          right: "0",
          zIndex: "20",
          height: "60px",
          display: "grid",
          gridTemplateColumns: "1fr auto 1fr",
          alignItems: "center",
          gap: "16px",
          padding: "0 20px",
          background: "var(--bar)",
          backdropFilter: "saturate(1.8) blur(24px)",
          WebkitBackdropFilter: "saturate(1.8) blur(24px)",
          borderBottom: "1px solid var(--line2)",
        }}
        className="v13-topbar"
      >
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: "10px",
            minWidth: "0",
          }}
        >
          <span
            style={{
              width: "26px",
              height: "26px",
              borderRadius: "8px",
              background:
                "linear-gradient(135deg,var(--accent),var(--accent2))",
              color: "var(--accentInk)",
              boxShadow:
                "0 0 20px -2px color-mix(in oklch, var(--accent) 70%, transparent),inset 0 1px 0 oklch(1 0 0 / 0.35)",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
            }}
          >
            <span
              data-i=""
              style={{
                fontSize: "15px",
                fontVariationSettings: "'FILL' 1,'wght' 500",
              }}
            >
              {"sailing"}
            </span>
          </span>
          <span
            style={{
              fontSize: "15px",
              fontWeight: "600",
              letterSpacing: "-0.02em",
            }}
          >
            {"Cove"}
          </span>
          <span
            style={{
              display: "inline-flex",
              alignItems: "center",
              gap: "6px",
              fontSize: "12px",
              color: "var(--ink2)",
              whiteSpace: "nowrap",
            }}
          >
            <span
              style={{
                width: "6px",
                height: "6px",
                borderRadius: "50%",
                background: "var(--ok)",
                boxShadow: "0 0 10px var(--ok)",
              }}
            ></span>
            {v.listen}
          </span>
        </div>
        <nav
          style={{
            display: "flex",
            gap: "2px",
            padding: "3px",
            borderRadius: "12px",
            background: "var(--sunk)",
            boxShadow: "inset 0 0 0 1px var(--line2)",
          }}
        >
          {v.tabs.map((t: any, i1: number) => (
            <React.Fragment key={t.id ?? t.name ?? i1}>
              <button
                onClick={t.onClick}
                style={{
                  height: "30px",
                  padding: "0 14px",
                  border: "0",
                  borderRadius: "9px",
                  transition: "all .2s",
                  background: t.bg,
                  color: t.fg,
                  boxShadow: t.sh,
                  fontSize: "13px",
                  fontWeight: "500",
                  whiteSpace: "nowrap",
                  cursor: "pointer",
                }}
                type="button"
              >
                <span data-l="zh">{t.zh}</span>
                <span data-l="en">{t.en}</span>
              </button>
            </React.Fragment>
          ))}
        </nav>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "flex-end",
            gap: "2px",
          }}
        >
          <button
            onClick={v.xOpenPal}
            title="⌘K"
            style={{
              display: "inline-flex",
              alignItems: "center",
              gap: "6px",
              height: "32px",
              padding: "0 8px",
              border: "0",
              borderRadius: "7px",
              background: "transparent",
              cursor: "pointer",
            }}
            className="v13-hover"
            type="button"
          >
            <span data-i="" style={{ fontSize: "19px", color: "var(--ink2)" }}>
              {"search"}
            </span>
            <span
              style={{
                fontFamily: "var(--mono)",
                fontSize: "10.5px",
                color: "var(--ink3)",
              }}
            >
              {"⌘K"}
            </span>
          </button>
          <button
            onClick={v.toggleMode}
            title="theme"
            style={{
              width: "32px",
              height: "32px",
              border: "0",
              borderRadius: "7px",
              background: "transparent",
              cursor: "pointer",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
            }}
            className="v13-hover"
            type="button"
          >
            <span data-i="" style={{ fontSize: "19px", color: "var(--ink2)" }}>
              {v.modeIcon}
            </span>
          </button>
          <button
            onClick={v.toggleLang}
            title="language"
            style={{
              minWidth: "32px",
              height: "32px",
              padding: "0 6px",
              border: "0",
              borderRadius: "7px",
              background: "transparent",
              fontFamily: "var(--mono)",
              fontSize: "10.5px",
              fontWeight: "500",
              color: "var(--ink3)",
              letterSpacing: "0.14em",
              textTransform: "uppercase",
              cursor: "pointer",
            }}
            className="v13-hover"
            type="button"
          >
            {v.langLabel}
          </button>
          <button
            onClick={v.goSettings}
            title="settings"
            style={{
              width: "32px",
              height: "32px",
              border: "0",
              borderRadius: "7px",
              background: v.setBg,
              cursor: "pointer",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
            }}
            className="v13-hover"
            type="button"
          >
            <span data-i="" style={{ fontSize: "19px", color: "var(--ink2)" }}>
              {"settings"}
            </span>
          </button>
        </div>
      </div>
      {v.pg.overview && (
        <>
          <main
            data-screen-label="01 Overview"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "860px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "28px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 01"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"概览"}</span>
                    <span data-l="en">{"Overview"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">{"网关状态、订阅额度与客户端接入"}</span>
                    <span data-l="en">
                      {"Gateway status, subscription quota and client setup"}
                    </span>
                  </p>
                </div>
                <button
                  className="icon-button"
                  title="管理 / Manage"
                  aria-label="管理当前页面"
                  onClick={v.managePage}
                  type="button"
                >
                  <span data-i="">{"tune"}</span>
                </button>
              </header>
              <section
                style={{
                  background: "var(--glass)",
                  border: "1px solid var(--glassBd)",
                  borderRadius: "18px",
                  boxShadow: "var(--sh)",
                  backdropFilter: "blur(22px) saturate(1.5)",
                  WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                  overflow: "hidden",
                }}
              >
                <div
                  style={{
                    position: "relative",
                    height: "240px",
                    margin: "10px 10px 0",
                    borderRadius: "12px",
                    overflow: "hidden",
                    background:
                      "radial-gradient(circle at 50% 50%,color-mix(in oklch, var(--accent) 16%, transparent),transparent 55%),linear-gradient(var(--gridc) 1px,transparent 1px) 0 0/20px 20px,linear-gradient(90deg,var(--gridc) 1px,transparent 1px) 0 0/20px 20px,var(--sunk)",
                    boxShadow: "inset 0 0 0 1px var(--line2)",
                  }}
                >
                  <svg
                    viewBox="0 0 100 240"
                    preserveAspectRatio="none"
                    style={{
                      position: "absolute",
                      inset: "0",
                      width: "100%",
                      height: "100%",
                      overflow: "visible",
                      filter:
                        "drop-shadow(0 0 3px color-mix(in oklch, var(--accent) 70%, transparent))",
                    }}
                  >
                    <path
                      d="M28,45 C34,45 34,120 40,120"
                      fill="none"
                      stroke={v.flC.c0.s}
                      strokeWidth="2"
                      strokeDasharray={v.flC.c0.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flC.c0.a }}
                    ></path>
                    <path
                      d="M28,95 C34,95 34,120 40,120"
                      fill="none"
                      stroke={v.flC.c1.s}
                      strokeWidth="2"
                      strokeDasharray={v.flC.c1.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flC.c1.a }}
                    ></path>
                    <path
                      d="M28,145 C34,145 34,120 40,120"
                      fill="none"
                      stroke={v.flC.c2.s}
                      strokeWidth="2"
                      strokeDasharray={v.flC.c2.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flC.c2.a }}
                    ></path>
                    <path
                      d="M28,195 C34,195 34,120 40,120"
                      fill="none"
                      stroke={v.flC.c3.s}
                      strokeWidth="2"
                      strokeDasharray={v.flC.c3.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flC.c3.a }}
                    ></path>
                    <path
                      d="M60,120 C66,120 66,40 72,40"
                      fill="none"
                      stroke={v.flS.s0.s}
                      strokeWidth="2"
                      strokeDasharray={v.flS.s0.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flS.s0.a }}
                    ></path>
                    <path
                      d="M60,120 C66,120 66,80 72,80"
                      fill="none"
                      stroke={v.flS.s1.s}
                      strokeWidth="2"
                      strokeDasharray={v.flS.s1.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flS.s1.a }}
                    ></path>
                    <path
                      d="M60,120 C66,120 66,120 72,120"
                      fill="none"
                      stroke={v.flS.s2.s}
                      strokeWidth="2"
                      strokeDasharray={v.flS.s2.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flS.s2.a }}
                    ></path>
                    <path
                      d="M60,120 C66,120 66,160 72,160"
                      fill="none"
                      stroke={v.flS.s3.s}
                      strokeWidth="2"
                      strokeDasharray={v.flS.s3.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flS.s3.a }}
                    ></path>
                    <path
                      d="M60,120 C66,120 66,200 72,200"
                      fill="none"
                      stroke={v.flS.s4.s}
                      strokeWidth="2"
                      strokeDasharray={v.flS.s4.d}
                      vectorEffect="non-scaling-stroke"
                      style={{ animation: v.flS.s4.a }}
                    ></path>
                  </svg>
                  {v.flowClients.map((n: any, i2: number) => (
                    <React.Fragment key={n.id ?? n.name ?? i2}>
                      <div
                        style={{
                          position: "absolute",
                          left: "8px",
                          width: "calc(28% - 8px)",
                          top: n.top,
                          height: "36px",
                          display: "flex",
                          alignItems: "center",
                          gap: "8px",
                          padding: "0 10px 0 4px",
                          borderRadius: "10px",
                          background: "var(--panel)",
                          boxShadow: "var(--tileSh)",
                          opacity: n.dim,
                        }}
                      >
                        <span
                          role="img"
                          data-mono={n.mono}
                          style={{
                            display: "inline-block",
                            flex: "none",
                            width: "18px",
                            height: "18px",
                            marginLeft: "5px",
                            background:
                              "url(" + n.logo + ") center/contain no-repeat",
                          }}
                        ></span>
                        <span
                          style={{
                            flex: "1",
                            minWidth: "0",
                            display: "flex",
                            flexDirection: "column",
                            lineHeight: "1.2",
                          }}
                        >
                          <span
                            style={{
                              fontSize: "12px",
                              fontWeight: "600",
                              whiteSpace: "nowrap",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                            }}
                          >
                            {n.name}
                          </span>
                          <span
                            style={{
                              fontSize: "10.5px",
                              fontFamily: "var(--mono)",
                              color: "var(--ink3)",
                              whiteSpace: "nowrap",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                            }}
                          >
                            {n.val}
                          </span>
                        </span>
                      </div>
                    </React.Fragment>
                  ))}
                  <div
                    style={{
                      position: "absolute",
                      left: "calc(40% - 10px)",
                      width: "calc(20% + 20px)",
                      top: "74px",
                      height: "92px",
                      borderRadius: "22px",
                      background:
                        "linear-gradient(135deg,var(--accent),var(--accent2))",
                      filter: "blur(18px)",
                      animation: "coveglow 3.2s ease-in-out infinite",
                    }}
                  ></div>
                  <div
                    style={{
                      position: "absolute",
                      left: "40%",
                      width: "20%",
                      top: "84px",
                      height: "72px",
                      overflow: "hidden",
                      border: "1px solid oklch(1 0 0 / 0.14)",
                      display: "flex",
                      flexDirection: "column",
                      alignItems: "center",
                      justifyContent: "center",
                      gap: "4px",
                      borderRadius: "14px",
                      background:
                        "linear-gradient(160deg,oklch(0.26 0.05 275),oklch(0.14 0.03 268))",
                      color: "oklch(0.97 0 0)",
                      boxShadow:
                        "0 12px 32px -8px color-mix(in oklch, var(--accent) 60%, transparent),inset 0 1px 0 oklch(1 0 0 / 0.18)",
                    }}
                  >
                    <span
                      style={{
                        position: "absolute",
                        top: "0",
                        bottom: "0",
                        left: "-60%",
                        width: "45%",
                        background:
                          "linear-gradient(90deg,transparent,oklch(1 0 0 / 0.14),transparent)",
                        transform: "translateX(0)",
                        animation: "covesweep 3.6s ease-in-out infinite",
                      }}
                    ></span>
                    <span
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "6px",
                        fontSize: "14px",
                        fontWeight: "600",
                      }}
                    >
                      <span
                        data-i=""
                        style={{
                          fontSize: "18px",
                          fontVariationSettings: "'FILL' 1,'wght' 500",
                        }}
                      >
                        {"sailing"}
                      </span>
                      {"Cove"}
                    </span>
                    <span
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "6px",
                        fontSize: "11px",
                        fontFamily: "var(--mono)",
                        color: "oklch(0.82 0.01 265)",
                      }}
                    >
                      <span
                        style={{
                          width: "6px",
                          height: "6px",
                          borderRadius: "50%",
                          background: "var(--ok)",
                          boxShadow: "0 0 10px var(--ok)",
                          animation: "covepulse 1.6s ease-out infinite",
                        }}
                      ></span>
                      {v.liveLabel}
                    </span>
                  </div>
                  {v.flowSources.map((n: any, i3: number) => (
                    <React.Fragment key={n.id ?? n.name ?? i3}>
                      <div
                        style={{
                          position: "absolute",
                          right: "8px",
                          width: "calc(28% - 8px)",
                          top: n.top,
                          height: "32px",
                          display: "flex",
                          alignItems: "center",
                          gap: "8px",
                          padding: "0 10px 0 4px",
                          borderRadius: "10px",
                          background: "var(--panel)",
                          boxShadow: "var(--tileSh)",
                          opacity: n.dim,
                        }}
                      >
                        <span
                          role="img"
                          data-mono={n.mono}
                          style={{
                            display: "inline-block",
                            flex: "none",
                            width: "16px",
                            height: "16px",
                            marginLeft: "6px",
                            background:
                              "url(" + n.logo + ") center/contain no-repeat",
                          }}
                        ></span>
                        <span
                          style={{
                            flex: "1",
                            minWidth: "0",
                            fontSize: "12px",
                            fontWeight: "500",
                            whiteSpace: "nowrap",
                            overflow: "hidden",
                            textOverflow: "ellipsis",
                          }}
                        >
                          {n.name}
                        </span>
                        <span
                          style={{
                            flex: "none",
                            width: "6px",
                            height: "6px",
                            borderRadius: "50%",
                            background: n.dot,
                            boxShadow: "0 0 10px " + n.dot + "",
                          }}
                        ></span>
                      </div>
                    </React.Fragment>
                  ))}
                </div>
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "14px",
                    flexWrap: "wrap",
                    padding: "16px 18px",
                  }}
                >
                  <span
                    style={{
                      width: "8px",
                      height: "8px",
                      borderRadius: "50%",
                      background: v.gatewayDot,
                      boxShadow:
                        `0 0 0 4px color-mix(in oklch, ${v.gatewayDot} 18%, transparent)`,
                    }}
                  ></span>
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      flex: "1",
                      minWidth: "200px",
                      lineHeight: "1.4",
                    }}
                  >
                    <span style={{ fontSize: "14px", fontWeight: "600" }}>
                      <span data-l="zh">{v.gatewayZh}</span>
                      <span data-l="en">{v.gatewayEn}</span>
                    </span>
                    <span style={{ fontSize: "12px", color: "var(--ink2)" }}>
                      <span data-l="zh">{v.gatewaySubZh}</span>
                      <span data-l="en">{v.gatewaySubEn}</span>
                    </span>
                  </div>
                  <button
                    onClick={v.copyBase}
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      height: "30px",
                      padding: "0 8px 0 10px",
                      border: "1px solid var(--line)",
                      borderRadius: "7px",
                      background: "var(--panel)",
                      fontFamily: "var(--mono)",
                      fontSize: "12px",
                      cursor: "pointer",
                    }}
                    className="v13-hover"
                    type="button"
                  >
                    {v.baseURL}
                    <span
                      data-i=""
                      style={{ fontSize: "15px", color: "var(--ink3)" }}
                    >
                      {v.baseIcon}
                    </span>
                  </button>
                </div>
                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns: "repeat(4,minmax(0,1fr))",
                    borderTop: "1px solid var(--line2)",
                  }}
                >
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      gap: "2px",
                      padding: "14px 18px",
                    }}
                  >
                    <span style={{ fontSize: "12px", color: "var(--ink2)" }}>
                      <span data-l="zh">{"今日请求"}</span>
                      <span data-l="en">{"Requests today"}</span>
                    </span>
                    <span
                      style={{
                        fontSize: "30px",
                        fontWeight: "600",
                        letterSpacing: "-0.045em",
                        lineHeight: "1.15",
                        fontVariantNumeric: "tabular-nums",
                        background:
                          "linear-gradient(180deg,var(--ink) 30%,color-mix(in oklch, var(--ink) 50%, var(--accent)))",
                        WebkitBackgroundClip: "text",
                        backgroundClip: "text",
                        color: "transparent",
                      }}
                    >
                      {v.requestCount}
                    </span>
                  </div>
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      gap: "2px",
                      padding: "14px 18px",
                      borderLeft: "1px solid var(--line2)",
                    }}
                  >
                    <span style={{ fontSize: "12px", color: "var(--ink2)" }}>
                      <span data-l="zh">{"已知 tokens"}</span>
                      <span data-l="en">{"Known tokens"}</span>
                    </span>
                    <span
                      style={{
                        fontSize: "30px",
                        fontWeight: "600",
                        letterSpacing: "-0.045em",
                        lineHeight: "1.15",
                        fontVariantNumeric: "tabular-nums",
                        background:
                          "linear-gradient(180deg,var(--ink) 30%,color-mix(in oklch, var(--ink) 50%, var(--accent)))",
                        WebkitBackgroundClip: "text",
                        backgroundClip: "text",
                        color: "transparent",
                      }}
                    >
                      {v.tokenCount}
                    </span>
                  </div>
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      gap: "2px",
                      padding: "14px 18px",
                      borderLeft: "1px solid var(--line2)",
                    }}
                  >
                    <span style={{ fontSize: "12px", color: "var(--ink2)" }}>
                      <span data-l="zh">{"估算费用"}</span>
                      <span data-l="en">{"Est. cost"}</span>
                    </span>
                    <span
                      style={{
                        fontSize: "30px",
                        fontWeight: "600",
                        letterSpacing: "-0.045em",
                        lineHeight: "1.15",
                        fontVariantNumeric: "tabular-nums",
                        background:
                          "linear-gradient(180deg,var(--ink) 30%,color-mix(in oklch, var(--ink) 50%, var(--accent)))",
                        WebkitBackgroundClip: "text",
                        backgroundClip: "text",
                        color: "transparent",
                      }}
                    >
                      {v.cost}
                    </span>
                  </div>
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      gap: "2px",
                      padding: "14px 18px",
                      borderLeft: "1px solid var(--line2)",
                    }}
                  >
                    <span style={{ fontSize: "12px", color: "var(--ink2)" }}>
                      <span data-l="zh">{"并发"}</span>
                      <span data-l="en">{"Concurrency"}</span>
                    </span>
                    <span
                      style={{
                        fontSize: "30px",
                        fontWeight: "600",
                        letterSpacing: "-0.045em",
                        lineHeight: "1.15",
                        fontVariantNumeric: "tabular-nums",
                        background:
                          "linear-gradient(180deg,var(--ink) 30%,color-mix(in oklch, var(--ink) 50%, var(--accent)))",
                        WebkitBackgroundClip: "text",
                        backgroundClip: "text",
                        color: "transparent",
                      }}
                    >
                      {v.activeRequests}
                      <span style={{ color: "var(--ink3)", fontWeight: "400" }}>
                        {" / "}
                        {v.maxConcurrent}
                      </span>
                    </span>
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    padding: "0 4px",
                  }}
                >
                  <span
                    style={{
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      fontWeight: "500",
                      color: "var(--ink3)",
                      letterSpacing: "0.14em",
                      textTransform: "uppercase",
                    }}
                  >
                    <span data-l="zh">{"订阅账号"}</span>
                    <span data-l="en">{"Subscription accounts"}</span>
                  </span>
                  <button
                    onClick={v.goSources}
                    style={{
                      border: "0",
                      background: "transparent",
                      fontSize: "12px",
                      color: "var(--accent)",
                      cursor: "pointer",
                    }}
                    type="button"
                  >
                    <span data-l="zh">{"管理 →"}</span>
                    <span data-l="en">{"Manage →"}</span>
                  </button>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    {v.xPool.map((a: any, i4: number) => (
                      <React.Fragment key={a.id ?? a.name ?? i4}>
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "12px",
                            minHeight: "60px",
                            padding: "10px 12px 10px 16px",
                            borderTop: "1px solid var(--line2)",
                          }}
                          className="v13-hover"
                        >
                          <span
                            style={{
                              flex: "none",
                              width: "36px",
                              height: "36px",
                              borderRadius: "9px",
                              background: "var(--tile)",
                              boxShadow: "var(--tileSh)",
                              display: "flex",
                              alignItems: "center",
                              justifyContent: "center",
                              opacity: a.dim,
                            }}
                          >
                            <span
                              role="img"
                              data-mono={a.mono}
                              style={{
                                display: "inline-block",
                                width: "20px",
                                height: "20px",
                                background:
                                  "url(" +
                                  a.logo +
                                  ") center/contain no-repeat",
                              }}
                            ></span>
                          </span>
                          <div
                            style={{
                              flex: "1",
                              display: "flex",
                              flexDirection: "column",
                              minWidth: "0",
                              lineHeight: "1.4",
                              opacity: a.dim,
                            }}
                          >
                            <span
                              style={{ fontSize: "14px", fontWeight: "500" }}
                            >
                              {a.plan}
                            </span>
                            <span
                              style={{
                                fontSize: "12px",
                                color: "var(--ink2)",
                                overflow: "hidden",
                                textOverflow: "ellipsis",
                                whiteSpace: "nowrap",
                              }}
                            >
                              {a.acct}
                            </span>
                          </div>
                          <div
                            style={{
                              display: "flex",
                              flexDirection: "column",
                              gap: "5px",
                              width: "230px",
                              opacity: a.dim,
                            }}
                          >
                            {a.q.map((b: any, i5: number) => (
                              <React.Fragment key={b.id ?? b.name ?? i5}>
                                <div
                                  style={{
                                    display: "flex",
                                    alignItems: "center",
                                    gap: "8px",
                                    fontSize: "11px",
                                    color: "var(--ink2)",
                                  }}
                                >
                                  <span
                                    style={{
                                      width: "30px",
                                      whiteSpace: "nowrap",
                                    }}
                                  >
                                    <span data-l="zh">{b.zh}</span>
                                    <span data-l="en">{b.en}</span>
                                  </span>
                                  <span
                                    style={{
                                      flex: "1",
                                      height: "4px",
                                      borderRadius: "99px",
                                      background: "var(--sunk)",
                                      overflow: "hidden",
                                    }}
                                  >
                                    <span
                                      style={{
                                        display: "block",
                                        width: b.w,
                                        height: "100%",
                                        background: b.fill,
                                      }}
                                    ></span>
                                  </span>
                                  <span
                                    style={{
                                      width: "32px",
                                      textAlign: "right",
                                      fontVariantNumeric: "tabular-nums",
                                      color: "var(--ink)",
                                    }}
                                  >
                                    {b.v}
                                  </span>
                                  <span
                                    style={{
                                      width: "58px",
                                      color: "var(--ink3)",
                                      whiteSpace: "nowrap",
                                    }}
                                  >
                                    <span data-l="zh">{b.rZh}</span>
                                    <span data-l="en">{b.rEn}</span>
                                  </span>
                                </div>
                              </React.Fragment>
                            ))}
                            {a.noQ && (
                              <>
                                {" "}
                                <span
                                  style={{
                                    fontSize: "12px",
                                    color: "var(--errT)",
                                  }}
                                >
                                  <span data-l="zh">
                                    {"额度未知或观测已过期"}
                                  </span>
                                  <span data-l="en">
                                    {"Quota unknown or stale"}
                                  </span>
                                </span>{" "}
                              </>
                            )}
                          </div>
                          <span
                            style={{
                              width: "96px",
                              display: "inline-flex",
                              alignItems: "center",
                              gap: "6px",
                              fontSize: "12px",
                              color: "var(--ink2)",
                              whiteSpace: "nowrap",
                            }}
                          >
                            <span
                              style={{
                                width: "6px",
                                height: "6px",
                                borderRadius: "50%",
                                background: a.dot,
                                boxShadow: "0 0 10px " + a.dot + "",
                              }}
                            ></span>
                            <span data-l="zh">{a.stZh}</span>
                            <span data-l="en">{a.stEn}</span>
                          </span>
                          <button
                            onClick={a.onToggle}
                            title="schedule"
                            style={{
                              flex: "none",
                              position: "relative",
                              width: "34px",
                              height: "20px",
                              border: "0",
                              borderRadius: "99px",
                              background: a.tgBg,
                              cursor: "pointer",
                              transition: "background .15s",
                            }}
                            role="switch"
                            aria-checked={a.on}
                            aria-label={a.toggleLabel}
                            disabled={a.busy}
                            type="button"
                          >
                            <span
                              style={{
                                position: "absolute",
                                top: "2px",
                                left: a.tgX,
                                width: "16px",
                                height: "16px",
                                borderRadius: "50%",
                                background: "oklch(1 0 0)",
                                boxShadow: "0 1px 2px oklch(0 0 0 / 0.25)",
                                transition: "left .15s",
                              }}
                            ></span>
                          </button>
                        </div>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    padding: "0 4px",
                  }}
                >
                  <span
                    style={{
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      fontWeight: "500",
                      color: "var(--ink3)",
                      letterSpacing: "0.14em",
                      textTransform: "uppercase",
                    }}
                  >
                    <span data-l="zh">{"客户端"}</span>
                    <span data-l="en">{"Clients"}</span>
                  </span>
                  <button
                    style={{
                      border: "0",
                      background: "transparent",
                      fontSize: "12px",
                      color: "var(--accent)",
                      cursor: "pointer",
                    }}
                    onClick={v.detectClients}
                    type="button"
                  >
                    <span data-l="zh">{"重新检测"}</span>
                    <span data-l="en">{"Detect again"}</span>
                  </button>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    {v.clients.map((c: any, i6: number) => (
                      <React.Fragment key={c.id ?? c.name ?? i6}>
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "12px",
                            minHeight: "58px",
                            padding: "10px 12px 10px 16px",
                            borderTop: "1px solid var(--line2)",
                            opacity: c.dim,
                          }}
                          className="v13-hover"
                          title={c.title}
                        >
                          <span
                            style={{
                              flex: "none",
                              width: "36px",
                              height: "36px",
                              borderRadius: "9px",
                              background: "var(--tile)",
                              boxShadow: "var(--tileSh)",
                              display: "flex",
                              alignItems: "center",
                              justifyContent: "center",
                            }}
                          >
                            <span
                              role="img"
                              data-mono={c.mono}
                              style={{
                                display: "inline-block",
                                width: "20px",
                                height: "20px",
                                background:
                                  "url(" +
                                  c.logo +
                                  ") center/contain no-repeat",
                              }}
                            ></span>
                          </span>
                          <div
                            style={{
                              flex: "1",
                              display: "flex",
                              flexDirection: "column",
                              minWidth: "0",
                              lineHeight: "1.4",
                            }}
                          >
                            <span
                              style={{ fontSize: "14px", fontWeight: "500" }}
                            >
                              {c.name}{" "}
                              <span
                                style={{
                                  fontSize: "12px",
                                  fontWeight: "400",
                                  color: "var(--ink3)",
                                }}
                              >
                                {c.ver}
                              </span>
                            </span>
                            <span
                              style={{
                                fontSize: "12px",
                                color: "var(--ink2)",
                                fontFamily: "var(--mono)",
                                overflow: "hidden",
                                textOverflow: "ellipsis",
                                whiteSpace: "nowrap",
                              }}
                            >
                              {c.path}
                            </span>
                          </div>
                          {!c.canPick && c.currentModel && <span title={c.currentModel} style={{ maxWidth: "240px", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontFamily: "var(--mono)" }}>{c.currentModel}</span>}
                          {c.canPick && (
                            <>
                              <button
                                onClick={c.onPick}
                                style={{
                                  display: "inline-flex",
                                  alignItems: "center",
                                  gap: "4px",
                                  maxWidth: "240px",
                                  height: "30px",
                                  padding: "0 6px 0 10px",
                                  border: "0",
                                  borderRadius: "7px",
                                  background: "transparent",
                                  fontFamily: "var(--mono)",
                                  fontSize: "12.5px",
                                  color: c.valC,
                                  cursor: "pointer",
                                }}
                                className="v13-hover"
                                type="button"
                              >
                                <span
                                  style={{
                                    overflow: "hidden",
                                    textOverflow: "ellipsis",
                                    whiteSpace: "nowrap",
                                  }}
                                >
                                  {c.val}
                                </span>
                                <span
                                  data-i=""
                                  style={{
                                    fontSize: "17px",
                                    color: "var(--ink3)",
                                  }}
                                >
                                  {"unfold_more"}
                                </span>
                              </button>
                            </>
                          )}
                          <span
                            style={{
                              width: "76px",
                              display: "inline-flex",
                              alignItems: "center",
                              gap: "6px",
                              fontSize: "12px",
                              color: "var(--ink2)",
                              whiteSpace: "nowrap",
                            }}
                          >
                            <span
                              style={{
                                width: "6px",
                                height: "6px",
                                borderRadius: "50%",
                                background: c.dot,
                                boxShadow: "0 0 10px " + c.dot + "",
                              }}
                            ></span>
                            <span data-l="zh">{c.stZh}</span>
                            <span data-l="en">{c.stEn}</span>
                          </span>
                          <button
                            onClick={c.onCfg}
                            title="config"
                            style={{
                              width: "30px",
                              height: "30px",
                              border: "0",
                              borderRadius: "7px",
                              background: "transparent",
                              cursor: "pointer",
                              display: "flex",
                              alignItems: "center",
                              justifyContent: "center",
                              visibility: c.cfgVis,
                            }}
                            className="v13-hover"
                            type="button"
                          >
                            <span
                              data-i=""
                              style={{ fontSize: "18px", color: "var(--ink2)" }}
                            >
                              {"tune"}
                            </span>
                          </button>
                        </div>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"需要留意"}</span>
                  <span data-l="en">{"Needs attention"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    {v.alerts.map((a: any, i7: number) => (
                      <React.Fragment key={a.id ?? a.name ?? i7}>
                        <button
                          onClick={a.onClick}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "12px",
                            width: "100%",
                            minHeight: "48px",
                            padding: "10px 16px",
                            border: "0",
                            borderTop: "1px solid var(--line2)",
                            background: "transparent",
                            textAlign: "left",
                            cursor: "pointer",
                          }}
                          className="v13-hover"
                          type="button"
                        >
                          <span
                            style={{
                              flex: "none",
                              width: "6px",
                              height: "6px",
                              borderRadius: "50%",
                              background: a.c,
                              boxShadow: "0 0 10px " + a.c + "",
                            }}
                          ></span>
                          <span
                            style={{
                              flex: "1",
                              fontSize: "13px",
                              minWidth: "0",
                            }}
                          >
                            <span data-l="zh">{a.zh}</span>
                            <span data-l="en">{a.en}</span>
                            <span style={{ color: "var(--ink2)" }}>
                              {"· "}
                              <span data-l="zh">{a.subZh}</span>
                              <span data-l="en">{a.subEn}</span>
                            </span>
                          </span>
                          <span
                            data-i=""
                            style={{ fontSize: "18px", color: "var(--ink3)" }}
                          >
                            {"chevron_right"}
                          </span>
                        </button>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
              </section>
              {v.emptyStates["01"]}
            </div>
          </main>
        </>
      )}
      {v.pg.sources && (
        <>
          <main
            data-screen-label="02 Sources"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "860px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "8px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 02"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"来源与账号"}</span>
                    <span data-l="en">{"Sources"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">
                      {"订阅账号、API Key 与本地模型服务"}
                    </span>
                    <span data-l="en">
                      {"Subscriptions, API keys and local model servers"}
                    </span>
                  </p>
                </div>
                <button
                  onClick={v.openAdd}
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: "4px",
                    height: "32px",
                    padding: "0 12px 0 8px",
                    border: "0",
                    borderRadius: "8px",
                    background:
                      "linear-gradient(135deg,var(--accent),var(--accent2))",
                    color: "var(--accentInk)",
                    fontSize: "13px",
                    fontWeight: "600",
                    cursor: "pointer",
                    boxShadow:
                      "0 1px 2px oklch(0.2 0.05 255 / 0.3),inset 0 1px 0 oklch(1 0 0 / 0.15)",
                  }}
                  className="v13-hover"
                  type="button"
                >
                  <span data-i="" style={{ fontSize: "18px" }}>
                    {"add"}
                  </span>
                  <span data-l="zh">{"添加来源"}</span>
                  <span data-l="en">{"Add source"}</span>
                </button>
              </header>
              <div
                style={{
                  background: "var(--glass)",
                  border: "1px solid var(--glassBd)",
                  borderRadius: "18px",
                  boxShadow: "var(--sh)",
                  backdropFilter: "blur(22px) saturate(1.5)",
                  WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                  overflow: "hidden",
                }}
              >
                <div style={{ marginTop: "-1px" }}>
                  {v.sources.map((s: any, i8: number) => (
                    <React.Fragment key={s.id ?? s.name ?? i8}>
                      <div
                        style={{
                          display: "flex",
                          flexDirection: "column",
                          borderTop: "1px solid var(--line2)",
                        }}
                      >
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "12px",
                            minHeight: "60px",
                            padding: "10px 12px 10px 8px",
                          }}
                        >
                          <span
                            data-i=""
                            title="drag to reorder priority"
                            style={{
                              fontSize: "18px",
                              color: "var(--ink3)",
                              cursor: "grab",
                              marginRight: "-6px",
                            }}
                          >
                            {"drag_indicator"}
                          </span>
                          <span
                            style={{
                              flex: "none",
                              width: "36px",
                              height: "36px",
                              borderRadius: "9px",
                              background: "var(--tile)",
                              boxShadow: "var(--tileSh)",
                              display: "flex",
                              alignItems: "center",
                              justifyContent: "center",
                              opacity: s.dim,
                            }}
                          >
                            <span
                              role="img"
                              data-mono={s.mono}
                              style={{
                                display: "inline-block",
                                width: "20px",
                                height: "20px",
                                background:
                                  "url(" +
                                  s.logo +
                                  ") center/contain no-repeat",
                              }}
                            ></span>
                          </span>
                          <div
                            style={{
                              flex: "1",
                              display: "flex",
                              flexDirection: "column",
                              minWidth: "0",
                              lineHeight: "1.4",
                              opacity: s.dim,
                            }}
                          >
                            <span
                              style={{ fontSize: "14px", fontWeight: "500" }}
                            >
                              {s.name}{" "}
                              <span
                                style={{
                                  fontSize: "12px",
                                  fontWeight: "400",
                                  color: "var(--ink3)",
                                }}
                              >
                                <span data-l="zh">{s.typeZh}</span>
                                <span data-l="en">{s.typeEn}</span>
                              </span>
                            </span>
                            <span
                              style={{
                                fontSize: "12px",
                                color: "var(--ink2)",
                                overflow: "hidden",
                                textOverflow: "ellipsis",
                                whiteSpace: "nowrap",
                              }}
                            >
                              {s.proto}
                              {" · "}
                              {s.acct}
                              {" · "}
                              {s.models} <span data-l="zh">{"个模型"}</span>
                              <span data-l="en">{"models"}</span>
                            </span>
                          </div>
                          {s.hasBars && (
                            <>
                              <div
                                style={{
                                  display: "flex",
                                  flexDirection: "column",
                                  gap: "5px",
                                  width: "150px",
                                  opacity: s.dim,
                                }}
                              >
                                {s.bars.map((b: any, i9: number) => (
                                  <React.Fragment key={b.id ?? b.name ?? i9}>
                                    <div
                                      style={{
                                        display: "flex",
                                        alignItems: "center",
                                        gap: "8px",
                                        fontSize: "11px",
                                        color: "var(--ink2)",
                                      }}
                                    >
                                      <span style={{ width: "28px" }}>
                                        {b.l}
                                      </span>
                                      <span
                                        style={{
                                          flex: "1",
                                          height: "4px",
                                          borderRadius: "99px",
                                          background: "var(--sunk)",
                                          overflow: "hidden",
                                        }}
                                      >
                                        <span
                                          style={{
                                            display: "block",
                                            width: b.w,
                                            height: "100%",
                                            background:
                                              "linear-gradient(90deg,var(--accent),var(--accent2))",
                                          }}
                                        ></span>
                                      </span>
                                      <span
                                        style={{
                                          width: "26px",
                                          textAlign: "right",
                                          fontVariantNumeric: "tabular-nums",
                                        }}
                                      >
                                        {b.v}
                                      </span>
                                    </div>
                                  </React.Fragment>
                                ))}
                              </div>
                            </>
                          )}
                          <span
                            style={{
                              width: "110px",
                              display: "inline-flex",
                              alignItems: "center",
                              gap: "6px",
                              fontSize: "12px",
                              color: "var(--ink2)",
                              whiteSpace: "nowrap",
                              opacity: s.dim,
                            }}
                          >
                            <span
                              style={{
                                width: "6px",
                                height: "6px",
                                borderRadius: "50%",
                                background: s.dot,
                                boxShadow: "0 0 10px " + s.dot + "",
                              }}
                            ></span>
                            {s.health}
                          </span>
                          <button
                            onClick={s.onLat}
                            title="latency test"
                            style={{
                              flex: "none",
                              display: "inline-flex",
                              alignItems: "center",
                              gap: "4px",
                              height: "26px",
                              minWidth: "68px",
                              padding: "0 8px",
                              border: "1px solid var(--line)",
                              borderRadius: "7px",
                              background: "var(--panel)",
                              fontSize: "12px",
                              color: s.latC,
                              fontVariantNumeric: "tabular-nums",
                              cursor: "pointer",
                              opacity: s.dim,
                            }}
                            disabled={s.busy}
                            className="v13-hover"
                            type="button"
                          >
                            <span data-i="" style={{ fontSize: "15px" }}>
                              {"speed"}
                            </span>
                            {s.latL}
                          </button>
                          <button
                            onClick={s.onToggle}
                            title="enabled"
                            style={{
                              flex: "none",
                              position: "relative",
                              width: "34px",
                              height: "20px",
                              border: "0",
                              borderRadius: "99px",
                              background: s.tgBg,
                              cursor: "pointer",
                              transition: "background .15s",
                            }}
                            role="switch"
                            aria-checked={s.on}
                            aria-label={s.toggleLabel}
                            disabled={s.busy}
                            type="button"
                          >
                            <span
                              style={{
                                position: "absolute",
                                top: "2px",
                                left: s.tgX,
                                width: "16px",
                                height: "16px",
                                borderRadius: "50%",
                                background: "oklch(1 0 0)",
                                boxShadow: "0 1px 2px oklch(0 0 0 / 0.25)",
                                transition: "left .15s",
                              }}
                            ></span>
                          </button>
                          <button
                            onClick={s.onExpand}
                            style={{
                              width: "30px",
                              height: "30px",
                              border: "0",
                              borderRadius: "7px",
                              background: "transparent",
                              cursor: "pointer",
                              display: "flex",
                              alignItems: "center",
                              justifyContent: "center",
                            }}
                            className="v13-hover"
                            type="button"
                          >
                            <span
                              data-i=""
                              style={{ fontSize: "18px", color: "var(--ink2)" }}
                            >
                              {s.chev}
                            </span>
                          </button>
                        </div>
                        {s.codex && <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "10px", padding: "12px 16px", borderTop: "1px solid var(--line2)" }}>
                          <button className="secondary" type="button" disabled={s.loginDisabled} onClick={s.onLogin}>
                            <span data-l="zh">{s.loginZh}</span><span data-l="en">{s.loginEn}</span>
                          </button>
                          <span style={{ fontSize: "12px", color: "var(--ink2)" }} role="status">{s.loginMessage || s.authS}</span>
                          {s.loginUrl && <a href={s.loginUrl} target="_blank" rel="noreferrer"><span data-l="zh">继续授权 ↗</span><span data-l="en">Continue authorization ↗</span></a>}
                          {s.loginWaiting && <button className="text" type="button" disabled={s.busy} onClick={s.onCancelLogin}><span data-l="zh">取消登录</span><span data-l="en">Cancel sign-in</span></button>}
                        </div>}
                        {s.open && (
                          <>
                            <div
                              style={{
                                display: "flex",
                                flexDirection: "column",
                                gap: "12px",
                                margin: "0 16px 14px 52px",
                                padding: "14px",
                                borderRadius: "10px",
                                background: "var(--sunk)",
                              }}
                            >
                              <div
                                style={{
                                  display: "grid",
                                  gridTemplateColumns:
                                    "repeat(auto-fit,minmax(150px,1fr))",
                                  gap: "10px 16px",
                                  fontSize: "12px",
                                }}
                              >
                                <div
                                  style={{
                                    display: "flex",
                                    flexDirection: "column",
                                  }}
                                >
                                  <span style={{ color: "var(--ink3)" }}>
                                    {"Base URL"}
                                  </span>
                                  <span style={{ fontFamily: "var(--mono)" }}>
                                    {s.base}
                                  </span>
                                </div>
                                <div
                                  style={{
                                    display: "flex",
                                    flexDirection: "column",
                                  }}
                                >
                                  <span style={{ color: "var(--ink3)" }}>
                                    <span data-l="zh">{"认证"}</span>
                                    <span data-l="en">{"Auth"}</span>
                                  </span>
                                  <span>
                                    {s.auth}
                                    {" · "}
                                    {s.authS}
                                    {" · "}
                                    {s.gen}
                                  </span>
                                </div>
                                <div
                                  style={{
                                    display: "flex",
                                    flexDirection: "column",
                                  }}
                                >
                                  <span style={{ color: "var(--ink3)" }}>
                                    <span data-l="zh">{"验证"}</span>
                                    <span data-l="en">{"Verification"}</span>
                                  </span>
                                  <span>
                                    <span data-l="zh">{s.vZh}</span>
                                    <span data-l="en">{s.vEn}</span>
                                  </span>
                                </div>
                                <div
                                  style={{
                                    display: "flex",
                                    flexDirection: "column",
                                  }}
                                >
                                  <span style={{ color: "var(--ink3)" }}>
                                    <span data-l="zh">{"额度"}</span>
                                    <span data-l="en">{"Quota"}</span>
                                  </span>
                                  <span>
                                    <span data-l="zh">{s.quotaZh}</span>
                                    <span data-l="en">{s.quotaEn}</span>
                                  </span>
                                </div>
                              </div>
                              {s.codex && (
                                <>
                                  <div
                                    style={{
                                      display: "flex",
                                      alignItems: "center",
                                      gap: "12px",
                                      paddingTop: "12px",
                                      borderTop: "1px solid var(--line)",
                                    }}
                                  >
                                    <div
                                      style={{
                                        flex: "1",
                                        display: "flex",
                                        flexDirection: "column",
                                        minWidth: "0",
                                        lineHeight: "1.45",
                                      }}
                                    >
                                      <span
                                        style={{
                                          fontSize: "13px",
                                          fontWeight: "500",
                                        }}
                                      >
                                        <span data-l="zh">
                                          {"订阅兼容调用"}
                                        </span>
                                        <span data-l="en">
                                          {"Subscription-compatible calls"}
                                        </span>
                                      </span>
                                      <span
                                        style={{
                                          fontSize: "12px",
                                          color: "var(--ink2)",
                                          textWrap: "pretty",
                                        }}
                                      >
                                        <span data-l="zh">
                                          {
                                            "接受 max_tokens 但不强制执行；关闭时这类请求返回 422"
                                          }
                                        </span>
                                        <span data-l="en">
                                          {
                                            "Accept max_tokens without enforcing it; when off such requests get 422"
                                          }
                                        </span>
                                      </span>
                                    </div>
                                    <button
                                      onClick={s.toggleAdj}
                                      style={{
                                        flex: "none",
                                        position: "relative",
                                        width: "34px",
                                        height: "20px",
                                        border: "0",
                                        borderRadius: "99px",
                                        background: s.adjBg,
                                        cursor: "pointer",
                                      }}
                                      disabled={s.busy}
                                      type="button"
                                    >
                                      <span
                                        style={{
                                          position: "absolute",
                                          top: "2px",
                                          left: s.adjX,
                                          width: "16px",
                                          height: "16px",
                                          borderRadius: "50%",
                                          background: "oklch(1 0 0)",
                                          boxShadow:
                                            "0 1px 2px oklch(0 0 0 / 0.25)",
                                          transition: "left .15s",
                                        }}
                                      ></span>
                                    </button>
                                  </div>
                                </>
                              )}
                              <div
                                style={{
                                  display: "flex",
                                  gap: "6px",
                                  flexWrap: "wrap",
                                }}
                              >
                                <button
                                  style={{
                                    height: "28px",
                                    padding: "0 10px",
                                    border: "1px solid var(--line)",
                                    borderRadius: "7px",
                                    background: "var(--panel)",
                                    fontSize: "12px",
                                    cursor: "pointer",
                                  }}
                                  onClick={s.actions.text}
                                  disabled={s.busy}
                                  className="v13-hover"
                                  type="button"
                                >
                                  <span data-l="zh">{"测试文本"}</span>
                                  <span data-l="en">{"Test text"}</span>
                                </button>
                                <button
                                  style={{
                                    height: "28px",
                                    padding: "0 10px",
                                    border: "1px solid var(--line)",
                                    borderRadius: "7px",
                                    background: "var(--panel)",
                                    fontSize: "12px",
                                    cursor: "pointer",
                                  }}
                                  onClick={s.actions.tools}
                                  disabled={s.busy}
                                  className="v13-hover"
                                  type="button"
                                >
                                  <span data-l="zh">{"测试工具两轮"}</span>
                                  <span data-l="en">{"Test tool loop"}</span>
                                </button>
                                <button
                                  style={{
                                    height: "28px",
                                    padding: "0 10px",
                                    border: "1px solid var(--line)",
                                    borderRadius: "7px",
                                    background: "var(--panel)",
                                    fontSize: "12px",
                                    cursor: "pointer",
                                  }}
                                  onClick={s.actions.models}
                                  disabled={s.busy}
                                  className="v13-hover"
                                  type="button"
                                >
                                  <span data-l="zh">{"获取模型"}</span>
                                  <span data-l="en">{"Fetch models"}</span>
                                </button>
                                <button
                                  style={{
                                    height: "28px",
                                    padding: "0 10px",
                                    border: "1px solid var(--line)",
                                    borderRadius: "7px",
                                    background: "var(--panel)",
                                    fontSize: "12px",
                                    cursor: "pointer",
                                  }}
                                  onClick={s.actions.quota}
                                  disabled={s.busy}
                                  className="v13-hover"
                                  type="button"
                                >
                                  <span data-l="zh">{"刷新额度"}</span>
                                  <span data-l="en">{"Refresh quota"}</span>
                                </button>
                                <button
                                  style={{
                                    height: "28px",
                                    padding: "0 10px",
                                    border: "1px solid var(--line)",
                                    borderRadius: "7px",
                                    background: "var(--panel)",
                                    fontSize: "12px",
                                    cursor: "pointer",
                                  }}
                                  onClick={s.actions.edit}
                                  disabled={s.busy}
                                  className="v13-hover"
                                  type="button"
                                >
                                  <span data-l="zh">{"替换凭据"}</span>
                                  <span data-l="en">
                                    {"Replace credential"}
                                  </span>
                                </button>
                                <button
                                  style={{
                                    marginLeft: "auto",
                                    height: "28px",
                                    padding: "0 10px",
                                    border: "0",
                                    borderRadius: "7px",
                                    background: "transparent",
                                    color: "var(--errT)",
                                    fontSize: "12px",
                                    cursor: "pointer",
                                  }}
                                  onClick={s.actions.delete}
                                  disabled={s.busy}
                                  className="v13-hover"
                                  type="button"
                                >
                                  <span data-l="zh">{"删除"}</span>
                                  <span data-l="en">{"Delete"}</span>
                                </button>
                              </div>
                            </div>
                          </>
                        )}
                      </div>
                    </React.Fragment>
                  ))}
                </div>
              </div>
              {v.emptyStates["02"]}
            </div>
          </main>
        </>
      )}
      {v.pg.models && (
        <>
          <main
            data-screen-label="03 Models"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "860px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "16px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 03"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"模型"}</span>
                    <span data-l="en">{"Models"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">
                      {"各来源实际提供的模型，以及每项能力的验证结果"}
                    </span>
                    <span data-l="en">
                      {
                        "What each source serves, and how each capability was verified"
                      }
                    </span>
                  </p>
                </div>
                <button
                  className="icon-button"
                  title="管理 / Manage"
                  aria-label="管理当前页面"
                  onClick={v.managePage}
                  type="button"
                >
                  <span data-i="">{"tune"}</span>
                </button>
              </header>
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "10px",
                  flexWrap: "wrap",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    gap: "6px",
                    flexWrap: "wrap",
                    flex: "1",
                  }}
                >
                  {v.modelSrcs.map((f: any, i10: number) => (
                    <React.Fragment key={f.id ?? f.name ?? i10}>
                      <button
                        onClick={f.onClick}
                        style={{
                          display: "inline-flex",
                          alignItems: "center",
                          gap: "6px",
                          height: "28px",
                          padding: "0 10px",
                          border: "1px solid " + f.bd + "",
                          borderRadius: "99px",
                          background: f.bg,
                          color: f.fg,
                          fontSize: "12px",
                          fontWeight: "500",
                          cursor: "pointer",
                        }}
                        type="button"
                      >
                        {f.notAll && (
                          <>
                            {" "}
                            <span
                              role="img"
                              data-mono={f.mono}
                              style={{
                                display: "inline-block",
                                flex: "none",
                                background:
                                  "url(" +
                                  f.logo +
                                  ") center/contain no-repeat",
                                width: "14px",
                                height: "14px",
                              }}
                            ></span>{" "}
                          </>
                        )}
                        {f.all && (
                          <>
                            {" "}
                            <span data-l="zh">{"全部"}</span>
                            <span data-l="en">{"All"}</span>{" "}
                          </>
                        )}
                        {f.notAll && (
                          <>
                            {" "}
                            <span>{f.n}</span>{" "}
                          </>
                        )}
                      </button>
                    </React.Fragment>
                  ))}
                </div>
                <div
                  style={{
                    display: "flex",
                    padding: "2px",
                    borderRadius: "9px",
                    background: "var(--sunk)",
                  }}
                >
                  {v.msorts.map((o: any, i11: number) => (
                    <React.Fragment key={o.id ?? o.name ?? i11}>
                      <button
                        onClick={o.onClick}
                        style={{
                          height: "26px",
                          padding: "0 10px",
                          border: "0",
                          borderRadius: "7px",
                          background: o.bg,
                          color: o.fg,
                          boxShadow: o.sh,
                          fontSize: "12px",
                          fontWeight: "500",
                          cursor: "pointer",
                        }}
                        type="button"
                      >
                        <span data-l="zh">{o.zh}</span>
                        <span data-l="en">{o.en}</span>
                      </button>
                    </React.Fragment>
                  ))}
                </div>
                <label
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "6px",
                    height: "30px",
                    width: "200px",
                    padding: "0 10px",
                    border: "1px solid var(--line)",
                    borderRadius: "7px",
                    background: "var(--panel)",
                  }}
                >
                  <span
                    data-i=""
                    style={{ fontSize: "16px", color: "var(--ink3)" }}
                  >
                    {"search"}
                  </span>
                  <input
                    value={v.mq}
                    onChange={v.onMq}
                    placeholder="model id"
                    style={{
                      flex: "1",
                      minWidth: "0",
                      border: "0",
                      outline: "none",
                      background: "transparent",
                      fontSize: "12px",
                    }}
                  />
                </label>
              </div>
              <div
                style={{
                  display: "flex",
                  justifyContent: "flex-end",
                  gap: "14px",
                  flexWrap: "wrap",
                  padding: "0 4px",
                  fontSize: "11px",
                  color: "var(--ink2)",
                }}
              >
                <span
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: "5px",
                  }}
                >
                  <span
                    style={{
                      width: "8px",
                      height: "8px",
                      borderRadius: "2px",
                      background: "var(--okT)",
                    }}
                  ></span>
                  <span data-l="zh">{"原生"}</span>
                  <span data-l="en">{"Native"}</span>
                </span>
                <span
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: "5px",
                  }}
                >
                  <span
                    style={{
                      width: "8px",
                      height: "8px",
                      borderRadius: "2px",
                      background: "var(--accent)",
                    }}
                  ></span>
                  <span data-l="zh">{"转换"}</span>
                  <span data-l="en">{"Translated"}</span>
                </span>
                <span
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: "5px",
                  }}
                >
                  <span
                    style={{
                      width: "8px",
                      height: "8px",
                      borderRadius: "2px",
                      background: "var(--warnT)",
                    }}
                  ></span>
                  <span data-l="zh">{"调整"}</span>
                  <span data-l="en">{"Adjusted"}</span>
                </span>
                <span
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: "5px",
                  }}
                >
                  <span
                    style={{
                      width: "8px",
                      height: "8px",
                      borderRadius: "2px",
                      background: "var(--ink3)",
                    }}
                  ></span>
                  <span data-l="zh">{"未验证"}</span>
                  <span data-l="en">{"Unverified"}</span>
                </span>
              </div>
              {v.modelGroups.map((g: any, i12: number) => (
                <React.Fragment key={g.id ?? g.name ?? i12}>
                  <section
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      gap: "8px",
                    }}
                  >
                    <div
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "8px",
                        padding: "0 4px",
                      }}
                    >
                      <span
                        role="img"
                        data-mono={g.mono}
                        style={{
                          display: "inline-block",
                          flex: "none",
                          background:
                            "url(" + g.logo + ") center/contain no-repeat",
                          width: "14px",
                          height: "14px",
                        }}
                      ></span>
                      <span
                        style={{
                          fontFamily: "var(--mono)",
                          fontSize: "10.5px",
                          fontWeight: "500",
                          color: "var(--ink3)",
                          letterSpacing: "0.14em",
                          textTransform: "uppercase",
                        }}
                      >
                        {g.name}
                      </span>
                      <span style={{ fontSize: "12px", color: "var(--ink3)" }}>
                        {g.n}
                      </span>
                    </div>
                    <div
                      style={{
                        background: "var(--glass)",
                        border: "1px solid var(--glassBd)",
                        borderRadius: "18px",
                        boxShadow: "var(--sh)",
                        backdropFilter: "blur(22px) saturate(1.5)",
                        WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                        overflow: "hidden",
                      }}
                    >
                      <div style={{ marginTop: "-1px" }}>
                        {g.items.map((m: any, i13: number) => (
                          <React.Fragment key={m.id ?? m.name ?? i13}>
                            <div
                              style={{
                                display: "flex",
                                alignItems: "center",
                                gap: "14px",
                                minHeight: "48px",
                                padding: "8px 16px",
                                borderTop: "1px solid var(--line2)",
                              }}
                              className="v13-hover"
                            >
                              <div
                                style={{
                                  flex: "1",
                                  display: "flex",
                                  flexDirection: "column",
                                  minWidth: "0",
                                  lineHeight: "1.4",
                                }}
                              >
                                <span
                                  style={{
                                    fontFamily: "var(--mono)",
                                    fontSize: "13px",
                                    fontWeight: "500",
                                  }}
                                >
                                  {m.m}
                                </span>
                                <span
                                  style={{
                                    fontSize: "11px",
                                    color: "var(--ink3)",
                                  }}
                                >
                                  {m.disc}
                                  {" · ctx "}
                                  {m.ctx}
                                </span>
                              </div>
                              <span style={{ display: "flex", gap: "2px" }}>
                                {m.caps.map((c: any, i14: number) => (
                                  <React.Fragment key={c.id ?? c.name ?? i14}>
                                    <span
                                      title={c.title}
                                      data-i=""
                                      style={{
                                        width: "22px",
                                        height: "22px",
                                        display: "flex",
                                        alignItems: "center",
                                        justifyContent: "center",
                                        fontSize: "15px",
                                        color: c.c,
                                      }}
                                    >
                                      {c.icon}
                                    </span>
                                  </React.Fragment>
                                ))}
                              </span>
                              <span
                                style={{
                                  width: "84px",
                                  display: "inline-flex",
                                  alignItems: "center",
                                  gap: "6px",
                                  fontSize: "12px",
                                  color: m.vC,
                                }}
                              >
                                <span
                                  style={{
                                    width: "6px",
                                    height: "6px",
                                    borderRadius: "50%",
                                    background: m.vDot,
                                    boxShadow: "0 0 10px " + m.vDot + "",
                                  }}
                                ></span>
                                {m.v}
                              </span>
                              <span
                                style={{
                                  width: "96px",
                                  textAlign: "right",
                                  fontSize: "12px",
                                  color: "var(--ink2)",
                                  fontVariantNumeric: "tabular-nums",
                                }}
                              >
                                {m.price}
                              </span>
                            </div>
                          </React.Fragment>
                        ))}
                      </div>
                    </div>
                  </section>
                </React.Fragment>
              ))}
              {v.noModels && (
                <>
                  {" "}
                  <div
                    style={{
                      padding: "40px",
                      textAlign: "center",
                      color: "var(--ink3)",
                      fontSize: "13px",
                    }}
                  >
                    <span data-l="zh">{"没有匹配的模型"}</span>
                    <span data-l="en">{"No matching models"}</span>
                  </div>{" "}
                </>
              )}
              {v.emptyStates["03"]}
            </div>
          </main>
        </>
      )}
      {v.pg.routes && (
        <>
          <main
            data-screen-label="04 Routes"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "860px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "28px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 04"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"路由"}</span>
                    <span data-l="en">{"Routes"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">
                      {"公开模型名映射到一组来源，按优先级与权重选择"}
                    </span>
                    <span data-l="en">
                      {
                        "Public names map to a group of sources, picked by priority and weight"
                      }
                    </span>
                  </p>
                </div>
                <button
                  className="icon-button"
                  title="管理 / Manage"
                  aria-label="管理当前页面"
                  onClick={v.managePage}
                  type="button"
                >
                  <span data-i="">{"tune"}</span>
                </button>
              </header>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"调度规则"}</span>
                  <span data-l="en">{"Scheduling"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    <div
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "58px",
                        padding: "10px 16px",
                        borderTop: "1px solid var(--line2)",
                      }}
                    >
                      <div
                        style={{
                          flex: "1",
                          display: "flex",
                          flexDirection: "column",
                          minWidth: "0",
                          lineHeight: "1.45",
                        }}
                      >
                        <span style={{ fontSize: "13px", fontWeight: "500" }}>
                          <span data-l="zh">{"订阅用完后使用付费 API"}</span>
                          <span data-l="en">{"Fall back to paid API"}</span>
                        </span>
                        <span
                          style={{ fontSize: "12px", color: "var(--ink2)" }}
                        >
                          <span data-l="zh">
                            {
                              "先用订阅账号的额度，全部不可用时才转到 API Key 来源"
                            }
                          </span>
                          <span data-l="en">
                            {
                              "Subscription quota is used first; API key sources only when none is available"
                            }
                          </span>
                        </span>
                      </div>
                      <button
                        onClick={v.xTogglePaid}
                        role="switch"
                        aria-checked={v.xPaid}
                        aria-label="订阅用完后使用付费 API"
                        style={{
                          flex: "none",
                          position: "relative",
                          width: "34px",
                          height: "20px",
                          border: "0",
                          borderRadius: "99px",
                          background: v.xPaidBg,
                          cursor: "pointer",
                          transition: "background .15s",
                        }}
                        disabled={v.schedulingUnavailable}
                        title={v.schedulingReason}
                        type="button"
                      >
                        <span
                          style={{
                            position: "absolute",
                            top: "2px",
                            left: v.xPaidX,
                            width: "16px",
                            height: "16px",
                            borderRadius: "50%",
                            background: "oklch(1 0 0)",
                            boxShadow: "0 1px 2px oklch(0 0 0 / 0.25)",
                            transition: "left .15s",
                          }}
                        ></span>
                      </button>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "52px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                      }}
                    >
                      <span
                        style={{
                          flex: "1",
                          fontSize: "13px",
                          fontWeight: "500",
                        }}
                      >
                        <span data-l="zh">{"剩余额度低于此值时跳过账号"}</span>
                        <span data-l="en">
                          {"Skip an account below this remaining quota"}
                        </span>
                      </span>
                      <div
                        style={{
                          display: "flex",
                          padding: "2px",
                          borderRadius: "9px",
                          background: "var(--sunk)",
                        }}
                      >
                        {v.xThr.map((o: any, i15: number) => (
                          <React.Fragment key={o.id ?? o.name ?? i15}>
                            <button
                              onClick={o.onClick}
                              style={{
                                height: "26px",
                                padding: "0 10px",
                                border: "0",
                                borderRadius: "7px",
                                background: o.bg,
                                color: o.fg,
                                boxShadow: o.sh,
                                fontSize: "12px",
                                fontWeight: "500",
                                fontVariantNumeric: "tabular-nums",
                                cursor: "pointer",
                              }}
                              aria-pressed={o.active}
                              disabled={v.schedulingUnavailable}
                              title={v.schedulingReason}
                              type="button"
                            >
                              {o.k}
                            </button>
                          </React.Fragment>
                        ))}
                      </div>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "58px",
                        padding: "10px 16px",
                        borderTop: "1px solid var(--line2)",
                        background: "var(--sunk)",
                      }}
                    >
                      <span
                        style={{
                          fontSize: "12px",
                          color: "var(--ink2)",
                          whiteSpace: "nowrap",
                        }}
                      >
                        <span data-l="zh">{"下一次 coding 请求"}</span>
                        <span data-l="en">{"Next coding request"}</span>
                      </span>
                      <span
                        data-i=""
                        style={{ fontSize: "16px", color: "var(--ink3)" }}
                      >
                        {"arrow_forward"}
                      </span>
                      <span
                        style={{
                          flex: "none",
                          width: "28px",
                          height: "28px",
                          borderRadius: "8px",
                          background: "var(--tile)",
                          boxShadow: "var(--tileSh)",
                          display: "flex",
                          alignItems: "center",
                          justifyContent: "center",
                        }}
                      >
                        <span
                          role="img"
                          data-mono={v.xNext.mono}
                          style={{
                            display: "inline-block",
                            width: "16px",
                            height: "16px",
                            background:
                              "url(" +
                              v.xNext.logo +
                              ") center/contain no-repeat",
                          }}
                        ></span>
                      </span>
                      <div
                        style={{
                          flex: "1",
                          display: "flex",
                          flexDirection: "column",
                          minWidth: "0",
                          lineHeight: "1.4",
                        }}
                      >
                        <span
                          style={{
                            fontSize: "13px",
                            fontWeight: "600",
                            color: v.xNext.c,
                          }}
                        >
                          {v.xNext.name}
                        </span>
                        <span
                          style={{
                            fontSize: "12px",
                            color: "var(--ink2)",
                            textWrap: "pretty",
                          }}
                        >
                          <span data-l="zh">{v.xNext.zh}</span>
                          <span data-l="en">{v.xNext.en}</span>
                        </span>
                      </div>
                    </div>
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"公开模型名"}</span>
                  <span data-l="en">{"Public model names"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    {v.aliases.map((a: any, i16: number) => (
                      <React.Fragment key={a.id ?? a.name ?? i16}>
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "12px",
                            minHeight: "44px",
                            padding: "8px 16px",
                            borderTop: "1px solid var(--line2)",
                          }}
                          className="v13-hover"
                        >
                          <span
                            style={{
                              fontFamily: "var(--mono)",
                              fontSize: "13px",
                              fontWeight: "500",
                              color: "var(--accent)",
                              width: "120px",
                            }}
                          >
                            {a.pub}
                          </span>
                          <span
                            data-i=""
                            style={{ fontSize: "16px", color: "var(--ink3)" }}
                          >
                            {"arrow_forward"}
                          </span>
                          <span
                            style={{
                              flex: "1",
                              fontFamily: "var(--mono)",
                              fontSize: "12px",
                              color: "var(--ink2)",
                            }}
                          >
                            {a.target}
                          </span>
                          <span
                            style={{ fontSize: "12px", color: "var(--ink3)" }}
                          >
                            {a.n} <span data-l="zh">{"个成员"}</span>
                            <span data-l="en">{"members"}</span>
                          </span>
                        </div>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
              </section>
              {v.routes.map((r: any, i17: number) => (
                <React.Fragment key={r.id ?? r.name ?? i17}>
                  <section
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      gap: "8px",
                    }}
                  >
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        padding: "0 4px",
                      }}
                    >
                      <span
                        title={r.runtimeTitle}
                        style={{
                          minWidth: 0,
                          overflowWrap: "anywhere",
                          flex: 1,
                          fontFamily: "var(--mono)",
                          fontSize: "10.5px",
                          fontWeight: "500",
                          color: "var(--ink3)",
                          letterSpacing: "0.14em",
                          textTransform: "uppercase",
                        }}
                      >
                        <span
                          style={{
                            fontFamily: "var(--mono)",
                            color: "var(--ink)",
                          }}
                        >
                          {r.name || r.id}
                        </span>
                        {" · "}
                        <span data-l="zh">
                          {r.selZh}
                          {" · 最多 "}
                          {r.att}
                          {" 次 · "}
                          {r.compatZh}
                        </span>
                        <span data-l="en">
                          {r.selEn}
                          {" · "}
                          {r.att}
                          {" attempts max · "}
                          {r.compatEn}
                        </span>
                      </span>
                      <button
                        onClick={r.onPreview}
                        disabled={r.busy}
                        style={{
                          display: "inline-flex",
                          alignItems: "center",
                          gap: "4px",
                          height: "26px",
                          padding: "0 10px 0 6px",
                          border: "1px solid var(--line)",
                          borderRadius: "7px",
                          background: "var(--panel)",
                          fontSize: "12px",
                          cursor: "pointer",
                        }}
                        className="v13-hover"
                        type="button"
                      >
                        <span data-i="" style={{ fontSize: "16px" }}>
                          {"visibility"}
                        </span>
                        <span data-l="zh">{r.pvLabelZh}</span>
                        <span data-l="en">{r.pvLabelEn}</span>
                      </button>
                    </div>
                    <div
                      style={{
                        background: "var(--glass)",
                        border: "1px solid var(--glassBd)",
                        borderRadius: "18px",
                        boxShadow: "var(--sh)",
                        backdropFilter: "blur(22px) saturate(1.5)",
                        WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                        overflow: "hidden",
                      }}
                    >
                      <div style={{ marginTop: "-1px" }}>
                        {r.members.map((m: any, i18: number) => (
                          <React.Fragment key={m.id ?? m.name ?? i18}>
                            <div
                              style={{
                                display: "flex",
                                alignItems: "center",
                                gap: "12px",
                                minHeight: "52px",
                                padding: "8px 16px",
                                borderTop: "1px solid var(--line2)",
                              }}
                              className="v13-hover"
                            >
                              <span
                                style={{
                                  flex: "none",
                                  width: "22px",
                                  height: "22px",
                                  borderRadius: "6px",
                                  background: "var(--sunk)",
                                  fontSize: "11px",
                                  fontWeight: "600",
                                  color: "var(--ink2)",
                                  display: "flex",
                                  alignItems: "center",
                                  justifyContent: "center",
                                }}
                              >
                                {"P"}
                                {m.p}
                              </span>
                              <span
                                style={{
                                  flex: "none",
                                  width: "30px",
                                  height: "30px",
                                  borderRadius: "8px",
                                  background: "var(--tile)",
                                  boxShadow: "var(--tileSh)",
                                  display: "flex",
                                  alignItems: "center",
                                  justifyContent: "center",
                                }}
                              >
                                <span
                                  role="img"
                                  data-mono={m.mono}
                                  style={{
                                    display: "inline-block",
                                    width: "16px",
                                    height: "16px",
                                    background:
                                      "url(" +
                                      m.logo +
                                      ") center/contain no-repeat",
                                  }}
                                ></span>
                              </span>
                              <div
                                style={{
                                  flex: "1",
                                  display: "flex",
                                  flexDirection: "column",
                                  minWidth: "0",
                                  lineHeight: "1.4",
                                }}
                              >
                                <span
                                  style={{
                                    fontSize: "13px",
                                    fontWeight: "500",
                                  }}
                                >
                                  {m.src}
                                </span>
                                <span
                                  style={{
                                    fontFamily: "var(--mono)",
                                    fontSize: "12px",
                                    color: "var(--ink2)",
                                  }}
                                >
                                  {m.m}
                                </span>
                              </div>
                              <div
                                style={{
                                  display: "flex",
                                  alignItems: "center",
                                  gap: "8px",
                                  width: "140px",
                                }}
                              >
                                <span
                                  style={{
                                    flex: "1",
                                    height: "4px",
                                    borderRadius: "99px",
                                    background: "var(--sunk)",
                                    overflow: "hidden",
                                  }}
                                >
                                  <span
                                    style={{
                                      display: "block",
                                      width: m.ww,
                                      height: "100%",
                                      background:
                                        "linear-gradient(90deg,var(--accent),var(--accent2))",
                                    }}
                                  ></span>
                                </span>
                                <span
                                  style={{
                                    width: "52px",
                                    textAlign: "right",
                                    fontSize: "12px",
                                    color: "var(--ink2)",
                                    fontVariantNumeric: "tabular-nums",
                                  }}
                                >
                                  {m.share}
                                </span>
                              </div>
                              <span
                                style={{
                                  width: "56px",
                                  fontSize: "12px",
                                  color: "var(--ink3)",
                                  fontVariantNumeric: "tabular-nums",
                                }}
                              >
                                {m.slot}
                              </span>
                              <span
                                title={m.title}
                                style={{
                                  width: "96px",
                                  display: "inline-flex",
                                  alignItems: "center",
                                  gap: "6px",
                                  fontSize: "12px",
                                  color: "var(--ink2)",
                                  whiteSpace: "nowrap",
                                }}
                              >
                                <span
                                  style={{
                                    width: "6px",
                                    height: "6px",
                                    borderRadius: "50%",
                                    background: m.dot,
                                    boxShadow: "0 0 10px " + m.dot + "",
                                  }}
                                ></span>
                                {m.h}
                              </span>
                            </div>
                          </React.Fragment>
                        ))}
                      </div>
                      {r.previewed && (
                        <>
                          <div
                            style={{
                              display: "flex",
                              flexDirection: "column",
                              gap: "10px",
                              padding: "14px 16px",
                              borderTop: "1px solid var(--line)",
                              background: "var(--sunk)",
                            }}
                          >
                            <span
                              style={{ fontSize: "12px", color: "var(--ink2)" }}
                            >
                              <span data-l="zh">{"示例请求"}</span>
                              <span data-l="en">{"Sample request"}</span>
                              {" · "}
                              <span style={{ color: "var(--ink)" }}>
                                {r.pvIn}
                              </span>
                            </span>
                            {r.ok.map((o: any, i19: number) => (
                              <React.Fragment key={o.id ?? o.name ?? i19}>
                                <div
                                  style={{
                                    display: "flex",
                                    alignItems: "flex-start",
                                    gap: "10px",
                                    fontSize: "12px",
                                  }}
                                >
                                  <span
                                    style={{
                                      flex: "none",
                                      width: "18px",
                                      height: "18px",
                                      borderRadius: "50%",
                                      background: "var(--ok)",
                                      color: "oklch(1 0 0)",
                                      fontSize: "11px",
                                      fontWeight: "600",
                                      display: "flex",
                                      alignItems: "center",
                                      justifyContent: "center",
                                    }}
                                  >
                                    {o.rank}
                                  </span>
                                  <span
                                    role="img"
                                    data-mono={o.mono}
                                    style={{
                                      display: "inline-block",
                                      flex: "none",
                                      background:
                                        "url(" +
                                        o.logo +
                                        ") center/contain no-repeat",
                                      width: "16px",
                                      height: "16px",
                                      marginTop: "1px",
                                    }}
                                  ></span>
                                  <span style={{ textWrap: "pretty" }}>
                                    <span style={{ fontWeight: "500" }}>
                                      {o.src}
                                      {"/"}
                                      {o.m}
                                    </span>
                                    <span style={{ color: "var(--ink2)" }}>
                                      <span data-l="zh">{o.zh}</span>
                                      <span data-l="en">{o.en}</span>
                                    </span>
                                  </span>
                                </div>
                              </React.Fragment>
                            ))}
                            {r.no.map((o: any, i20: number) => (
                              <React.Fragment key={o.id ?? o.name ?? i20}>
                                <div
                                  style={{
                                    display: "flex",
                                    alignItems: "flex-start",
                                    gap: "10px",
                                    fontSize: "12px",
                                    color: "var(--ink2)",
                                  }}
                                >
                                  <span
                                    data-i=""
                                    style={{
                                      flex: "none",
                                      width: "18px",
                                      fontSize: "17px",
                                      color: "var(--errT)",
                                    }}
                                  >
                                    {"block"}
                                  </span>
                                  <span
                                    role="img"
                                    data-mono={o.mono}
                                    style={{
                                      display: "inline-block",
                                      flex: "none",
                                      background:
                                        "url(" +
                                        o.logo +
                                        ") center/contain no-repeat",
                                      width: "16px",
                                      height: "16px",
                                      marginTop: "1px",
                                      opacity: "0.5",
                                    }}
                                  ></span>
                                  <span style={{ textWrap: "pretty" }}>
                                    <span
                                      style={{ textDecoration: "line-through" }}
                                    >
                                      {o.src}
                                      {"/"}
                                      {o.m}
                                    </span>
                                    <span data-l="zh">{o.zh}</span>
                                    <span data-l="en">{o.en}</span>
                                  </span>
                                </div>
                              </React.Fragment>
                            ))}
                            <span
                              style={{
                                paddingTop: "8px",
                                borderTop: "1px solid var(--line)",
                                fontSize: "12px",
                                fontWeight: "500",
                              }}
                            >
                              <span data-l="zh">{r.pickZh}</span>
                              <span data-l="en">{r.pickEn}</span>
                            </span>
                          </div>
                        </>
                      )}
                    </div>
                  </section>
                </React.Fragment>
              ))}
              {v.emptyStates["04"]}
            </div>
          </main>
        </>
      )}
      {v.pg.keys && (
        <>
          <main
            data-screen-label="05 API Keys"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "860px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "8px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 05"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"API Keys"}</span>
                    <span data-l="en">{"API Keys"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">
                      {"供客户端调用 Cove，明文只在创建时显示一次"}
                    </span>
                    <span data-l="en">
                      {"For clients calling Cove; the secret is shown once"}
                    </span>
                  </p>
                </div>
                <button
                  onClick={v.openKey}
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: "4px",
                    height: "32px",
                    padding: "0 12px 0 8px",
                    border: "0",
                    borderRadius: "8px",
                    background:
                      "linear-gradient(135deg,var(--accent),var(--accent2))",
                    color: "var(--accentInk)",
                    fontSize: "13px",
                    fontWeight: "600",
                    cursor: "pointer",
                    boxShadow:
                      "0 1px 2px oklch(0.2 0.05 255 / 0.3),inset 0 1px 0 oklch(1 0 0 / 0.15)",
                  }}
                  className="v13-hover"
                  type="button"
                >
                  <span data-i="" style={{ fontSize: "18px" }}>
                    {"add"}
                  </span>
                  <span data-l="zh">{"创建 Key"}</span>
                  <span data-l="en">{"Create key"}</span>
                </button>
              </header>
              <div
                style={{
                  background: "var(--glass)",
                  border: "1px solid var(--glassBd)",
                  borderRadius: "18px",
                  boxShadow: "var(--sh)",
                  backdropFilter: "blur(22px) saturate(1.5)",
                  WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                  overflow: "hidden",
                }}
              >
                <div style={{ marginTop: "-1px" }}>
                  {v.keys.map((k: any, i21: number) => (
                    <React.Fragment key={k.id ?? k.name ?? i21}>
                      <div
                        style={{
                          display: "flex",
                          alignItems: "center",
                          gap: "14px",
                          minHeight: "64px",
                          padding: "10px 12px 10px 16px",
                          borderTop: "1px solid var(--line2)",
                          opacity: k.dim,
                        }}
                        className="v13-hover"
                      >
                        <div
                          style={{
                            flex: "1",
                            display: "flex",
                            flexDirection: "column",
                            minWidth: "0",
                            lineHeight: "1.45",
                          }}
                        >
                          <span style={{ fontSize: "14px", fontWeight: "500" }}>
                            {k.name}{" "}
                            <span
                              style={{
                                fontFamily: "var(--mono)",
                                fontSize: "11px",
                                fontWeight: "400",
                                color: "var(--ink3)",
                              }}
                            >
                              {k.fp}
                            </span>
                          </span>
                          <span
                            style={{
                              fontSize: "12px",
                              color: "var(--ink2)",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                            }}
                          >
                            <span style={{ fontFamily: "var(--mono)" }}>
                              {k.target}
                            </span>
                            {" · "}
                            {k.protos}
                            {" · "}
                            <span
                              style={{
                                color: "var(--accent)",
                                fontFamily: "var(--mono)",
                              }}
                            >
                              {k.models}
                            </span>
                          </span>
                          <span
                            style={{ fontSize: "12px", color: "var(--ink3)" }}
                          >
                            <span data-l="zh">
                              {k.limZh}
                              {" · "}
                              {k.expZh}
                            </span>
                            <span data-l="en">
                              {k.limEn}
                              {" · "}
                              {k.expEn}
                            </span>
                          </span>
                        </div>
                        <span
                          style={{
                            width: "80px",
                            fontSize: "12px",
                            color: "var(--ink2)",
                            textAlign: "right",
                          }}
                        >
                          <span data-l="zh">{k.lastZh}</span>
                          <span data-l="en">{k.lastEn}</span>
                        </span>
                        <span
                          style={{
                            width: "66px",
                            display: "inline-flex",
                            alignItems: "center",
                            gap: "6px",
                            fontSize: "12px",
                            color: "var(--ink2)",
                          }}
                        >
                          <span
                            style={{
                              width: "6px",
                              height: "6px",
                              borderRadius: "50%",
                              background: k.dot,
                              boxShadow: "0 0 10px " + k.dot + "",
                            }}
                          ></span>
                          <span data-l="zh">{k.stZh}</span>
                          <span data-l="en">{k.stEn}</span>
                        </span>
                        <span
                          style={{
                            width: "64px",
                            display: "flex",
                            justifyContent: "flex-end",
                            gap: "2px",
                          }}
                        >
                          {k.active && (
                            <>
                              {" "}
                              <button
                                title="Rotate"
                                style={{
                                  width: "30px",
                                  height: "30px",
                                  border: "0",
                                  borderRadius: "7px",
                                  background: "transparent",
                                  cursor: "pointer",
                                  display: "flex",
                                  alignItems: "center",
                                  justifyContent: "center",
                                }}
                                onClick={k.onManage}
                                className="v13-hover"
                                type="button"
                              >
                                <span
                                  data-i=""
                                  style={{
                                    fontSize: "18px",
                                    color: "var(--ink2)",
                                  }}
                                >
                                  {"autorenew"}
                                </span>
                              </button>
                              <button
                                title="Revoke"
                                style={{
                                  width: "30px",
                                  height: "30px",
                                  border: "0",
                                  borderRadius: "7px",
                                  background: "transparent",
                                  cursor: "pointer",
                                  display: "flex",
                                  alignItems: "center",
                                  justifyContent: "center",
                                }}
                                onClick={k.onRevoke}
                                disabled={k.busy}
                                className="v13-hover"
                                type="button"
                              >
                                <span
                                  data-i=""
                                  style={{
                                    fontSize: "18px",
                                    color: "var(--errT)",
                                  }}
                                >
                                  {"block"}
                                </span>
                              </button>{" "}
                            </>
                          )}
                        </span>
                      </div>
                    </React.Fragment>
                  ))}
                </div>
              </div>
              {v.emptyStates["05"]}
            </div>
          </main>
        </>
      )}
      {v.pg.requests && (
        <>
          <main
            data-screen-label="06 Requests"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "860px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "12px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 06"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"请求"}</span>
                    <span data-l="en">{"Requests"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">
                      {"只记录元数据，不保存提示词与响应"}
                    </span>
                    <span data-l="en">
                      {"Metadata only; prompts and outputs are never stored"}
                    </span>
                  </p>
                </div>
                <button
                  className="icon-button"
                  title="管理 / Manage"
                  aria-label="管理当前页面"
                  onClick={v.managePage}
                  type="button"
                >
                  <span data-i="">{"tune"}</span>
                </button>
              </header>
              <section
                style={{
                  display: "grid",
                  gridTemplateColumns: "repeat(4,minmax(0,1fr))",
                  background: "var(--glass)",
                  border: "1px solid var(--glassBd)",
                  borderRadius: "18px",
                  boxShadow: "var(--sh)",
                  backdropFilter: "blur(22px) saturate(1.5)",
                  WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                  overflow: "hidden",
                }}
              >
                {v.reqStats.map((m: any, i22: number) => (
                  <React.Fragment key={m.id ?? m.name ?? i22}>
                    <div
                      style={{
                        display: "flex",
                        flexDirection: "column",
                        gap: "6px",
                        padding: "14px 16px",
                        borderLeft: "1px solid var(--line2)",
                        marginLeft: "-1px",
                      }}
                    >
                      <span style={{ fontSize: "12px", color: "var(--ink2)" }}>
                        <span data-l="zh">{m.zh}</span>
                        <span data-l="en">{m.en}</span>
                      </span>
                      <span
                        style={{
                          fontSize: "22px",
                          fontWeight: "600",
                          letterSpacing: "-0.025em",
                          fontVariantNumeric: "tabular-nums",
                          color: m.c,
                        }}
                      >
                        {m.v}
                      </span>
                      <span
                        style={{
                          display: "flex",
                          alignItems: "flex-end",
                          gap: "2px",
                          height: "18px",
                        }}
                      >
                        {m.spark.map((b: any, i23: number) => (
                          <React.Fragment key={b.id ?? b.name ?? i23}>
                            <span
                              style={{
                                flex: "1",
                                height: b.h,
                                borderRadius: "1px",
                                background: m.sc,
                              }}
                            ></span>
                          </React.Fragment>
                        ))}
                      </span>
                    </div>
                  </React.Fragment>
                ))}
              </section>
              <div
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "center",
                  gap: "10px",
                  flexWrap: "wrap",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    padding: "2px",
                    borderRadius: "9px",
                    background: "var(--sunk)",
                  }}
                >
                  {v.reqFilters.map((f: any, i24: number) => (
                    <React.Fragment key={f.id ?? f.name ?? i24}>
                      <button
                        onClick={f.onClick}
                        style={{
                          display: "inline-flex",
                          alignItems: "center",
                          gap: "6px",
                          height: "26px",
                          padding: "0 10px",
                          border: "0",
                          borderRadius: "7px",
                          background: f.bg,
                          color: f.fg,
                          boxShadow: f.sh,
                          fontSize: "12px",
                          fontWeight: "500",
                          cursor: "pointer",
                        }}
                        type="button"
                      >
                        <span data-l="zh">{f.zh}</span>
                        <span data-l="en">{f.en}</span>
                        <span style={{ color: "var(--ink3)" }}>{f.n}</span>
                      </button>
                    </React.Fragment>
                  ))}
                </div>
              </div>
              <div
                style={{
                  background: "var(--glass)",
                  border: "1px solid var(--glassBd)",
                  borderRadius: "18px",
                  boxShadow: "var(--sh)",
                  backdropFilter: "blur(22px) saturate(1.5)",
                  WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                  overflow: "hidden",
                }}
              >
                <div style={{ marginTop: "-1px" }}>
                  {v.reqs.map((r: any, i25: number) => (
                    <React.Fragment key={r.id ?? r.name ?? i25}>
                      <button
                        onClick={r.onClick}
                        style={{
                          display: "flex",
                          alignItems: "center",
                          gap: "12px",
                          width: "100%",
                          minHeight: "52px",
                          padding: "8px 12px 8px 16px",
                          border: "0",
                          borderTop: "1px solid var(--line2)",
                          background: "transparent",
                          textAlign: "left",
                          cursor: "pointer",
                          fontVariantNumeric: "tabular-nums",
                        }}
                        className="v13-hover"
                        type="button"
                      >
                        <span
                          style={{
                            flex: "none",
                            width: "6px",
                            height: "6px",
                            borderRadius: "50%",
                            background: r.dot,
                            boxShadow: "0 0 10px " + r.dot + "",
                          }}
                        ></span>
                        <span
                          style={{
                            width: "58px",
                            fontFamily: "var(--mono)",
                            fontSize: "12px",
                            color: "var(--ink2)",
                          }}
                        >
                          {r.time}
                        </span>
                        <div
                          style={{
                            flex: "1",
                            display: "flex",
                            flexDirection: "column",
                            minWidth: "0",
                            lineHeight: "1.4",
                          }}
                        >
                          <span
                            style={{
                              fontSize: "13px",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                            }}
                          >
                            <span
                              style={{
                                fontFamily: "var(--mono)",
                                color: "var(--accent)",
                              }}
                            >
                              {r.model}
                            </span>
                            <span style={{ color: "var(--ink3)" }}>{"→"}</span>
                            <span style={{ fontFamily: "var(--mono)" }}>
                              {r.sent}
                            </span>
                          </span>
                          <span
                            style={{
                              fontSize: "12px",
                              color: "var(--ink3)",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                            }}
                          >
                            {r.key}
                            {" · "}
                            {r.proto}
                            {" · "}
                            {r.id}
                          </span>
                        </div>
                        <span
                          style={{
                            width: "96px",
                            textAlign: "right",
                            fontSize: "12px",
                            color: "var(--ink2)",
                          }}
                        >
                          {r.tok}
                        </span>
                        <span
                          style={{
                            width: "48px",
                            textAlign: "right",
                            fontSize: "12px",
                            color: "var(--ink2)",
                          }}
                        >
                          {r.dur}
                        </span>
                        <span
                          style={{
                            width: "76px",
                            textAlign: "right",
                            fontSize: "12px",
                            color: r.stC,
                          }}
                        >
                          <span data-l="zh">{r.stZh}</span>
                          <span data-l="en">{r.stEn}</span>
                        </span>
                        <span
                          data-i=""
                          style={{ fontSize: "18px", color: "var(--ink3)" }}
                        >
                          {"chevron_right"}
                        </span>
                      </button>
                    </React.Fragment>
                  ))}
                </div>
              </div>
              {v.emptyStates["06"]}
              {v.hasMoreRequests && <button className="secondary" disabled={v.moreRequestsBusy} onClick={v.moreRequests} type="button"><span data-l="zh">{v.moreRequestsBusy ? "正在加载…" : "加载更多请求"}</span><span data-l="en">{v.moreRequestsBusy ? "Loading…" : "Load more requests"}</span></button>}
            </div>
          </main>
        </>
      )}
      {v.pg.usage && (
        <>
          <main
            data-screen-label="07 Usage"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "860px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "28px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 07"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"用量"}</span>
                    <span data-l="en">{"Usage"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">
                      {"已知用量、估算费用与订阅额度分开统计"}
                    </span>
                    <span data-l="en">
                      {"Known usage, estimated cost and plan quota, kept apart"}
                    </span>
                  </p>
                </div>
                <button
                  className="icon-button"
                  title="管理 / Manage"
                  aria-label="管理当前页面"
                  onClick={v.managePage}
                  type="button"
                >
                  <span data-i="">{"tune"}</span>
                </button>
              </header>
              <section
                style={{
                  background: "var(--glass)",
                  border: "1px solid var(--glassBd)",
                  borderRadius: "18px",
                  boxShadow: "var(--sh)",
                  backdropFilter: "blur(22px) saturate(1.5)",
                  WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                  padding: "16px 18px 12px",
                  display: "flex",
                  flexDirection: "column",
                  gap: "14px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "flex-start",
                    gap: "12px",
                    flexWrap: "wrap",
                  }}
                >
                  <div style={{ display: "flex", flexDirection: "column" }}>
                    <span style={{ fontSize: "12px", color: "var(--ink2)" }}>
                      <span data-l="zh">{"已知 tokens"}</span>
                      <span data-l="en">{"Known tokens"}</span>
                    </span>
                    <span
                      style={{
                        fontSize: "22px",
                        fontWeight: "600",
                        letterSpacing: "-0.01em",
                      }}
                    >
                      {v.usageTokenCount}
                    </span>
                  </div>
                  <div
                    style={{
                      display: "flex",
                      padding: "2px",
                      borderRadius: "9px",
                      background: "var(--sunk)",
                    }}
                  >
                    {v.ranges.map((g: any, i26: number) => (
                      <React.Fragment key={g.id ?? g.name ?? i26}>
                        <button
                          onClick={g.onClick}
                          style={{
                            height: "26px",
                            padding: "0 10px",
                            border: "0",
                            borderRadius: "7px",
                            background: g.bg,
                            color: g.fg,
                            boxShadow: g.sh,
                            fontSize: "12px",
                            fontWeight: "500",
                            cursor: "pointer",
                          }}
                          type="button"
                        >
                          {g.l}
                        </button>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns: v.barColumns,
                    gap: "8px",
                    alignItems: "end",
                    height: "150px",
                  }}
                >
                  {v.bars.map((b: any, i27: number) => (
                    <React.Fragment key={b.id ?? b.name ?? i27}>
                      <div
                        title={"" + b.k + "M"}
                        style={{
                          display: "flex",
                          flexDirection: "column",
                          justifyContent: "flex-end",
                          height: "100%",
                          gap: "2px",
                        }}
                      >
                        {b.hasU && (
                          <>
                            {" "}
                            <div
                              style={{
                                height: b.hu,
                                border: "1px dashed var(--warn)",
                                borderRadius: "3px",
                              }}
                            ></div>{" "}
                          </>
                        )}
                        <div
                          style={{
                            height: b.hk,
                            background:
                              "linear-gradient(180deg,var(--accent2),var(--accent))",
                            borderRadius: "3px",
                          }}
                        ></div>
                      </div>
                    </React.Fragment>
                  ))}
                </div>
                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns: v.barColumns,
                    gap: "8px",
                  }}
                >
                  {v.bars.map((b: any, i28: number) => (
                    <React.Fragment key={b.id ?? b.name ?? i28}>
                      <span
                        style={{
                          fontSize: "11px",
                          color: "var(--ink3)",
                          textAlign: "center",
                        }}
                      >
                        {b.d}
                      </span>
                    </React.Fragment>
                  ))}
                </div>
                <span style={{ fontSize: "11px", color: "var(--ink3)" }}>
                  <span data-l="zh">{"虚线为用量未知的请求，不按 0 计入"}</span>
                  <span data-l="en">
                    {"Dashed: requests with unknown usage, never counted as 0"}
                  </span>
                </span>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"按 Key"}</span>
                  <span data-l="en">{"By key"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    {v.keyUse.map((u: any, i29: number) => (
                      <React.Fragment key={u.id ?? u.name ?? i29}>
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "14px",
                            minHeight: "46px",
                            padding: "8px 16px",
                            borderTop: "1px solid var(--line2)",
                            fontSize: "13px",
                            fontVariantNumeric: "tabular-nums",
                          }}
                          className="v13-hover"
                        >
                          <span style={{ flex: "1", fontWeight: "500" }}>
                            {u.key}
                          </span>
                          <span
                            style={{
                              width: "120px",
                              height: "4px",
                              borderRadius: "99px",
                              background: "var(--sunk)",
                              overflow: "hidden",
                            }}
                          >
                            <span
                              style={{
                                display: "block",
                                width: u.pct,
                                height: "100%",
                                background:
                                  "linear-gradient(90deg,var(--accent),var(--accent2))",
                              }}
                            ></span>
                          </span>
                          <span style={{ width: "56px", textAlign: "right" }}>
                            {u.tok}
                          </span>
                          <span
                            style={{
                              width: "64px",
                              textAlign: "right",
                              color: "var(--ink2)",
                            }}
                          >
                            {u.req}
                            {" req"}
                          </span>
                          <span
                            style={{
                              width: "50px",
                              textAlign: "right",
                              color: "var(--warnT)",
                            }}
                          >
                            {u.unk}
                            {" ?"}
                          </span>
                          <span
                            style={{
                              width: "90px",
                              textAlign: "right",
                              color: "var(--ink2)",
                            }}
                          >
                            {u.cost}
                          </span>
                        </div>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"预算"}</span>
                  <span data-l="en">{"Budgets"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    {v.budgets.map((b: any, i30: number) => (
                      <React.Fragment key={b.id ?? b.name ?? i30}>
                        <div
                          style={{
                            display: "flex",
                            flexDirection: "column",
                            gap: "6px",
                            padding: "12px 16px",
                            borderTop: "1px solid var(--line2)",
                          }}
                        >
                          <div
                            style={{
                              display: "flex",
                              justifyContent: "space-between",
                              gap: "12px",
                              fontSize: "13px",
                            }}
                          >
                            <span style={{ fontWeight: "500" }}>
                              <span data-l="zh">{b.zh}</span>
                              <span data-l="en">{b.en}</span>
                              <span
                                style={{
                                  fontWeight: "400",
                                  fontSize: "12px",
                                  color: "var(--ink3)",
                                }}
                              >
                                <span data-l="zh">{b.modeZh}</span>
                                <span data-l="en">{b.modeEn}</span>
                              </span>
                            </span>
                            <span
                              style={{ fontVariantNumeric: "tabular-nums" }}
                            >
                              {b.settled}{" "}
                              <span style={{ color: "var(--ink3)" }}>
                                {"/ "}
                                {b.limit}
                              </span>
                            </span>
                          </div>
                          <span
                            style={{
                              height: "4px",
                              borderRadius: "99px",
                              background: "var(--sunk)",
                              overflow: "hidden",
                            }}
                          >
                            <span
                              style={{
                                display: "block",
                                width: b.w,
                                height: "100%",
                                background:
                                  "linear-gradient(90deg,var(--accent),var(--accent2))",
                              }}
                            ></span>
                          </span>
                          <span
                            style={{ fontSize: "12px", color: "var(--ink2)" }}
                          >
                            <span data-l="zh">{"预留"}</span>
                            <span data-l="en">{"Reserved"}</span> {b.reserved}
                            {" · "}
                            <span style={{ color: "var(--warnT)" }}>
                              <span data-l="zh">{"待确认"}</span>
                              <span data-l="en">{"Pending"}</span> {b.pending}
                            </span>
                          </span>
                        </div>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"账号额度"}</span>
                  <span data-l="en">{"Account quota"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    {v.quota.map((q: any, i31: number) => (
                      <React.Fragment key={q.id ?? q.name ?? i31}>
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "14px",
                            minHeight: "52px",
                            padding: "8px 16px",
                            borderTop: "1px solid var(--line2)",
                          }}
                          className="v13-hover"
                        >
                          <div
                            style={{
                              flex: "1",
                              display: "flex",
                              flexDirection: "column",
                              minWidth: "0",
                              lineHeight: "1.4",
                            }}
                          >
                            <span
                              style={{ fontSize: "13px", fontWeight: "500" }}
                            >
                              {q.acct}{" "}
                              <span
                                style={{
                                  fontWeight: "400",
                                  color: "var(--ink2)",
                                }}
                              >
                                <span data-l="zh">{q.winZh}</span>
                                <span data-l="en">{q.winEn}</span>
                              </span>
                            </span>
                            <span
                              style={{ fontSize: "12px", color: "var(--ink3)" }}
                            >
                              <span data-l="zh">{q.subZh}</span>
                              <span data-l="en">{q.subEn}</span>
                            </span>
                          </div>
                          {q.has && (
                            <>
                              {" "}
                              <span
                                style={{
                                  width: "120px",
                                  height: "4px",
                                  borderRadius: "99px",
                                  background: "var(--sunk)",
                                  overflow: "hidden",
                                }}
                              >
                                <span
                                  style={{
                                    display: "block",
                                    width: q.w,
                                    height: "100%",
                                    background:
                                      "linear-gradient(90deg,var(--accent),var(--accent2))",
                                  }}
                                ></span>
                              </span>{" "}
                            </>
                          )}
                          <span
                            style={{
                              width: "84px",
                              textAlign: "right",
                              fontSize: "12px",
                              color: "var(--ink2)",
                            }}
                          >
                            <span data-l="zh">{q.valZh}</span>
                            <span data-l="en">{q.valEn}</span>
                          </span>
                        </div>
                      </React.Fragment>
                    ))}
                  </div>
                </div>
              </section>
              {v.emptyStates["07"]}
            </div>
          </main>
        </>
      )}
      {v.pg.settings && (
        <>
          <main
            data-screen-label="08 Settings"
            style={{ padding: "116px 24px 120px" }}
            className="v13-page"
          >
            <div
              style={{
                maxWidth: "640px",
                margin: "0 auto",
                display: "flex",
                flexDirection: "column",
                gap: "28px",
              }}
            >
              <header
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "flex-end",
                  gap: "16px",
                  flexWrap: "wrap",
                  marginBottom: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "8px",
                      fontFamily: "var(--mono)",
                      fontSize: "10.5px",
                      letterSpacing: "0.16em",
                      color: "var(--accent)",
                      textTransform: "uppercase",
                    }}
                  >
                    <span
                      style={{
                        width: "18px",
                        height: "1px",
                        background:
                          "linear-gradient(90deg,var(--accent),transparent)",
                      }}
                    ></span>
                    {"Cove / 08"}
                  </span>
                  <h1
                    style={{
                      margin: "0",
                      fontSize: "36px",
                      fontWeight: "600",
                      letterSpacing: "-0.045em",
                      lineHeight: "1.08",
                    }}
                  >
                    <span data-l="zh">{"设置"}</span>
                    <span data-l="en">{"Settings"}</span>
                  </h1>
                  <p
                    style={{
                      margin: "0",
                      fontSize: "13.5px",
                      color: "var(--ink2)",
                      textWrap: "pretty",
                    }}
                  >
                    <span data-l="zh">
                      {"启动配置修改后需重启，运行配置立即生效"}
                    </span>
                    <span data-l="en">
                      {
                        "Startup settings need a restart; runtime settings apply now"
                      }
                    </span>
                  </p>
                </div>
                <button
                  className="icon-button"
                  title="管理 / Manage"
                  aria-label="管理当前页面"
                  onClick={v.managePage}
                  type="button"
                >
                  <span data-i="">{"tune"}</span>
                </button>
              </header>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"服务 · 修改需重启"}</span>
                  <span data-l="en">{"Service · restart required"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "44px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span>
                        <span data-l="zh">{"监听地址"}</span>
                        <span data-l="en">{"Listen address"}</span>
                      </span>
                      <span
                        style={{
                          fontFamily: "var(--mono)",
                          fontSize: "12px",
                          color: "var(--ink2)",
                        }}
                      >
                        {v.listen}
                      </span>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "44px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span>
                        <span data-l="zh">{"数据目录"}</span>
                        <span data-l="en">{"Data directory"}</span>
                      </span>
                      <span
                        style={{
                          fontFamily: "var(--mono)",
                          fontSize: "12px",
                          color: "var(--ink2)",
                        }}
                      >
                        {v.dataDir}
                      </span>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "44px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span>
                        <span data-l="zh">{"全局并发"}</span>
                        <span data-l="en">{"Global concurrency"}</span>
                      </span>
                      <span
                        style={{
                          fontFamily: "var(--mono)",
                          fontSize: "12px",
                          color: "var(--ink2)",
                        }}
                      >
                        {v.maxConcurrent}
                      </span>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "44px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span>
                        <span data-l="zh">{"超时"}</span>
                        <span data-l="en">{"Timeouts"}</span>
                        <span
                          style={{ color: "var(--ink3)", fontSize: "12px" }}
                        >
                          {"header · idle · total"}
                        </span>
                      </span>
                      <span
                        style={{
                          fontFamily: "var(--mono)",
                          fontSize: "12px",
                          color: "var(--ink2)",
                        }}
                      >
                        {v.timeouts}
                      </span>
                    </div>
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"隐私"}</span>
                  <span data-l="en">{"Privacy"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "44px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span>
                        <span data-l="zh">{"请求记录保留"}</span>
                        <span data-l="en">{"Request retention"}</span>
                      </span>
                      <select
                        style={{
                          height: "26px",
                          padding: "0 6px",
                          border: "1px solid var(--line)",
                          borderRadius: "6px",
                          background: "var(--panel)",
                          fontSize: "12px",
                        }}
                        value={v.retentionDays}
                        onChange={v.onRetention}
                      >
                        <option value="7">{"7 d"}</option>
                        <option value="30">{"30 d"}</option>
                        <option value="90">{"90 d"}</option>
                      </select>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "44px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span
                        style={{
                          display: "flex",
                          flexDirection: "column",
                          lineHeight: "1.4",
                        }}
                      >
                        <span>
                          <span data-l="zh">{"保存提示词与响应"}</span>
                          <span data-l="en">{"Store prompts & outputs"}</span>
                        </span>
                        <span
                          style={{ fontSize: "12px", color: "var(--ink3)" }}
                        >
                          <span data-l="zh">{"诊断与导出永不包含正文"}</span>
                          <span data-l="en">
                            {"Never included in diagnostics or exports"}
                          </span>
                        </span>
                      </span>
                      <span
                        style={{
                          position: "relative",
                          width: "34px",
                          height: "20px",
                          borderRadius: "99px",
                          background: "var(--line)",
                        }}
                      >
                        <span
                          style={{
                            position: "absolute",
                            top: "2px",
                            left: "2px",
                            width: "16px",
                            height: "16px",
                            borderRadius: "50%",
                            background: "oklch(1 0 0)",
                            boxShadow: "0 1px 2px oklch(0 0 0 / 0.25)",
                          }}
                        ></span>
                      </span>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "44px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span>
                        <span data-l="zh">{"诊断包"}</span>
                        <span data-l="en">{"Diagnostics bundle"}</span>
                      </span>
                      <button
                        style={{
                          height: "26px",
                          padding: "0 10px",
                          border: "1px solid var(--line)",
                          borderRadius: "6px",
                          background: "var(--panel)",
                          fontSize: "12px",
                          cursor: "pointer",
                        }}
                        onClick={v.manageSettings}
                        type="button"
                      >
                        <span data-l="zh">{"预览并导出"}</span>
                        <span data-l="en">{"Preview & export"}</span>
                      </button>
                    </div>
                  </div>
                </div>
              </section>
              <section
                style={{ display: "flex", flexDirection: "column", gap: "8px" }}
              >
                <div
                  style={{
                    padding: "0 4px",
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    fontWeight: "500",
                    color: "var(--ink3)",
                    letterSpacing: "0.14em",
                    textTransform: "uppercase",
                  }}
                >
                  <span data-l="zh">{"备份与恢复"}</span>
                  <span data-l="en">{"Backup & restore"}</span>
                </div>
                <div
                  style={{
                    background: "var(--glass)",
                    border: "1px solid var(--glassBd)",
                    borderRadius: "18px",
                    boxShadow: "var(--sh)",
                    backdropFilter: "blur(22px) saturate(1.5)",
                    WebkitBackdropFilter: "blur(22px) saturate(1.5)",
                    overflow: "hidden",
                  }}
                >
                  <div style={{ marginTop: "-1px" }}>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "52px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span
                        style={{
                          display: "flex",
                          flexDirection: "column",
                          lineHeight: "1.4",
                        }}
                      >
                        <span>
                          <span data-l="zh">{"元数据备份"}</span>
                          <span data-l="en">{"Metadata backup"}</span>
                        </span>
                        <span
                          style={{ fontSize: "12px", color: "var(--ink3)" }}
                        >
                          <span data-l="zh">
                            {"不含凭据，恢复后需重新登录"}
                          </span>
                          <span data-l="en">
                            {"No credentials; re-auth after restore"}
                          </span>
                        </span>
                      </span>
                      <button
                        style={{
                          height: "26px",
                          padding: "0 10px",
                          border: "1px solid var(--line)",
                          borderRadius: "6px",
                          background: "var(--panel)",
                          fontSize: "12px",
                          cursor: "pointer",
                        }}
                        onClick={v.manageOperations}
                        type="button"
                      >
                        <span data-l="zh">{"生成"}</span>
                        <span data-l="en">{"Create"}</span>
                      </button>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "52px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span
                        style={{
                          display: "flex",
                          flexDirection: "column",
                          lineHeight: "1.4",
                        }}
                      >
                        <span>
                          <span data-l="zh">{"完整加密备份"}</span>
                          <span data-l="en">{"Full encrypted backup"}</span>
                        </span>
                        <span
                          style={{ fontSize: "12px", color: "var(--ink3)" }}
                        >
                          <span data-l="zh">
                            {"age 加密，口令丢失无法找回"}
                          </span>
                          <span data-l="en">
                            {"age-encrypted; lost passphrase is unrecoverable"}
                          </span>
                        </span>
                      </span>
                      <button
                        style={{
                          height: "26px",
                          padding: "0 10px",
                          border: "1px solid var(--line)",
                          borderRadius: "6px",
                          background: "var(--panel)",
                          fontSize: "12px",
                          cursor: "pointer",
                        }}
                        onClick={v.manageOperations}
                        type="button"
                      >
                        <span data-l="zh">{"设置口令"}</span>
                        <span data-l="en">{"Set passphrase"}</span>
                      </button>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        justifyContent: "space-between",
                        alignItems: "center",
                        gap: "12px",
                        minHeight: "52px",
                        padding: "8px 16px",
                        borderTop: "1px solid var(--line2)",
                        fontSize: "13px",
                      }}
                    >
                      <span
                        style={{
                          display: "flex",
                          flexDirection: "column",
                          lineHeight: "1.4",
                        }}
                      >
                        <span>
                          <span data-l="zh">{"从备份恢复"}</span>
                          <span data-l="en">{"Restore from backup"}</span>
                        </span>
                        <span
                          style={{ fontSize: "12px", color: "var(--ink3)" }}
                        >
                          <span data-l="zh">
                            {"先预览，写入新目录验证后再切换"}
                          </span>
                          <span data-l="en">
                            {"Preview, restore to a new dir, verify, switch"}
                          </span>
                        </span>
                      </span>
                      <button
                        onClick={v.openBackup}
                        style={{
                          height: "26px",
                          padding: "0 10px",
                          border: "0",
                          borderRadius: "6px",
                          background:
                            "linear-gradient(135deg,var(--accent),var(--accent2))",
                          color: "var(--accentInk)",
                          fontSize: "12px",
                          fontWeight: "600",
                          cursor: "pointer",
                        }}
                        type="button"
                      >
                        <span data-l="zh">{"选择文件"}</span>
                        <span data-l="en">{"Choose file"}</span>
                      </button>
                    </div>
                  </div>
                </div>
              </section>
              <span
                style={{
                  textAlign: "center",
                  fontSize: "12px",
                  color: "var(--ink3)",
                }}
              >
                {v.versionLabel}
              </span>
              {v.emptyStates["08"]}
            </div>
          </main>
        </>
      )}
      {v.xPal && (
        <>
          <div
            onClick={v.xClosePal}
            data-screen-label="Command palette"
            style={{
              position: "fixed",
              inset: "0",
              zIndex: "60",
              background: "var(--scrim)",
              backdropFilter: "blur(8px)",
              WebkitBackdropFilter: "blur(8px)",
              display: "flex",
              justifyContent: "center",
              alignItems: "flex-start",
              padding: "14vh 16px 0",
            }}
            role="dialog"
            aria-modal="true"
            aria-label="Command palette"
          >
            <div
              onClick={v.stop}
              style={{
                width: "100%",
                maxWidth: "520px",
                background: "var(--panel)",
                borderRadius: "16px",
                boxShadow: "var(--shL)",
                display: "flex",
                flexDirection: "column",
                overflow: "hidden",
              }}
            >
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "10px",
                  padding: "12px 14px",
                  borderBottom: "1px solid var(--line2)",
                }}
              >
                <span
                  data-i=""
                  style={{ fontSize: "19px", color: "var(--ink3)" }}
                >
                  {"search"}
                </span>
                <input
                  ref={v.xPalRef}
                  value={v.xPalQ}
                  onChange={v.xOnPalQ}
                  onKeyDown={v.xOnPalKey}
                  placeholder="跳转、暂停账号、复制地址…"
                  style={{
                    flex: "1",
                    minWidth: "0",
                    border: "0",
                    outline: "none",
                    background: "transparent",
                    fontSize: "14px",
                  }}
                />
                <span
                  style={{
                    fontFamily: "var(--mono)",
                    fontSize: "10.5px",
                    color: "var(--ink3)",
                  }}
                >
                  {"esc"}
                </span>
              </div>
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  maxHeight: "340px",
                  overflowY: "auto",
                  padding: "6px",
                }}
              >
                {v.xCmds.map((c: any, i32: number) => (
                  <React.Fragment key={c.id ?? c.name ?? i32}>
                    <button
                      onClick={c.run}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "10px",
                        height: "36px",
                        padding: "0 10px",
                        border: "0",
                        borderRadius: "8px",
                        background: c.bg,
                        textAlign: "left",
                        fontSize: "13px",
                        cursor: "pointer",
                      }}
                      className="v13-hover"
                      type="button"
                    >
                      <span
                        data-i=""
                        style={{ fontSize: "18px", color: "var(--ink2)" }}
                      >
                        {c.icon}
                      </span>
                      <span style={{ flex: "1" }}>
                        <span data-l="zh">{c.zh}</span>
                        <span data-l="en">{c.en}</span>
                      </span>
                    </button>
                  </React.Fragment>
                ))}
                {v.xNoCmds && (
                  <>
                    {" "}
                    <div
                      style={{
                        padding: "14px 10px",
                        fontSize: "13px",
                        color: "var(--ink3)",
                      }}
                    >
                      <span data-l="zh">{"没有匹配的命令"}</span>
                      <span data-l="en">{"No matching commands"}</span>
                    </div>{" "}
                  </>
                )}
              </div>
            </div>
          </div>
        </>
      )}
      {v.mPicker && (
        <>
          <div
            onClick={v.close}
            data-screen-label="Picker · Model"
            style={{
              position: "fixed",
              inset: "0",
              zIndex: "50",
              background: "var(--scrim)",
              backdropFilter: "blur(8px)",
              WebkitBackdropFilter: "blur(8px)",
              display: "flex",
              justifyContent: "center",
              alignItems: "flex-start",
              padding: "96px 16px",
            }}
            role="dialog"
            aria-modal="true"
            aria-label="Picker \u00b7 Model"
          >
            <div
              onClick={v.stop}
              style={{
                width: "100%",
                maxWidth: "440px",
                maxHeight: "calc(100vh - 160px)",
                display: "flex",
                flexDirection: "column",
                background: "var(--panel)",
                borderRadius: "16px",
                boxShadow: "var(--shL)",
                overflow: "hidden",
              }}
            >
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "10px",
                  padding: "12px 14px",
                  borderBottom: "1px solid var(--line2)",
                }}
              >
                <span
                  role="img"
                  data-mono={v.picker.mono}
                  style={{
                    display: "inline-block",
                    flex: "none",
                    background:
                      "url(" + v.picker.logo + ") center/contain no-repeat",
                    width: "18px",
                    height: "18px",
                  }}
                ></span>
                <input
                  autoFocus={true}
                  value={v.pq}
                  onChange={v.onPq}
                  placeholder="Search models…"
                  style={{
                    flex: "1",
                    minWidth: "0",
                    border: "0",
                    outline: "none",
                    background: "transparent",
                    fontSize: "14px",
                  }}
                />
                <span
                  style={{
                    fontSize: "11px",
                    color: "var(--ink3)",
                    padding: "2px 6px",
                    border: "1px solid var(--line)",
                    borderRadius: "4px",
                  }}
                >
                  {"esc"}
                </span>
              </div>
              <div style={{ overflowY: "auto", padding: "6px" }}>
                {v.hasPAliases && (
                  <>
                    <div
                      style={{
                        padding: "8px 10px 4px",
                        fontSize: "11px",
                        fontWeight: "600",
                        color: "var(--ink3)",
                      }}
                    >
                      <span data-l="zh">{"公开名 · 路由"}</span>
                      <span data-l="en">{"Public names · routes"}</span>
                    </div>
                    {v.pAliases.map((a: any, i33: number) => (
                      <React.Fragment key={a.id ?? a.name ?? i33}>
                        <button
                          onClick={a.onClick}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "10px",
                            width: "100%",
                            height: "34px",
                            padding: "0 10px",
                            border: "0",
                            borderRadius: "7px",
                            background: "transparent",
                            textAlign: "left",
                            cursor: "pointer",
                          }}
                          className="v13-hover"
                          type="button"
                        >
                          <span
                            style={{
                              flex: "1",
                              fontFamily: "var(--mono)",
                              fontSize: "13px",
                              color: "var(--accent)",
                              fontWeight: "500",
                            }}
                          >
                            {a.pub}
                          </span>
                          <span
                            style={{
                              fontSize: "11px",
                              color: "var(--ink3)",
                              fontFamily: "var(--mono)",
                            }}
                          >
                            {a.target}
                          </span>
                          <span
                            data-i=""
                            style={{
                              width: "18px",
                              fontSize: "17px",
                              color: "var(--accent)",
                            }}
                          >
                            {a.mark}
                          </span>
                        </button>
                      </React.Fragment>
                    ))}
                  </>
                )}
                {v.pGroups.map((g: any, i34: number) => (
                  <React.Fragment key={g.id ?? g.name ?? i34}>
                    <div
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: "6px",
                        padding: "10px 10px 4px",
                        fontSize: "11px",
                        fontWeight: "600",
                        color: "var(--ink3)",
                      }}
                    >
                      <span
                        role="img"
                        data-mono={g.mono}
                        style={{
                          display: "inline-block",
                          flex: "none",
                          background:
                            "url(" + g.logo + ") center/contain no-repeat",
                          width: "12px",
                          height: "12px",
                        }}
                      ></span>
                      {g.name}
                    </div>
                    {g.items.map((m: any, i35: number) => (
                      <React.Fragment key={m.id ?? m.name ?? i35}>
                        <button
                          onClick={m.onClick}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "10px",
                            width: "100%",
                            height: "34px",
                            padding: "0 10px",
                            border: "0",
                            borderRadius: "7px",
                            background: "transparent",
                            textAlign: "left",
                            cursor: "pointer",
                          }}
                          className="v13-hover"
                          type="button"
                        >
                          <span
                            style={{
                              flex: "1",
                              fontFamily: "var(--mono)",
                              fontSize: "13px",
                            }}
                          >
                            {m.m}
                          </span>
                          <span
                            style={{ fontSize: "11px", color: "var(--ink3)" }}
                          >
                            {m.ctx}
                          </span>
                          <span
                            data-i=""
                            style={{
                              width: "18px",
                              fontSize: "17px",
                              color: "var(--accent)",
                            }}
                          >
                            {m.mark}
                          </span>
                        </button>
                      </React.Fragment>
                    ))}
                  </React.Fragment>
                ))}
              </div>
              <div
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  gap: "10px",
                  padding: "8px 14px",
                  borderTop: "1px solid var(--line2)",
                  fontSize: "11px",
                  color: "var(--ink3)",
                }}
              >
                <span>
                  {v.picker.name}
                  {" · "}
                  <span data-l="zh">{"当前"}</span>
                  <span data-l="en">{"current"}</span>
                  <span style={{ fontFamily: "var(--mono)" }}>
                    {v.picker.cur}
                  </span>
                </span>
                <span>
                  <span data-l="zh">{"写入前会预览差异"}</span>
                  <span data-l="en">{"Diff shown before writing"}</span>
                </span>
              </div>
            </div>
          </div>
        </>
      )}
      {v.mAdd && (
        <>
          <div
            onClick={v.close}
            data-screen-label="Sheet · Add source"
            role="dialog"
            aria-modal="true"
            aria-label="添加来源"
            style={{
              position: "fixed",
              inset: "0",
              zIndex: "50",
              background: "var(--scrim)",
              backdropFilter: "blur(8px)",
              WebkitBackdropFilter: "blur(8px)",
              display: "flex",
              justifyContent: "center",
              alignItems: "flex-start",
              padding: "80px 16px",
              overflowY: "auto",
            }}
          >
            <div
              onClick={v.stop}
              style={{
                width: "100%",
                maxWidth: "520px",
                background: "var(--panel)",
                borderRadius: "16px",
                boxShadow: "var(--shL)",
                display: "flex",
                flexDirection: "column",
              }}
            >
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "8px",
                  padding: "14px 16px",
                  borderBottom: "1px solid var(--line2)",
                }}
              >
                <span
                  style={{ flex: "1", fontSize: "14px", fontWeight: "600" }}
                >
                  <span data-l="zh">{"添加来源"}</span>
                  <span data-l="en">{"Add source"}</span>
                </span>
                <button
                  onClick={v.close}
                  style={{
                    width: "26px",
                    height: "26px",
                    border: "0",
                    borderRadius: "6px",
                    background: "transparent",
                    cursor: "pointer",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                  }}
                  className="v13-hover"
                  type="button"
                >
                  <span
                    data-i=""
                    style={{ fontSize: "18px", color: "var(--ink2)" }}
                  >
                    {"close"}
                  </span>
                </button>
              </div>
              {v.addGrid && (
                <>
                  <div
                    style={{
                      display: "grid",
                      gridTemplateColumns: "repeat(3,minmax(0,1fr))",
                      gap: "8px",
                      padding: "16px",
                    }}
                  >
                    {v.presets.map((p: any, i36: number) => (
                      <React.Fragment key={p.id ?? p.name ?? i36}>
                        <button
                          onClick={p.onClick}
                          style={{
                            display: "flex",
                            flexDirection: "column",
                            alignItems: "center",
                            gap: "8px",
                            padding: "16px 8px 12px",
                            border: "1px solid var(--line)",
                            borderRadius: "10px",
                            background: "var(--panel)",
                            cursor: "pointer",
                          }}
                          className="v13-hover"
                          type="button"
                        >
                          <span
                            style={{
                              flex: "none",
                              width: "44px",
                              height: "44px",
                              borderRadius: "11px",
                              background: "var(--tile)",
                              boxShadow: "var(--tileSh)",
                              display: "flex",
                              alignItems: "center",
                              justifyContent: "center",
                            }}
                          >
                            <span
                              role="img"
                              data-mono={p.mono}
                              style={{
                                display: "inline-block",
                                width: "26px",
                                height: "26px",
                                background:
                                  "url(" +
                                  p.logo +
                                  ") center/contain no-repeat",
                              }}
                            ></span>
                          </span>
                          <span
                            style={{
                              display: "flex",
                              flexDirection: "column",
                              alignItems: "center",
                              lineHeight: "1.35",
                            }}
                          >
                            <span
                              style={{ fontSize: "13px", fontWeight: "500" }}
                            >
                              <span data-l="zh">{p.zh}</span>
                              <span data-l="en">{p.en}</span>
                            </span>
                            <span
                              style={{ fontSize: "11px", color: "var(--ink3)" }}
                            >
                              <span data-l="zh">{p.subZh}</span>
                              <span data-l="en">{p.subEn}</span>
                            </span>
                          </span>
                        </button>
                      </React.Fragment>
                    ))}
                  </div>
                  <div style={{ padding: "0 16px 16px" }}>
                    <button
                      style={{
                        width: "100%",
                        height: "34px",
                        border: "1px dashed var(--line)",
                        borderRadius: "8px",
                        background: "transparent",
                        fontSize: "12px",
                        color: "var(--ink2)",
                        cursor: "pointer",
                      }}
                      onClick={v.customAdd}
                      className="v13-hover"
                      type="button"
                    >
                      <span data-l="zh">
                        {"自定义 OpenAI / Anthropic 兼容地址"}
                      </span>
                      <span data-l="en">
                        {"Custom OpenAI / Anthropic-compatible URL"}
                      </span>
                    </button>
                  </div>
                </>
              )}
            </div>
          </div>
        </>
      )}
      {v.mReq && (
        <>
          <div
            onClick={v.close}
            data-screen-label="Drawer · Request"
            role="dialog"
            aria-modal="true"
            aria-label="请求详情"
            style={{
              position: "fixed",
              inset: "0",
              zIndex: "50",
              background: "var(--scrim)",
              backdropFilter: "blur(8px)",
              WebkitBackdropFilter: "blur(8px)",
            }}
          >
            <aside
              onClick={v.stop}
              style={{
                position: "absolute",
                top: "0",
                right: "0",
                bottom: "0",
                width: "min(480px,100%)",
                background: "var(--panel)",
                borderLeft: "1px solid var(--line)",
                boxShadow: "var(--shL)",
                overflowY: "auto",
                display: "flex",
                flexDirection: "column",
              }}
            >
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "10px",
                  padding: "14px 16px",
                  borderBottom: "1px solid var(--line2)",
                }}
              >
                <span
                  style={{
                    width: "6px",
                    height: "6px",
                    borderRadius: "50%",
                    background: v.reqD.dot,
                    boxShadow: "0 0 10px " + v.reqD.dot + "",
                  }}
                ></span>
                <span
                  style={{
                    flex: "1",
                    fontFamily: "var(--mono)",
                    fontSize: "13px",
                    fontWeight: "600",
                  }}
                >
                  {v.reqD.id}
                </span>
                <span style={{ fontSize: "12px", color: v.reqD.stC }}>
                  <span data-l="zh">{v.reqD.stZh}</span>
                  <span data-l="en">{v.reqD.stEn}</span>
                </span>
                <button
                  onClick={v.close}
                  style={{
                    width: "26px",
                    height: "26px",
                    border: "0",
                    borderRadius: "6px",
                    background: "transparent",
                    cursor: "pointer",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                  }}
                  className="v13-hover"
                  type="button"
                >
                  <span
                    data-i=""
                    style={{ fontSize: "18px", color: "var(--ink2)" }}
                  >
                    {"close"}
                  </span>
                </button>
              </div>
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  padding: "4px 16px",
                  fontSize: "12px",
                }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    padding: "7px 0",
                    borderBottom: "1px solid var(--line2)",
                  }}
                >
                  <span style={{ color: "var(--ink3)" }}>
                    {"key · protocol"}
                  </span>
                  <span>
                    {v.reqD.key}
                    {" · "}
                    {v.reqD.proto}
                  </span>
                </div>
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    padding: "7px 0",
                    borderBottom: "1px solid var(--line2)",
                  }}
                >
                  <span style={{ color: "var(--ink3)" }}>
                    {"requested → sent"}
                  </span>
                  <span style={{ fontFamily: "var(--mono)" }}>
                    <span style={{ color: "var(--accent)" }}>
                      {v.reqD.model}
                    </span>
                    {" → "}
                    {v.reqD.sent}
                  </span>
                </div>
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    padding: "7px 0",
                    borderBottom: "1px solid var(--line2)",
                  }}
                >
                  <span style={{ color: "var(--ink3)" }}>
                    {"delivery · observation"}
                  </span>
                  <span>
                    {v.reqD.deliv}
                    {" · "}
                    {v.reqD.obs}
                  </span>
                </div>
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    padding: "7px 0",
                    borderBottom: "1px solid var(--line2)",
                  }}
                >
                  <span style={{ color: "var(--ink3)" }}>{"TTFT · total"}</span>
                  <span>
                    {v.reqD.ttft}
                    {" · "}
                    {v.reqD.dur}
                  </span>
                </div>
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    padding: "7px 0",
                  }}
                >
                  <span style={{ color: "var(--ink3)" }}>
                    {"tokens · cost"}
                  </span>
                  <span>
                    {v.reqD.tok}
                    {" · "}
                    {v.reqD.cost}
                  </span>
                </div>
              </div>
              <div
                style={{
                  padding: "14px 16px 6px",
                  fontFamily: "var(--mono)",
                  fontSize: "10.5px",
                  fontWeight: "500",
                  color: "var(--ink3)",
                  letterSpacing: "0.14em",
                  textTransform: "uppercase",
                }}
              >
                <span data-l="zh">{"尝试"}</span>
                <span data-l="en">{"Attempts"}</span>
              </div>
              {v.reqD.hasAtt && (
                <>
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      padding: "0 16px 20px",
                    }}
                  >
                    {v.reqD.attempts.map((a: any, i37: number) => (
                      <React.Fragment key={a.id ?? a.name ?? i37}>
                        <div
                          style={{
                            display: "grid",
                            gridTemplateColumns: "20px minmax(0,1fr)",
                            gap: "10px",
                          }}
                        >
                          <div
                            style={{
                              display: "flex",
                              flexDirection: "column",
                              alignItems: "center",
                              paddingTop: "4px",
                            }}
                          >
                            <span
                              style={{
                                width: "10px",
                                height: "10px",
                                borderRadius: "50%",
                                border: "2px solid " + a.dot + "",
                              }}
                            ></span>
                            <span
                              style={{
                                flex: "1",
                                width: "1px",
                                background: "var(--line)",
                                visibility: a.lineVis,
                              }}
                            ></span>
                          </div>
                          <div
                            style={{
                              display: "flex",
                              flexDirection: "column",
                              gap: "3px",
                              paddingBottom: "16px",
                              lineHeight: "1.45",
                            }}
                          >
                            <div
                              style={{
                                display: "flex",
                                alignItems: "center",
                                gap: "6px",
                              }}
                            >
                              <span
                                role="img"
                                data-mono={a.mono}
                                style={{
                                  display: "inline-block",
                                  flex: "none",
                                  background:
                                    "url(" +
                                    a.logo +
                                    ") center/contain no-repeat",
                                  width: "14px",
                                  height: "14px",
                                }}
                              ></span>
                              <span
                                style={{
                                  flex: "1",
                                  fontSize: "13px",
                                  fontWeight: "500",
                                }}
                              >
                                {a.src}{" "}
                                <span
                                  style={{
                                    fontWeight: "400",
                                    fontFamily: "var(--mono)",
                                    fontSize: "12px",
                                    color: "var(--ink2)",
                                  }}
                                >
                                  {a.m}
                                </span>
                              </span>
                              <span
                                style={{
                                  fontSize: "11px",
                                  color: "var(--ink3)",
                                }}
                              >
                                {a.dur}
                              </span>
                            </div>
                            <span
                              style={{
                                fontFamily: "var(--mono)",
                                fontSize: "12px",
                                color: a.c,
                              }}
                            >
                              {a.res}
                            </span>
                            <span
                              style={{
                                fontSize: "12px",
                                color: "var(--ink2)",
                                textWrap: "pretty",
                              }}
                            >
                              <span data-l="zh">{a.zh}</span>
                              <span data-l="en">{a.en}</span>
                            </span>
                            <span
                              style={{
                                fontFamily: "var(--mono)",
                                fontSize: "11px",
                                color: "var(--ink3)",
                              }}
                            >
                              {a.usage}
                            </span>
                          </div>
                        </div>
                      </React.Fragment>
                    ))}
                  </div>
                </>
              )}
              {v.reqD.noAtt && (
                <>
                  {" "}
                  <div
                    style={{
                      margin: "0 16px",
                      padding: "10px 12px",
                      borderRadius: "8px",
                      background: "var(--sunk)",
                      fontSize: "12px",
                      textWrap: "pretty",
                    }}
                  >
                    <span style={{ fontWeight: "500" }}>
                      <span data-l="zh">{"没有已记录的上游尝试"}</span>
                      <span data-l="en">{"No recorded upstream attempts"}</span>
                    </span>
                    <br />
                    <span style={{ color: "var(--ink2)" }}>
                      <span data-l="zh">{v.reqD.whyZh}</span>
                      <span data-l="en">{v.reqD.whyEn}</span>
                    </span>
                  </div>{" "}
                </>
              )}
              {v.requestExtra}
            </aside>
          </div>
        </>
      )}
    </>
  );
}
