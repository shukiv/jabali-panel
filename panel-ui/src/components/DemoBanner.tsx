// DemoBanner — fixed strip above every route when /info reports
// demo.enabled. Stays out of the DOM completely in production (the
// useServiceInfo hook returns `undefined` for `demo` when the backend
// omits the key, so the banner short-circuits before AntD renders).
//
// Render strategy: AntD Alert with a `closable={false}` ribbon at the
// top of the viewport. The CSS var `--jabali-demo-banner-h` is set on
// the document element so shell layouts can pad themselves down by
// reading it (e.g. `padding-top: var(--jabali-demo-banner-h, 0px)`).

import { Alert } from "antd";
import { useEffect } from "react";

import { useServiceInfo } from "../useServiceInfo";

const BANNER_HEIGHT_PX = 72;

export function DemoBanner() {
  const { data } = useServiceInfo();
  const demo = data?.demo;
  const enabled = !!demo?.enabled;

  useEffect(() => {
    const h = enabled ? `${BANNER_HEIGHT_PX}px` : "0px";
    document.documentElement.style.setProperty("--jabali-demo-banner-h", h);
    // AntD shells use position:fixed headers + 100vh siders. Injecting
    // a stylesheet that offsets every header/sider by the banner height
    // is the lightest fix — no shell components need to be aware of
    // demo mode. Cleanup on toggle removes the rule.
    const STYLE_ID = "jabali-demo-banner-shift";
    if (enabled) {
      let el = document.getElementById(STYLE_ID) as HTMLStyleElement | null;
      if (!el) {
        el = document.createElement("style");
        el.id = STYLE_ID;
        document.head.appendChild(el);
      }
      // Shift the entire viewport down by BANNER_HEIGHT so the AntD
      // shell (which uses 100vh + position:sticky/fixed) lays out
      // BELOW the banner. html padding works where body padding
      // doesn't because AntD's Layout reads viewport height directly.
      el.textContent = `
        html { padding-top: ${BANNER_HEIGHT_PX}px; box-sizing: border-box; }
        body, #root { min-height: calc(100vh - ${BANNER_HEIGHT_PX}px); }
        .ant-layout { min-height: calc(100vh - ${BANNER_HEIGHT_PX}px) !important; }
      `;
    } else {
      document.getElementById(STYLE_ID)?.remove();
    }
    return () => {
      document.getElementById(STYLE_ID)?.remove();
    };
  }, [enabled]);

  if (!enabled) return null;

  const message =
    demo?.banner ||
    "Demo mode — every change is disabled. Browse freely; nothing you do here persists.";

  return (
    <Alert
      type="warning"
      banner
      showIcon
      message={message}
      style={{
        position: "fixed",
        top: 0,
        left: 0,
        right: 0,
        zIndex: 2000,
        height: BANNER_HEIGHT_PX,
        display: "flex",
        alignItems: "center",
      }}
    />
  );
}
