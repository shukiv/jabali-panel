// CollapsibleAlert — GH #2242. An AntD Alert whose description folds away
// behind a "Details" / "Hide" toggle, so a page opens on its content instead of
// on its explanations. On a phone, three full alerts filled the Updates page
// before any update information showed.
//
// Two kinds of alert use it, and they differ only in how they first appear:
//   plain notes      — folded to the title until someone asks (the default).
//   safety warnings  — `defaultOpen`: shown in full the first time, so every
//                      admin reads them once, then folded for good once
//                      someone folds them in this browser.
// The title, the colour and `action` (e.g. an "Update now" button) stay
// visible while folded, so nothing an admin must act on is ever hidden.
//
// The toggle sits at the end of the title row and `action` goes under the
// text. AntD's own `action` slot is a column beside the text that does not
// shrink: at phone width it squeezed the text to one or two words per line
// and made a banner a full screen tall.
//
// The browser remembers each alert's state in localStorage under
// `jabali.alert.<storageKey>`. That covers `dismissible` too: an alert closed
// with its X stays closed on the next load, where it used to come back.
// Storage can be blocked (private windows, strict policies), so every access is
// wrapped and the alert falls back to in-memory state.
//
// Alerts that report a live problem (an operation failed, a service is down)
// are not for this component: they must stay open while the problem lasts.

import { Alert, Button } from "antd";
import type { AlertProps } from "antd";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { DownOutlined, UpOutlined } from "@icons";

const STORAGE_PREFIX = "jabali.alert.";

type AlertState = "open" | "folded" | "dismissed";

const readState = (key: string): AlertState | null => {
  try {
    const v = window.localStorage.getItem(STORAGE_PREFIX + key);
    return v === "open" || v === "folded" || v === "dismissed" ? v : null;
  } catch {
    return null;
  }
};

const writeState = (key: string, state: AlertState) => {
  try {
    window.localStorage.setItem(STORAGE_PREFIX + key, state);
  } catch {
    /* storage disabled — keep in-memory */
  }
};

export type CollapsibleAlertProps = Omit<AlertProps, "closable" | "onClose"> & {
  /** Stable id for this alert. Give a different key to each variant that
   * should open again on its own (e.g. a different problem). */
  storageKey: string;
  /** Show the description on the first visit (safety warnings). */
  defaultOpen?: boolean;
  /** Show an X; a closed alert stays closed in this browser. */
  dismissible?: boolean;
};

export function CollapsibleAlert(props: CollapsibleAlertProps) {
  // Keyed so a new storageKey starts from its own stored state.
  return <CollapsibleAlertInner key={props.storageKey} {...props} />;
}

function CollapsibleAlertInner({
  storageKey,
  defaultOpen = false,
  dismissible = false,
  title,
  message,
  description,
  action,
  ...rest
}: CollapsibleAlertProps) {
  const { t } = useTranslation();
  const [state, setState] = useState<AlertState>(
    () => readState(storageKey) ?? (defaultOpen ? "open" : "folded"),
  );

  if (state === "dismissed") return null;

  const set = (next: AlertState) => {
    setState(next);
    writeState(storageKey, next);
  };

  const open = state === "open";
  const toggle = description ? (
    <Button
      size="small"
      type="link"
      aria-expanded={open}
      icon={open ? <UpOutlined /> : <DownOutlined />}
      iconPlacement="end"
      // No side padding: on a phone the toggle wraps under the title and must
      // line up with it.
      style={{ paddingInline: 0 }}
      onClick={() => set(open ? "folded" : "open")}
    >
      {open ? t("collapsiblealert.hide") : t("collapsiblealert.details")}
    </Button>
  ) : null;

  const shown = open && description;
  return (
    <Alert
      {...rest}
      title={
        <span
          style={{
            display: "flex",
            flexWrap: "wrap",
            alignItems: "center",
            justifyContent: "space-between",
            columnGap: 8,
          }}
        >
          <span style={{ minWidth: 0 }}>{title ?? message}</span>
          {toggle}
        </span>
      }
      description={
        shown || action ? (
          <>
            {shown ? description : null}
            {action ? <div style={{ marginTop: shown ? 8 : 0 }}>{action}</div> : null}
          </>
        ) : undefined
      }
      // An object `closable` only renders the button when it names a closeIcon
      // (true = antd's default icon).
      closable={
        dismissible
          ? {
              closeIcon: true,
              onClose: () => set("dismissed"),
              "aria-label": t("collapsiblealert.close"),
            }
          : false
      }
    />
  );
}
