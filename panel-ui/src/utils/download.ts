// Trigger a browser file download without navigating the page.
//
// Setting `window.location.href` to a download URL makes the browser begin a
// navigation, which ABORTS any in-flight XHR (e.g. the backups-list poll). The
// aborted request then surfaces a spurious "Network Failed" toast a moment
// before the download actually starts (GH #462). An `<a download>` click
// triggers the download in the background and leaves the SPA (and its pending
// requests) untouched. Same-origin + cookie auth still apply, and the filename
// comes from the response's Content-Disposition header.
export function downloadUrl(url: string): void {
  const a = document.createElement("a");
  a.href = url;
  a.download = "";
  document.body.appendChild(a);
  a.click();
  a.remove();
}
