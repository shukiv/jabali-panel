// ssoTab — open a one-time sign-in link (webmail, phpMyAdmin / Adminer, a
// CMS admin login) in a new tab without wasting it.
//
// The link can be used once, so the tab has to exist before the link is
// minted:
//   - window.open must run synchronously with the click; after an await,
//     popup blockers may refuse it;
//   - with "noopener" window.open returns null whether or not the tab opened,
//     so a blocked tab could not be told apart from an open one.
// So: open a blank tab first, give up (and do not mint) if it was blocked,
// cut its link back to the panel, then mint and send the tab to the link.
import { feedback } from "./feedback";

// openSsoTab opens the blank tab. When the browser blocks it, it shows
// `blockedMessage` and returns null; the caller must then not mint.
export function openSsoTab(blockedMessage: string): Window | null {
  const tab = window.open("about:blank", "_blank");
  if (!tab) {
    feedback.message.warning(blockedMessage);
    return null;
  }
  // JAB-330: sever the opener BEFORE anything async, so the opened site can
  // never reach back into the panel window (reverse tabnabbing). noopener
  // can't be passed to window.open: it makes window.open return null.
  try {
    tab.opener = null;
  } catch {
    // older Safari — best effort
  }
  return tab;
}

// sendSsoTab sends the tab to the minted link. The blank tab shares the
// panel's origin, so only an http(s) link is followed; for anything else the
// tab is closed and false is returned. False also when the user closed the tab,
// or took it elsewhere, while the link was being minted.
export function sendSsoTab(tab: Window, url: string | null | undefined): boolean {
  let target: URL | null = null;
  try {
    target = url ? new URL(url, window.location.href) : null;
  } catch {
    target = null;
  }
  if (!target || (target.protocol !== "https:" && target.protocol !== "http:")) {
    tab.close();
    return false;
  }
  if (tab.closed) return false;
  try {
    tab.location.href = target.href;
  } catch {
    // The tab is on another origin now.
    return false;
  }
  return true;
}
