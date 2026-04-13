/**
 * Jabali Terminal — browser-side bundle.
 *
 * Ship path: this file is copied by install.sh into the parent panel's
 *   resources/js/jabali-terminal.js
 * and referenced from vite.config.js. The Blade view at
 *   panel/views/terminal.blade.php
 * expects this bundle to publish `window.jabaliTerminalBundle` with three
 * helpers. Nothing else is exposed globally.
 *
 * CSP note (SEC-REV-8): no CDN imports — xterm.js and addons are resolved
 * through the panel's npm package graph.
 */

// Scoped @xterm/* packages (current) rather than legacy xterm*. The legacy
// names were deprecated and, more importantly, their internal APIs drifted
// out of sync with newer addon versions — mixing the two families causes
// "_renderer.value is undefined" and "mainDocument is undefined" crashes
// that no user-code try/catch can swallow (the errors fire from xterm's
// own async frames). Versions are pinned in package-deps.json so all four
// live in the same compatibility window.
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
// The canvas renderer is shipped as a separate addon in xterm.js 5.x —
// without it, _renderService._renderer.value stays undefined and any
// later refresh crashes in an async rAF we don't own.
import { CanvasAddon } from '@xterm/addon-canvas';
import '@xterm/xterm/css/xterm.css';

function base64urlDecode(s) {
    // Translate URL-safe alphabet to standard base64, pad to multiple of 4.
    const padded = s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4);
    const bin = atob(padded);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
        out[i] = bin.charCodeAt(i);
    }
    return out;
}

async function hmacHex(keyBytes, messageBytes) {
    // Web Crypto rather than a JS HMAC library: no third-party JS crypto.
    const cryptoKey = await crypto.subtle.importKey(
        'raw',
        keyBytes,
        { name: 'HMAC', hash: 'SHA-256' },
        false,
        ['sign'],
    );
    const sig = await crypto.subtle.sign('HMAC', cryptoKey, messageBytes);
    const bytes = new Uint8Array(sig);
    let hex = '';
    for (let i = 0; i < bytes.length; i++) {
        hex += bytes[i].toString(16).padStart(2, '0');
    }
    return hex;
}

function createTerminal(container, { onData, onResize }) {
    const term = new Terminal({
        cursorBlink: true,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        fontSize: 13,
        theme: { background: '#000000' },
        scrollback: 5000,
        convertEol: true,
    });

    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon());

    term.open(container);
    // ORDER MATTERS: CanvasAddon must be loaded AFTER open() in xterm 5.x —
    // its activate() calls terminal._core._renderService.setRenderer(), which
    // requires the render service (which open() creates). Without this, the
    // first resize/refresh schedules an async rAF that reads undefined
    // _renderer.value.dimensions and crashes.
    term.loadAddon(new CanvasAddon());

    // xterm.js 5.x lazily initialises its renderer on the first animation
    // frame after open(). Calling fit.fit() synchronously here — or letting
    // the ResizeObserver below fire before the first renderer commit —
    // triggers the internal _refreshAnimationFrame before _renderer.value
    // exists, which throws "can't access property 'dimensions' ...".
    // We defer the first fit until a frame after open(), retry on failure,
    // and treat every later fit() as best-effort.
    const safeFit = () => {
        try {
            fit.fit();
            onResize({ cols: term.cols, rows: term.rows });
            return true;
        } catch (_e) {
            return false;
        }
    };

    let fitAttempts = 0;
    const initialFit = () => {
        if (safeFit()) {
            return;
        }
        fitAttempts += 1;
        if (fitAttempts < 30) {
            requestAnimationFrame(initialFit);
        }
    };
    requestAnimationFrame(initialFit);

    // Push keystrokes to the server as UTF-8 bytes so non-ASCII input is handled.
    const encoder = new TextEncoder();
    term.onData((data) => {
        onData(encoder.encode(data));
    });

    // Re-fit on every window resize, and report the new geometry to the daemon
    // so the PTY's window size is updated (TIOCSWINSZ on the daemon side).
    const reportResize = () => {
        safeFit();
    };
    const ro = new ResizeObserver(reportResize);
    ro.observe(container);
    window.addEventListener('resize', reportResize);

    return {
        writeBytes(bytes) {
            term.write(bytes);
        },
        fit() {
            safeFit();
        },
        dispose() {
            window.removeEventListener('resize', reportResize);
            ro.disconnect();
            term.dispose();
        },
    };
}

window.jabaliTerminalBundle = {
    base64urlDecode,
    hmacHex,
    createTerminal,
};
