// A browser can hold a Kratos session that the panel can't map to an account:
// panel-api answers 401 identity_not_linked (for example a passkey enrolled on
// an identity whose panel user is gone). /login then saw Kratos refuse a new
// flow (session_already_available) and escalated to ?aal=aal2. An identity
// with no second factor gets an aal2 flow with nothing in it, so the page
// showed "No credential method is currently configured" with no way to sign
// out. initLoginFlow now tells that case apart instead of escalating it.
import axios from "axios";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { initLoginFlow, kratosClient, SessionNotLinkedError } from "./kratos";

const alreadyLoggedIn = {
  response: { status: 400, data: { error: { id: "session_already_available" } } },
};

function panelMe(status: number, error: string) {
  return vi
    .spyOn(axios, "get")
    .mockRejectedValue({ response: { status, data: { error } } });
}

describe("initLoginFlow with a session the panel can't use", () => {
  const assign = vi.fn();
  const realLocation = window.location;

  beforeEach(() => {
    assign.mockReset();
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { href: "https://panel.example.com/login", pathname: "/login", search: "", assign },
    });
  });

  afterEach(() => {
    Object.defineProperty(window, "location", { configurable: true, value: realLocation });
    vi.restoreAllMocks();
  });

  it("reports an unlinked identity instead of escalating to aal2", async () => {
    vi.spyOn(kratosClient, "get").mockRejectedValue(alreadyLoggedIn);
    const me = panelMe(401, "identity_not_linked");

    await expect(initLoginFlow()).rejects.toBeInstanceOf(SessionNotLinkedError);
    expect(me).toHaveBeenCalledWith("/api/v1/me", expect.objectContaining({ withCredentials: true }));
    expect(assign).not.toHaveBeenCalled();
  });

  it("still escalates to aal2 when the panel wants a second factor", async () => {
    vi.spyOn(kratosClient, "get").mockRejectedValue(alreadyLoggedIn);
    panelMe(401, "invalid_session");

    void initLoginFlow();
    await vi.waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
    expect(assign.mock.calls[0][0]).toBe("/.ory/self-service/login/browser?aal=aal2");
  });

  it("follows Kratos's own redirect without asking the panel", async () => {
    vi.spyOn(kratosClient, "get").mockRejectedValue({
      response: {
        status: 422,
        data: {
          error: { id: "browser_location_change_required" },
          redirect_browser_to: "/.ory/self-service/login/browser?aal=aal2&return_to=x",
        },
      },
    });
    const me = vi.spyOn(axios, "get");

    void initLoginFlow();
    await vi.waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
    expect(assign.mock.calls[0][0]).toBe("/.ory/self-service/login/browser?aal=aal2&return_to=x");
    expect(me).not.toHaveBeenCalled();
  });
});
