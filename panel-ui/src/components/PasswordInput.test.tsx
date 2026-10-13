// PasswordInput.test.tsx — the dice button only lands a generated password the
// field's own rule accepts. The generator is mocked so each case is exact.
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./passwordGenerator", () => ({ generatePassword: vi.fn() }));

import { PasswordInput } from "./PasswordInput";
import { generatePassword } from "./passwordGenerator";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("PasswordInput generator", () => {
  it("lands the first password without a rule", () => {
    vi.mocked(generatePassword).mockReturnValue("first=Password1");
    const onChange = vi.fn();
    render(<PasswordInput onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Generate strong password" }));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.calls[0][0].target.value).toBe("first=Password1");
  });

  it("leaves the field alone when no generated password passes the rule", () => {
    vi.mocked(generatePassword).mockReturnValue("always=Equals1");
    const onChange = vi.fn();
    render(<PasswordInput onChange={onChange} accepts={(pw) => !pw.includes("=")} />);
    fireEvent.click(screen.getByRole("button", { name: "Generate strong password" }));
    expect(onChange).not.toHaveBeenCalled();
    expect(vi.mocked(generatePassword).mock.calls.length).toBeGreaterThan(1);
  });
});
