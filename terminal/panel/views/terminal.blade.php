<x-filament-panels::page>
    {{-- Jabali Terminal page. See docs/SECURITY.md for the auth flow this view implements. --}}
    {{--
        xterm.js is bundled by the parent panel via Vite (resources/js/jabali-terminal.js).
        The module is imported on demand so other panel pages don't pay the cost.
        CSP forbids CDN script loads (SEC-REV-8 reinforcement) — do NOT switch to a CDN.
    --}}
    @vite(['resources/css/jabali-terminal.css', 'resources/js/jabali-terminal.js'])

    {{--
        Outer Alpine scope holds the tab state only. Terminal + Sessions each
        get their own subtree with x-show so switching tabs does NOT tear down
        the inner xterm x-data block (the WS + PTY stay alive while the user
        peeks at transcripts).
    --}}
    <div x-data="{ tab: 'terminal' }" class="flex flex-col gap-4">
        {{--
            Uses Filament's <x-filament::tabs> + tabs.item to match the
            pill-segmented look Server Settings and friends render. The
            `alpine-active` prop on each item (Filament support component,
            see vendor/filament/support/resources/views/components/tabs/item.blade.php)
            toggles the `fi-active` class from Alpine state — no Livewire
            round-trip, so the xterm canvas and WebSocket inside the
            Terminal subtree are never re-rendered when switching tabs.
            `contained` gives the segmented-control style; without it
            you'd get the thin underline look (the previous hand-rolled
            markup mimicked that, but users preferred the pills).
        --}}
        <x-filament::tabs contained>
            <x-filament::tabs.item
                icon="heroicon-o-command-line"
                x-on:click="tab = 'terminal'"
                alpine-active="tab === 'terminal'"
            >
                {{ __('Terminal') }}
            </x-filament::tabs.item>
            <x-filament::tabs.item
                icon="heroicon-o-clock"
                x-on:click="tab = 'sessions'"
                alpine-active="tab === 'sessions'"
            >
                {{ __('Sessions') }}
            </x-filament::tabs.item>
        </x-filament::tabs>

        {{-- ───────── Terminal tab ───────── --}}
        <div
            x-show="tab === 'terminal'"
            id="jt-root"
            x-data="jabaliTerminal()"
            x-init="init()"
            wire:ignore
            class="flex flex-col gap-4"
        >
            {{-- Re-auth section --}}
            <div x-show="stage === 'auth'" x-cloak>
                <x-filament::section
                    :heading="__('Re-authenticate to open a root shell')"
                    :description="$requiresTwoFactor
                        ? __('A fresh password and 2FA code are required every time. The shell runs as root.')
                        : __('A fresh password is required every time. The shell runs as root.')"
                    icon="heroicon-o-lock-closed"
                >
                    <form @submit.prevent="submitAuth()" class="flex flex-col gap-4">
                        <x-filament::input.wrapper>
                            <x-filament::input
                                type="password"
                                x-model="password"
                                :placeholder="__('Password')"
                                autocomplete="current-password"
                                required
                                x-bind:disabled="busy"
                            />
                        </x-filament::input.wrapper>

                        @if ($requiresTwoFactor)
                            <x-filament::input.wrapper>
                                <x-filament::input
                                    type="text"
                                    x-model="twoFactorCode"
                                    :placeholder="__('2FA code')"
                                    inputmode="numeric"
                                    pattern="[0-9]*"
                                    maxlength="10"
                                    autocomplete="one-time-code"
                                    required
                                    x-bind:disabled="busy"
                                />
                            </x-filament::input.wrapper>
                        @endif

                        <template x-if="error">
                            <div>
                                <x-filament::badge color="danger" size="lg" class="w-full">
                                    <span x-text="error"></span>
                                </x-filament::badge>
                            </div>
                        </template>

                        <x-filament::button
                            type="submit"
                            color="primary"
                            size="lg"
                            icon="heroicon-o-command-line"
                            x-bind:disabled="busy || password.length === 0 || ({{ $requiresTwoFactor ? 'true' : 'false' }} && twoFactorCode.length === 0)"
                        >
                            <span x-show="!busy">{{ __('Open terminal') }}</span>
                            <span x-show="busy" x-cloak>{{ __('Opening…') }}</span>
                        </x-filament::button>
                    </form>
                </x-filament::section>
            </div>

            {{-- Live terminal --}}
            <div x-show="stage === 'live'" x-cloak class="flex flex-col gap-3">
                <x-filament::section>
                    <div class="flex items-center justify-between">
                        <div class="flex items-center gap-2">
                            <template x-if="connected">
                                <x-filament::badge color="success" icon="heroicon-o-signal">
                                    {{ __('Connected') }}
                                </x-filament::badge>
                            </template>
                            <template x-if="!connected">
                                <x-filament::badge color="warning" icon="heroicon-o-signal-slash">
                                    {{ __('Connecting…') }}
                                </x-filament::badge>
                            </template>
                        </div>
                        <x-filament::button
                            type="button"
                            color="danger"
                            size="sm"
                            icon="heroicon-o-x-mark"
                            x-on:click="endSession()"
                        >
                            {{ __('End session') }}
                        </x-filament::button>
                    </div>
                </x-filament::section>

                <div id="jt-xterm" class="fi-section rounded-xl bg-black p-2 min-h-[60vh]"></div>

                <template x-if="warning">
                    <div>
                        <x-filament::badge color="warning" icon="heroicon-o-exclamation-triangle" size="lg">
                            <span x-text="warning"></span>
                        </x-filament::badge>
                    </div>
                </template>
            </div>

            {{-- Closed state --}}
            <div x-show="stage === 'closed'" x-cloak>
                <x-filament::section icon="heroicon-o-power">
                    <div class="flex flex-col items-center justify-center gap-4 py-6">
                        <p class="text-sm text-gray-500 dark:text-gray-400" x-text="closeReason || '{{ __('Session closed') }}'"></p>
                        <x-filament::button
                            type="button"
                            color="primary"
                            icon="heroicon-o-arrow-path"
                            x-on:click="resetToAuth()"
                        >
                            {{ __('Open another session') }}
                        </x-filament::button>
                    </div>
                </x-filament::section>
            </div>
        </div>

        {{-- ───────── Sessions tab ───────── --}}
        <div x-show="tab === 'sessions'" x-cloak class="flex flex-col gap-4">
            <x-filament::section>
                <div class="flex items-center justify-between">
                    <p class="text-sm text-gray-500 dark:text-gray-400">
                        {{ __('Last 100 sessions. Transcripts are sealed with HMAC on close.') }}
                    </p>
                    <x-filament::button
                        color="gray"
                        size="sm"
                        icon="heroicon-o-arrow-path"
                        wire:click="refreshSessions"
                    >
                        {{ __('Refresh') }}
                    </x-filament::button>
                </div>
            </x-filament::section>

            <x-filament::section>
                @if (empty($sessions))
                    <div class="flex flex-col items-center justify-center gap-2 py-10 text-center">
                        <x-filament::icon
                            icon="heroicon-o-clock"
                            class="h-8 w-8 text-gray-400"
                        />
                        <p class="text-sm text-gray-500 dark:text-gray-400">
                            {{ __('No sessions recorded yet.') }}
                        </p>
                    </div>
                @else
                    <div class="overflow-x-auto">
                        <table class="w-full text-start text-sm">
                            <thead>
                                <tr class="border-b border-gray-200 dark:border-white/10">
                                    <th class="px-3 py-2 text-start font-medium text-gray-500 dark:text-gray-300">{{ __('Date') }}</th>
                                    <th class="px-3 py-2 text-start font-medium text-gray-500 dark:text-gray-300">{{ __('Admin') }}</th>
                                    <th class="px-3 py-2 text-start font-medium text-gray-500 dark:text-gray-300">{{ __('Session') }}</th>
                                    <th class="px-3 py-2 text-end font-medium text-gray-500 dark:text-gray-300">{{ __('Size') }}</th>
                                    <th class="px-3 py-2 text-start font-medium text-gray-500 dark:text-gray-300">{{ __('Sealed') }}</th>
                                    <th class="px-3 py-2 text-end font-medium text-gray-500 dark:text-gray-300"></th>
                                </tr>
                            </thead>
                            <tbody class="divide-y divide-gray-200 dark:divide-white/5">
                                @foreach ($sessions as $session)
                                    <tr>
                                        <td class="px-3 py-2 font-mono text-xs">{{ $session['date'] ?? '' }}</td>
                                        <td class="px-3 py-2">{{ $session['admin'] ?? '' }}</td>
                                        <td class="px-3 py-2 font-mono text-xs">{{ $session['session_id'] ?? '' }}</td>
                                        <td class="px-3 py-2 text-end tabular-nums">{{ number_format((int) ($session['size_bytes'] ?? 0)) }} B</td>
                                        <td class="px-3 py-2">
                                            @if ($session['sealed'] ?? false)
                                                <x-filament::badge color="success" icon="heroicon-o-lock-closed" size="sm">
                                                    {{ __('sealed') }}
                                                </x-filament::badge>
                                            @else
                                                <x-filament::badge color="warning" icon="heroicon-o-lock-open" size="sm">
                                                    {{ __('unsealed') }}
                                                </x-filament::badge>
                                            @endif
                                        </td>
                                        <td class="px-3 py-2 text-end">
                                            {{--
                                                Plain <button> rather than <x-filament::link tag="button">:
                                                the Filament link component's attribute bag didn't forward
                                                wire:click reliably in some Filament 5 builds when tag=button,
                                                and passing a string argument was also suspect in Livewire 4's
                                                parser. Switched to a pure numeric-index call — bulletproof
                                                regardless of Filament / Livewire version chrome. The PHP side
                                                (viewTranscriptAt) looks the name up from $this->sessions[$i]
                                                and delegates to the same validation path viewTranscript uses.
                                            --}}
                                            <button
                                                type="button"
                                                wire:click="viewTranscriptAt({{ $loop->index }})"
                                                class="inline-flex items-center gap-1 text-sm font-medium text-primary-600 hover:text-primary-500 dark:text-primary-400 dark:hover:text-primary-300"
                                            >
                                                <x-filament::icon icon="heroicon-o-eye" class="h-4 w-4" />
                                                {{ __('View') }}
                                            </button>
                                        </td>
                                    </tr>
                                @endforeach
                            </tbody>
                        </table>
                    </div>
                @endif
            </x-filament::section>

            @if ($transcript !== null)
                <x-filament::section
                    :heading="$openName"
                    icon="heroicon-o-document-text"
                >
                    <x-slot name="headerEnd">
                        <x-filament::button
                            color="gray"
                            size="sm"
                            icon="heroicon-o-x-mark"
                            wire:click="closeTranscript"
                        >
                            {{ __('Close') }}
                        </x-filament::button>
                    </x-slot>
                    {{-- Plain <pre>: we never {!! !!} the transcript; Blade auto-escapes so
                         arbitrary bytes written by the daemon cannot inject HTML. --}}
                    <pre class="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-950 p-4 font-mono text-xs text-gray-200">{{ $transcript }}</pre>
                </x-filament::section>
            @endif
        </div>
    </div>

    {{--
        The Alpine factory below is intentionally small — all heavy lifting
        (xterm.js, Web Crypto HMAC, WebSocket) lives in the Vite bundle under
        resources/js/jabali-terminal.js so it is audited alongside the rest
        of the panel's JS.

        Contract with the bundle:
          window.jabaliTerminalBundle = {
              createTerminal(containerEl, { onData, onResize }): { writeBytes, fit, dispose },
              hmacHex(keyBytes, messageBytes): Promise<string>,  // HMAC-SHA256 via Web Crypto
              base64urlDecode(s): Uint8Array,
          };
    --}}
    <script>
        function jabaliTerminal() {
            // SEC-REV-2: token lives in this closure variable only. It is never
            // rendered into the DOM and is overwritten with null as soon as the
            // WS auth frame has been sent.
            let token = null;
            let ws = null;
            let term = null;
            // Three-state machine for the WS receive path. 'challenge' means we
            // still expect the daemon's JSON challenge as the first frame;
            // 'data' means PTY bytes from here on. `pending` buffers PTY bytes
            // that arrive after the auth frame is sent but before the xterm
            // instance has finished mounting (Alpine's DOM flip + a layout
            // frame). Without this, a fast prompt byte would race ahead of
            // `term` being set and either close the WS (mistaken for a second
            // challenge) or throw inside term.writeBytes.
            let wsPhase = 'challenge';
            let pending = [];

            return {
                stage: 'auth',
                password: '',
                twoFactorCode: '',
                busy: false,
                error: '',
                warning: '',
                connected: false,
                closeReason: '',

                async init() {
                    // Nothing to pre-load; the bundle is loaded by the Vite
                    // directive at the top of this view.
                },

                async submitAuth() {
                    this.error = '';
                    this.busy = true;
                    try {
                        // Dedicated route (Step 6) — never Livewire — so the
                        // response JSON never rides through an HTML render.
                        const csrf = document.querySelector('meta[name="csrf-token"]');
                        const response = await fetch('{{ route('jabali-terminal.session') }}', {
                            method: 'POST',
                            credentials: 'same-origin',
                            headers: {
                                'Content-Type': 'application/json',
                                'Accept': 'application/json',
                                'X-CSRF-TOKEN': csrf ? csrf.getAttribute('content') : '',
                                'X-Requested-With': 'XMLHttpRequest',
                            },
                            body: JSON.stringify({
                                password: this.password,
                                two_factor_code: this.twoFactorCode,
                            }),
                        });
                        // Scrub secrets from local state regardless of outcome.
                        this.password = '';
                        this.twoFactorCode = '';

                        if (response.status === 429) {
                            this.error = 'too many attempts, wait a minute';
                            return;
                        }
                        if (!response.ok) {
                            // Generic message — do not differentiate 422 vs 403
                            // vs 502 to the user; status just tells us whether
                            // to let them retry now or show "try later".
                            this.error = (response.status >= 500)
                                ? 'daemon unavailable'
                                : 'invalid credentials';
                            return;
                        }

                        const result = await response.json();
                        if (!result || !result.token || !result.ws_url) {
                            this.error = 'session mint failed';
                            return;
                        }
                        token = result.token;
                        await this.openWebSocket(result.ws_url);
                    } catch (e) {
                        this.error = 'authentication failed';
                    } finally {
                        this.busy = false;
                    }
                },

                async openWebSocket(wsUrl) {
                    const bundle = window.jabaliTerminalBundle;
                    if (!bundle) {
                        this.error = 'terminal bundle missing';
                        return;
                    }

                    this.stage = 'live';
                    this.connected = false;

                    // Extract the 32B nonce at bytes 40..72 of the 104B raw token.
                    const rawToken = bundle.base64urlDecode(token);
                    if (rawToken.length !== 104) {
                        this.stage = 'closed';
                        this.closeReason = 'malformed token';
                        return;
                    }
                    const tokenNonce = rawToken.slice(40, 72);

                    ws = new WebSocket(wsUrl);
                    ws.binaryType = 'arraybuffer';

                    ws.addEventListener('message', async (evt) => {
                        // First message must be the challenge (SEC-REV-2).
                        if (wsPhase === 'challenge') {
                            let msg;
                            try {
                                msg = JSON.parse(typeof evt.data === 'string' ? evt.data : new TextDecoder().decode(evt.data));
                            } catch {
                                ws.close();
                                return;
                            }
                            if (msg.type !== 'challenge' || typeof msg.nonce !== 'string') {
                                ws.close();
                                return;
                            }
                            // Compute HMAC-SHA256(token_nonce, challenge_nonce_bytes) via Web Crypto.
                            const challengeBytes = hexToBytes(msg.nonce);
                            const nonceResponse = await bundle.hmacHex(tokenNonce, challengeBytes);
                            const authToken = token;
                            token = null; // SEC-REV-2: drop the token from memory immediately.
                            ws.send(JSON.stringify({
                                type: 'auth',
                                token: authToken,
                                nonce_response: nonceResponse,
                            }));
                            wsPhase = 'data';
                            this.connected = true;
                            // Wait for Alpine to flip `x-show` (stage === 'live'
                            // was set above) AND for the browser to lay out the
                            // now-visible flex container before handing it to
                            // xterm. Without the nextTick + rAF pair, term.open
                            // can be called while the container still has
                            // display:none or zero pixels, leaving xterm in a
                            // state where later prompt bytes render into an
                            // invisible canvas. This was reproducible as a
                            // ~50% blank-terminal on panel reload.
                            this.$nextTick(() => {
                                requestAnimationFrame(() => this.mountTerminal());
                            });
                            return;
                        }

                        // Post-auth: PTY bytes from daemon. May arrive before
                        // mountTerminal has run; buffer until term is ready.
                        const bytes = (typeof evt.data === 'string')
                            ? new TextEncoder().encode(evt.data)
                            : new Uint8Array(evt.data);
                        if (term) {
                            term.writeBytes(bytes);
                        } else {
                            pending.push(bytes);
                        }
                    });

                    ws.addEventListener('close', (evt) => {
                        this.connected = false;
                        this.stage = 'closed';
                        this.closeReason = evt.reason || 'session closed';
                        if (term) {
                            term.dispose();
                            term = null;
                        }
                        wsPhase = 'challenge';
                        pending = [];
                    });

                    ws.addEventListener('error', () => {
                        // Don't leak details — close handler will flip UI state.
                    });
                },

                mountTerminal() {
                    if (term !== null) {
                        return;
                    }
                    const bundle = window.jabaliTerminalBundle;
                    const container = document.getElementById('jt-xterm');
                    if (!container) {
                        return;
                    }
                    // The flex container gains size only after Alpine has
                    // removed display:none on the live-stage div AND the
                    // browser has performed layout. If we got here early
                    // (Vite bundle still initialising, first frame hadn't
                    // laid out yet), spin on rAF until the container has
                    // real pixels — xterm can't measure a 0x0 box.
                    if (container.clientWidth === 0 || container.clientHeight === 0) {
                        requestAnimationFrame(() => this.mountTerminal());
                        return;
                    }
                    term = bundle.createTerminal(container, {
                        onData: (bytes) => {
                            if (ws && ws.readyState === WebSocket.OPEN) {
                                ws.send(bytes);
                            }
                        },
                        onResize: ({ cols, rows }) => {
                            if (ws && ws.readyState === WebSocket.OPEN) {
                                ws.send(JSON.stringify({ type: 'resize', cols, rows }));
                            }
                        },
                    });
                    term.fit();

                    // Drain any PTY bytes that arrived while we were waiting
                    // for the container to finish laying out. This keeps the
                    // post-auth prompt from being lost on slow first frames.
                    if (pending.length > 0) {
                        for (const chunk of pending) {
                            term.writeBytes(chunk);
                        }
                        pending = [];
                    }

                    // 4KB paste cap (SEC-REV-2 paste defence).
                    container.addEventListener('paste', (ev) => {
                        const data = ev.clipboardData && ev.clipboardData.getData('text');
                        if (data && data.length > 4096) {
                            ev.preventDefault();
                            this.warning = 'paste larger than 4KB was blocked';
                            setTimeout(() => { this.warning = ''; }, 4000);
                        }
                    }, true);
                },

                endSession() {
                    if (ws && ws.readyState !== WebSocket.CLOSED) {
                        ws.close(1000, 'user ended session');
                    }
                },

                resetToAuth() {
                    this.stage = 'auth';
                    this.error = '';
                    this.warning = '';
                    this.closeReason = '';
                },
            };

            function hexToBytes(hex) {
                const out = new Uint8Array(hex.length / 2);
                for (let i = 0; i < out.length; i++) {
                    out[i] = parseInt(hex.substr(i * 2, 2), 16);
                }
                return out;
            }
        }
    </script>
</x-filament-panels::page>
