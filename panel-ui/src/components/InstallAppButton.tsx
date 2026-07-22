// InstallAppButton — surfaces the browser's PWA install prompt (#434). Renders
// only after the browser fires `beforeinstallprompt` (i.e. the manifest + SW
// install criteria are met and the app isn't already installed); hidden
// otherwise, so it's a no-op on unsupported browsers / installed instances.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";

import { Button, Tooltip } from "antd";

import { DownloadOutlined } from "@icons";

// BeforeInstallPromptEvent isn't in the DOM lib typings yet.
type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
};

export function InstallAppButton() {
  const { t } = useTranslation();
  const [promptEvent, setPromptEvent] =
    useState<BeforeInstallPromptEvent | null>(null);

  useEffect(() => {
    const onBeforeInstall = (e: Event) => {
      e.preventDefault();
      setPromptEvent(e as BeforeInstallPromptEvent);
    };
    const onInstalled = () => setPromptEvent(null);
    window.addEventListener("beforeinstallprompt", onBeforeInstall);
    window.addEventListener("appinstalled", onInstalled);
    return () => {
      window.removeEventListener("beforeinstallprompt", onBeforeInstall);
      window.removeEventListener("appinstalled", onInstalled);
    };
  }, []);

  if (!promptEvent) return null;

  return (
    <Tooltip title={t("installappbutton.install_jabali_as_an_app")}>
      <Button
        type="text"
        aria-label={t("installappbutton.install_app")}
        icon={<DownloadOutlined />}
        onClick={async () => {
          try {
            await promptEvent.prompt();
            await promptEvent.userChoice;
          } finally {
            setPromptEvent(null);
          }
        }}
      />
    </Tooltip>
  );
}
