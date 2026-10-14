// SupportChatWidget — mounts the "Ask Jabali" support chat in a layout when the
// admin turned it on for that panel (Server Settings -> General -> Support
// chat). Renders nothing itself: the Chatwoot SDK adds its own bubble, loaded
// after the page is idle (lib/supportChat). Turning the chat off, or leaving
// the layout, hides the widget.
import { useEffect } from "react";

import { disableSupportChat, enableSupportChat } from "../lib/supportChat";

export function SupportChatWidget({ enabled }: { enabled: boolean }) {
  useEffect(() => {
    if (!enabled) return undefined;
    enableSupportChat();
    return () => disableSupportChat();
  }, [enabled]);
  return null;
}
