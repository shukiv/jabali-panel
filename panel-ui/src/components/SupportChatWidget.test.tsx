// SupportChatWidget.test.tsx — each layout shows the "Ask Jabali" chat only
// when the admin turned it on for that panel, and hides it when it's turned
// off or the layout goes away.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => ({ enable: vi.fn(), disable: vi.fn() }));
vi.mock("../lib/supportChat", () => ({
  enableSupportChat: chat.enable,
  disableSupportChat: chat.disable,
}));

import { SupportChatWidget } from "./SupportChatWidget";

describe("SupportChatWidget", () => {
  beforeEach(() => vi.clearAllMocks());

  it("loads nothing while the chat is off", () => {
    render(<SupportChatWidget enabled={false} />);
    expect(chat.enable).not.toHaveBeenCalled();
  });

  it("shows the chat when on and hides it when turned off or unmounted", () => {
    const { rerender, unmount } = render(<SupportChatWidget enabled />);
    expect(chat.enable).toHaveBeenCalledTimes(1);

    rerender(<SupportChatWidget enabled={false} />);
    expect(chat.disable).toHaveBeenCalledTimes(1);

    rerender(<SupportChatWidget enabled />);
    expect(chat.enable).toHaveBeenCalledTimes(2);
    unmount();
    expect(chat.disable).toHaveBeenCalledTimes(2);
  });

  it("is mounted with each panel's own switch", () => {
    const src = (f: string) => readFileSync(resolve(__dirname, "../shells", f), "utf8");
    expect(src("AdminLayout.tsx")).toContain("<SupportChatWidget enabled={!!caps?.support_chat_admin} />");
    expect(src("UserLayout.tsx")).toContain("<SupportChatWidget enabled={!!caps?.support_chat_clients} />");
  });
});
