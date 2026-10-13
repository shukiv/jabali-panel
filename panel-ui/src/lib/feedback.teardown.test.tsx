// feedback.teardown.test.tsx — a toast a test fires renders in a tree that
// RTL's cleanup unmounts. AntD's static fallback renders into a root nothing
// unmounts, so a toast fired near the end of a file rendered after the test
// environment was torn down and failed a CI run with "window is not defined".
import { act, cleanup, screen } from "@testing-library/react";
import { expect, it } from "vitest";

import { feedback } from "./feedback";

it("a toast fired in a test is gone after cleanup", async () => {
  act(() => {
    feedback.message.success("toast from a test");
  });
  expect(await screen.findByText("toast from a test")).toBeInTheDocument();
  cleanup();
  expect(screen.queryByText("toast from a test")).toBeNull();
});
