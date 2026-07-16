import { BookOutlined } from "@icons";
import type { CSSProperties } from "react";
import { useThemeMode } from "../../../theme/ThemeModeContext";
import wordpressLogo from "../../../icons/brand/apps/wordpress.svg";
import mediawikiLogo from "../../../icons/brand/apps/mediawiki.svg";
import drupalLogo from "../../../icons/brand/apps/drupal.svg";
import joomlaLogo from "../../../icons/brand/apps/joomla.svg";
import phpbbLogo from "../../../icons/brand/apps/phpbb.svg";
import opencartLogo from "../../../icons/brand/apps/opencart.svg";
import prestashopLogo from "../../../icons/brand/apps/prestashop.svg";
import flarumLogo from "../../../icons/brand/apps/flarum.svg";
import itflowLogo from "../../../icons/brand/apps/itflow.svg";
import moodleLogo from "../../../icons/brand/apps/moodle.svg";
import dokuwikiLogo from "../../../icons/brand/apps/dokuwiki.svg";
// Theme-aware variants: "<name>-<theme>.svg" is the logo shown FOR that theme
// (a light-coloured mark for dark mode, a dark-coloured mark for light mode).
import prestashopDark from "../../../icons/brand/apps/prestashop-dark.svg";
import prestashopLight from "../../../icons/brand/apps/prestashop-light.svg";

interface CmsIconProps {
  appType: string | undefined;
  size?: number;
}

// Real brand logos for each native CMS app_type, keyed the same way the
// backend names them. These are the official marks (collected under
// icons/brand/apps/) — they replace the earlier monochrome react-icons
// glyphs, several of which were the wrong brand (e.g. the Wikipedia mark
// stood in for MediaWiki, a speech bubble for Flarum, a briefcase for ITFlow).
const APP_LOGOS: Record<string, string> = {
  wordpress: wordpressLogo,
  mediawiki: mediawikiLogo,
  drupal: drupalLogo,
  joomla: joomlaLogo,
  phpbb: phpbbLogo,
  opencart: opencartLogo,
  prestashop: prestashopLogo,
  flarum: flarumLogo,
  itflow: itflowLogo,
  moodle: moodleLogo,
  dokuwiki: dokuwikiLogo,
};

// Apps whose logo differs per theme. Keyed by app_type → { dark, light }
// where each value is the logo to show under that panel theme. Takes
// precedence over APP_LOGOS. Mirrors the marketing-site slider's themed set.
const APP_LOGOS_THEMED: Record<string, { dark: string; light: string }> = {
  prestashop: { dark: prestashopDark, light: prestashopLight },
};

// appDisplayName maps an app_type to its proper display name (for table badges
// where the icon alone isn't enough — GH #504). Falls back to title-case.
const APP_DISPLAY_NAMES: Record<string, string> = {
  wordpress: "WordPress", moodle: "Moodle", dokuwiki: "DokuWiki",
  mediawiki: "MediaWiki", drupal: "Drupal", joomla: "Joomla", phpbb: "phpBB",
  opencart: "OpenCart", prestashop: "PrestaShop", flarum: "Flarum",
  itflow: "ITFlow",
};
export function appDisplayName(appType?: string): string {
  if (!appType) return "WordPress";
  return APP_DISPLAY_NAMES[appType] || appType.charAt(0).toUpperCase() + appType.slice(1);
}

// CmsIcon renders the brand logo for an app_type. Unknown app_type falls
// back to the AntD BookOutlined generic icon.
export function CmsIcon({ appType, size = 18 }: CmsIconProps) {
  const { mode } = useThemeMode();
  const key = appType || "wordpress";
  const logo = APP_LOGOS_THEMED[key]?.[mode] ?? APP_LOGOS[key];

  if (logo) {
    return (
      <img
        src={logo}
        width={size}
        height={size}
        alt={appDisplayName(key)}
        style={{ width: size, height: size, flexShrink: 0, objectFit: "contain" }}
      />
    );
  }

  const style: CSSProperties = {
    fontSize: size,
    width: size,
    height: size,
    flexShrink: 0,
  };
  return <BookOutlined style={style} />;
}
