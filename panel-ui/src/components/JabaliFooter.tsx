// JabaliFooter — brand footer rendered at the bottom of both shells.
//
// Left: logo + "Jabali Panel" + tagline.
// Right: source link, copyright, and current version tag.
//
// The version string is imported from panel-ui/package.json at build
// time so bumping the SPA version there propagates automatically.
import { useTranslation } from "react-i18next";
import { GithubOutlined } from "@icons";
import { Grid, Layout, Space, theme, Typography } from "antd";

import { useThemeMode } from "../theme/ThemeModeContext";
import pkg from "../../package.json";

// Canonical source-code URL. Pointing at the Gitea mirror for now; swap
// to a GitHub URL here if the project gets mirrored publicly.
const SOURCE_URL = "https://github.com/shukiv/jabali-panel";
const WEBSITE_URL = "https://jabali-panel.com/";

export function JabaliFooter() {
  const { t } = useTranslation();
  const { mode } = useThemeMode();
  const { token } = theme.useToken();
  const screens = Grid.useBreakpoint();
  // Inline row layout only on desktop (lg+). Phone + tablet stack
  // vertically and center so the brand block and version block don't
  // hug the left edge on narrow shells.
  const isWide = screens.lg !== false;
  // Tagline + AGPL link stay visible from sm upward (tablet still has
  // room); only true phone xs hides them.
  const showExtras = screens.sm !== false;
  const logoSrc =
    mode === "dark" ? "/images/jabali_logo_dark.svg" : "/images/jabali_logo.svg";
  // Match the Layout body color (gray-50 light / colorBgLayout dark) so
  // the footer blends seamlessly with the sidebar and content gutter.
  // Inline "transparent" wasn't enough — AntD's Footer ships a token-
  // backed bg that can paint colorBgContainer depending on algorithm.
  const footerBg = mode === "dark" ? token.colorBgLayout : "#f9fafb";

  return (
    <Layout.Footer
      style={{
        display: "flex",
        flexDirection: isWide ? "row" : "column",
        alignItems: "center",
        justifyContent: isWide ? "space-between" : "center",
        textAlign: isWide ? "left" : "center",
        gap: isWide ? 16 : 8,
        // AntD's Footer default padding is 24px 50px — the 24 top/bottom
        // leaves a visible gap above the logo on short list views. Tight
        // to 8px vertical; keep 24px horizontal on sm+ to match Content
        // padding, 12 on xs for the narrower content gutter.
        padding: isWide ? "8px 24px" : "8px 12px",
        background: footerBg,
      }}
    >
      <Space size={12}>
        <img
          src={logoSrc}
          alt="Jabali"
          style={{ height: 32, width: "auto", flexShrink: 0 }}
        />
        <div>
          <Typography.Text strong style={{ display: "block" }}>
            <a
              href={WEBSITE_URL}
              target="_blank"
              rel="noreferrer"
              style={{ color: "inherit" }}
            >
              Jabali Panel
            </a>
          </Typography.Text>
          {showExtras && (
            <Typography.Text type="secondary">
              Web Hosting Control Panel
            </Typography.Text>
          )}
        </div>
      </Space>

      <Space size={12}>
        <a
          href={SOURCE_URL}
          target="_blank"
          rel="noreferrer"
          aria-label={t("jabalifooter.source_code")}
          style={{ color: "inherit", display: "inline-flex", alignItems: "center" }}
        >
          <GithubOutlined style={{ fontSize: 18 }} />
        </a>
        {showExtras && (
          <>
            <Typography.Text type="secondary">·</Typography.Text>
            <Typography.Text type="secondary">
              <a
                href="https://www.gnu.org/licenses/agpl-3.0.html"
                target="_blank"
                rel="noreferrer"
                style={{ color: "inherit" }}
              >
                AGPL-3.0
              </a>
            </Typography.Text>
          </>
        )}
        <Typography.Text strong>v{pkg.version}</Typography.Text>
      </Space>
    </Layout.Footer>
  );
}
