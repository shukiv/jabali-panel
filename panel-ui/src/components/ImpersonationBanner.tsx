// ImpersonationBanner.tsx — persistent "Viewing as <user> — Exit" strip shown
// at the top of the user shell while an admin is acting-as (GH #183, ADR-0128).
// Read once per render; enter/exit do a full page navigation, so the banner is
// always correct on load. Sticky + a bright amber so it stays visible and
// clearly distinguishable in dark mode while scrolling.
import { useEffect, useRef } from "react";
import { Alert, Button } from "antd";

import { getActAs, stopImpersonation } from "../impersonation";
import { ClampedText } from "./ClampedText";

export function ImpersonationBanner() {
  const actAs = getActAs();
  const ref = useRef<HTMLDivElement>(null);

  // Publish the banner height + a body class so global.css can offset modal /
  // drawer content below the always-on-top banner (GH #279), and the shell's
  // sticky sidebar and bars can stick below it (GH #2243). Measure live so
  // the offset tracks the real height (wraps on narrow screens).
  useEffect(() => {
    if (!actAs) return;
    const el = ref.current;
    const apply = () => {
      const h = el?.offsetHeight ?? 40;
      document.body.style.setProperty("--imp-banner-h", `${h}px`);
    };
    apply();
    document.body.classList.add("imp-active");
    const ro = el ? new ResizeObserver(apply) : null;
    if (el && ro) ro.observe(el);
    return () => {
      ro?.disconnect();
      document.body.classList.remove("imp-active");
      document.body.style.removeProperty("--imp-banner-h");
    };
  }, [actAs]);

  if (!actAs) return null;

  const exit = async () => {
    await stopImpersonation();
    window.location.assign("/jabali-admin/users");
  };

  return (
    <div ref={ref} style={{ position: "sticky", top: 0, zIndex: 1001 }}>
      <Alert
        type="warning"
        banner
        showIcon
        // Force a bright amber in both themes — the default dark-mode warning
        // banner is near-black and doesn't read as an alert.
        style={{ background: "#fa8c16", borderBottom: "1px solid #ffc53d" }}
        message={
          <ClampedText lines={2} style={{ color: "#1f1300", fontWeight: 600 }}>
            {`Viewing as ${actAs.username || actAs.id} — acting on this user's behalf. Every action is logged.`}
          </ClampedText>
        }
        action={
          <Button size="small" danger onClick={exit}>
            Exit
          </Button>
        }
      />
    </div>
  );
}
