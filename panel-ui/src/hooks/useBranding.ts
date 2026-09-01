// useBranding — public-read branding info (brand text + whether
// custom logos are uploaded). Shared across AdminLayout, UserLayout,
// and the login page; the endpoint is unauthenticated so pre-login
// rendering works.
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";

import { apiClient } from "../apiClient";

type BrandingInfo = {
  panel_brand_text: string;
  panel_font_size: string;
  panel_primary_color: string;
  panel_accent_color: string;
  panel_success_color: string;
  panel_warning_color: string;
  panel_error_color: string;
  panel_info_color: string;
  panel_link_color: string;
  panel_light_topbar_color: string;
  panel_dark_topbar_color: string;
  panel_light_bg_color: string;
  panel_dark_bg_color: string;
  panel_light_container_color: string;
  panel_dark_container_color: string;
  panel_light_text_color: string;
  panel_dark_text_color: string;
  has_logo_light: boolean;
  has_logo_dark: boolean;
  // Operator-configured panel hostname (GH #1411). "" when unset
  // (IP-only install). The login page uses it to detect wrong-host
  // access. May be absent on an older panel-api that predates the field.
  panel_hostname?: string;
};

const BRANDING_KEY = ["branding", "public"] as const;

export function useBranding() {
  const query = useQuery<BrandingInfo>({
    queryKey: BRANDING_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<BrandingInfo>("/branding");
      return data;
    },
    staleTime: 60_000,
    retry: 1,
  });

  const brandText = query.data?.panel_brand_text ?? "";
  const fontSize = (query.data?.panel_font_size ?? "medium") as
    | "small"
    | "medium"
    | "large";
  const d = query.data;
  const colors: Record<string, string> = {
    panel_primary_color: d?.panel_primary_color ?? "",
    panel_accent_color: d?.panel_accent_color ?? "",
    panel_success_color: d?.panel_success_color ?? "",
    panel_warning_color: d?.panel_warning_color ?? "",
    panel_error_color: d?.panel_error_color ?? "",
    panel_info_color: d?.panel_info_color ?? "",
    panel_link_color: d?.panel_link_color ?? "",
  };
  const chrome: Record<string, string> = {
    panel_light_topbar_color: d?.panel_light_topbar_color ?? "",
    panel_dark_topbar_color: d?.panel_dark_topbar_color ?? "",
    panel_light_bg_color: d?.panel_light_bg_color ?? "",
    panel_dark_bg_color: d?.panel_dark_bg_color ?? "",
    panel_light_container_color: d?.panel_light_container_color ?? "",
    panel_dark_container_color: d?.panel_dark_container_color ?? "",
    panel_light_text_color: d?.panel_light_text_color ?? "",
    panel_dark_text_color: d?.panel_dark_text_color ?? "",
  };
  const hasLogoLight = d?.has_logo_light ?? false;
  const hasLogoDark = d?.has_logo_dark ?? false;
  // undefined until the query settles; "" when settled with no hostname
  // configured. The login page relies on that distinction (GH #1411).
  const panelHostname = d?.panel_hostname;

  return {
    brandText,
    fontSize,
    colors,
    chrome,
    hasLogoLight,
    hasLogoDark,
    panelHostname,
    isLoading: query.isLoading,
  };
}

// logoURL returns the public endpoint URL for a given variant, or the
// built-in SVG path when the operator hasn't uploaded one. A cache-
// bust query string based on last update would be ideal but the
// endpoint already sends Cache-Control: max-age=300 and
// re-uploading replaces the on-disk file — browsers re-fetch on the
// next staleTime window.
export function logoURL(variant: "light" | "dark", hasCustom: boolean): string {
  if (hasCustom) {
    return `/api/v1/branding/logo/${variant}`;
  }
  return variant === "dark"
    ? "/images/jabali_logo_dark.svg"
    : "/images/jabali_logo.svg";
}

// useApplyBrandingToTitle keeps document.title in sync with brandText.
// Empty value falls back to "Jabali Panel".
export function useApplyBrandingToTitle() {
  const { brandText } = useBranding();
  useEffect(() => {
    document.title = brandText ? `${brandText} — Panel` : "Jabali Panel";
  }, [brandText]);
}
