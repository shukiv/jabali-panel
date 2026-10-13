// PasswordInput — drop-in replacement for AntD's Input.Password that
// adds a "generate strong password" button. Designed to be used inside
// a Form.Item; it speaks the same event-based value/onChange contract
// the native Input.Password uses, so existing Form.Item bindings don't
// need any adjustment.
//
// Usage:
//
//   <Form.Item label="Password" name="password" rules={…}>
//     <PasswordInput autoComplete="new-password" />
//   </Form.Item>
//
// Do NOT use this on sign-in pages — the login flow is for typing an
// existing password, not generating a new one.

import { Button, Input, Space, Tooltip } from "antd";
import type { ChangeEvent, ComponentProps } from "react";

import { generatePassword } from "./passwordGenerator";

type InputPasswordProps = ComponentProps<typeof Input.Password>;

interface PasswordInputProps extends Omit<InputPasswordProps, "suffix"> {
  /** Length passed to generatePassword. Default 16. */
  generatorLength?: number;
  /**
   * Field-specific rule a generated password must pass (an app that bans
   * "=", say). The generator retries a few times and leaves the field alone
   * if nothing passes.
   */
  accepts?: (password: string) => boolean;
}

// One generated password in five or so contains a given symbol, so 20
// tries is plenty for any rule that bans a character or two.
const GENERATE_TRIES = 20;

// Dice icon for the "generate password" affordance. Matches AntD icon
// sizing conventions: 1em × 1em, currentColor, aria-hidden. The five-pip
// face reads cleanly at 14–16px, which is where AntD renders icons
// inside standard Buttons.
function DiceIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      width="1em"
      height="1em"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <rect x="3" y="3" width="18" height="18" rx="3" />
      <circle cx="7.5" cy="7.5" r="1.2" fill="currentColor" stroke="none" />
      <circle cx="16.5" cy="7.5" r="1.2" fill="currentColor" stroke="none" />
      <circle cx="12" cy="12" r="1.2" fill="currentColor" stroke="none" />
      <circle cx="7.5" cy="16.5" r="1.2" fill="currentColor" stroke="none" />
      <circle cx="16.5" cy="16.5" r="1.2" fill="currentColor" stroke="none" />
    </svg>
  );
}

export function PasswordInput({
  value,
  onChange,
  generatorLength = 16,
  accepts,
  ...rest
}: PasswordInputProps) {
  const handleGenerate = () => {
    let pw: string | undefined;
    for (let i = 0; i < GENERATE_TRIES && pw === undefined; i++) {
      const candidate = generatePassword(generatorLength);
      if (!accepts || accepts(candidate)) pw = candidate;
    }
    if (pw === undefined) return;
    // Forward a minimal ChangeEvent-shaped object so Form.Item (which
    // calls the child's onChange with whatever the child emits) lands
    // the value in form state the same way native keystrokes do.
    onChange?.({
      target: { value: pw },
      currentTarget: { value: pw },
    } as ChangeEvent<HTMLInputElement>);
  };

  return (
    <Space.Compact style={{ width: "100%", display: "flex" }}>
      <Input.Password
        {...rest}
        value={value}
        onChange={onChange}
        style={{ flex: 1 }}
      />
      <Tooltip title={`Generate ${generatorLength}-character strong password`}>
        <Button
          type="default"
          onClick={handleGenerate}
          icon={<DiceIcon />}
          aria-label="Generate strong password"
        />
      </Tooltip>
    </Space.Compact>
  );
}
