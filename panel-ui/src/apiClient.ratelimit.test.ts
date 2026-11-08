// A rate-limited request (429 {"error":"rate_limited"}) reads as a sentence,
// not the raw code: the database consoles show it as "Could not open
// phpMyAdmin: <message>".
import { AxiosError, type InternalAxiosRequestConfig } from "axios";
import { describe, expect, it } from "vitest";

import { apiClient } from "./apiClient";

describe("rate_limited message", () => {
  it("turns a 429 rate_limited into a readable sentence", async () => {
    const adapter = async (config: InternalAxiosRequestConfig) => {
      throw new AxiosError("Request failed with status code 429", "ERR_BAD_REQUEST", config, null, {
        status: 429,
        statusText: "Too Many Requests",
        data: { error: "rate_limited" },
        headers: { "retry-after": "6" },
        config,
      });
    };

    const err = (await apiClient.post("/sso/phpmyadmin", {}, { adapter }).catch((e: unknown) => e)) as Error;

    expect(err.message).toMatch(/too many requests/i);
    expect(err.message).not.toBe("rate limited");
  });
});
