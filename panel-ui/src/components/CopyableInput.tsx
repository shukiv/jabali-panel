import { useState } from "react";
import { Input, Tooltip, theme } from "antd";
import type { InputProps } from "antd";
import { CopyOutlined, CheckOutlined, EyeOutlined, EyeInvisibleOutlined } from "@icons";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts

interface CopyableInputProps extends Omit<InputProps, "value" | "readOnly" | "suffix" | "type"> {
  value: string;
  /** Text copied to the clipboard. Defaults to `value` (useful when `value` is masked). */
  copyText?: string;
  /** Mask the value behind a reveal toggle (for secrets / passwords). */
  secret?: boolean;
}

/**
 * Read-only field with the copy affordance rendered INSIDE the input (as a
 * suffix), not below or beside it. This is the panel-wide convention for
 * showing a copyable value — token IDs, one-time secrets, generated
 * passwords, connection strings. With `secret`, a reveal-eye toggle sits
 * alongside the copy icon, also inside the input.
 */
export function CopyableInput({ value, copyText, secret = false, ...rest }: CopyableInputProps) {
  const { token } = theme.useToken();
  const [copied, setCopied] = useState(false);
  const [revealed, setRevealed] = useState(!secret);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(copyText ?? value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      feedback.message.error("Clipboard blocked — copy manually");
    }
  };

  const display = secret && !revealed ? "•".repeat(Math.min(value.length, 40)) : value;
  const iconStyle = { cursor: "pointer", color: token.colorTextDescription, display: "inline-flex" };

  return (
    <Input
      {...rest}
      readOnly
      value={display}
      onFocus={(e) => e.currentTarget.select()}
      suffix={
        <span style={{ display: "inline-flex", gap: 8, alignItems: "center" }}>
          {secret && (
            <Tooltip title={revealed ? "Hide" : "Reveal"}>
              <span
                role="button"
                aria-label={revealed ? "Hide value" : "Reveal value"}
                onClick={() => setRevealed((r) => !r)}
                style={iconStyle}
              >
                {revealed ? <EyeInvisibleOutlined /> : <EyeOutlined />}
              </span>
            </Tooltip>
          )}
          <Tooltip title={copied ? "Copied" : "Copy"}>
            <span
              role="button"
              aria-label="Copy to clipboard"
              onClick={copied ? undefined : handleCopy}
              style={copied ? { display: "inline-flex", color: token.colorSuccess } : iconStyle}
            >
              {copied ? <CheckOutlined /> : <CopyOutlined />}
            </span>
          </Tooltip>
        </span>
      }
    />
  );
}
