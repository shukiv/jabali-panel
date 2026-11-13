// useWebmailBranding.ts — GH #2502: a domain's webmail branding on
// mail.<domain>. Every call answers the whole branding, so each mutation
// writes its answer straight into the cache.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../apiClient";

export interface WebmailBranding {
  host: string;
  // can_edit: an admin, or the owner while their package has the switch.
  can_edit: boolean;
  // applies: webmail shows it (set by an admin, or the package allows it).
  applies: boolean;
  set_by_admin: boolean;
  app_name: string;
  company_name: string;
  website_url: string;
  privacy_url: string;
  imprint_url: string;
  // The pictures' content hashes; empty when there is none.
  logo_light: string;
  logo_dark: string;
  favicon: string;
}

export type WebmailBrandingText = Pick<
  WebmailBranding,
  "app_name" | "company_name" | "website_url" | "privacy_url" | "imprint_url"
>;

export type WebmailBrandingPicture = "logo-light" | "logo-dark" | "favicon";

export const webmailBrandingKey = (domainID: string) => ["webmail-branding", domainID];

const base = (domainID: string) => `/domains/${domainID}/webmail-branding`;

// webmailBrandingPictureURL is the panel's copy of the picture, for a
// preview; the hash makes a new upload a new URL.
export function webmailBrandingPictureURL(domainID: string, picture: WebmailBrandingPicture, hash: string) {
  return `/api/v1${base(domainID)}/${picture}?v=${encodeURIComponent(hash)}`;
}

export function useWebmailBranding(domainID: string) {
  return useQuery({
    queryKey: webmailBrandingKey(domainID),
    queryFn: async () => (await apiClient.get<WebmailBranding>(base(domainID))).data,
    enabled: !!domainID,
  });
}

function useBrandingMutation<V>(domainID: string, fn: (v: V) => Promise<WebmailBranding>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (data) => qc.setQueryData(webmailBrandingKey(domainID), data),
  });
}

export function useSaveWebmailBranding(domainID: string) {
  return useBrandingMutation(domainID, async (text: WebmailBrandingText) =>
    (await apiClient.put<WebmailBranding>(base(domainID), text)).data,
  );
}

export function useResetWebmailBranding(domainID: string) {
  return useBrandingMutation(domainID, async (_: void) =>
    (await apiClient.delete<WebmailBranding>(base(domainID))).data,
  );
}

export function useUploadWebmailBrandingPicture(domainID: string) {
  return useBrandingMutation(domainID, async ({ picture, file }: { picture: WebmailBrandingPicture; file: Blob }) => {
    const fd = new FormData();
    fd.append("file", file);
    return (
      await apiClient.post<WebmailBranding>(`${base(domainID)}/${picture}`, fd, {
        headers: { "Content-Type": "multipart/form-data" },
      })
    ).data;
  });
}

export function useRemoveWebmailBrandingPicture(domainID: string) {
  return useBrandingMutation(domainID, async (picture: WebmailBrandingPicture) =>
    (await apiClient.delete<WebmailBranding>(`${base(domainID)}/${picture}`)).data,
  );
}
