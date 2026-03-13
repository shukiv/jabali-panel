<x-filament-panels::page>
    @once
        <style>
            /* Make support cards stretch and center the CTA vertically within the remaining space. */
            .support-card {
                display: flex;
                flex-direction: column;
                height: 100%;
            }

            .support-card > .fi-section-content-ctn {
                flex: 1 1 auto;
                display: flex;
                flex-direction: column;
            }

            .support-card > .fi-section-content-ctn > .fi-section-content {
                flex: 1 1 auto;
                display: flex;
                align-items: center;
                justify-content: center;
            }
        </style>
    @endonce

    <div class="grid gap-6 md:grid-cols-2 xl:grid-cols-4">
        <x-filament::section
            icon="heroicon-o-book-open"
            icon-color="primary"
            class="support-card"
        >
            <x-slot name="heading">{{ __('Documentation') }}</x-slot>
            <x-slot name="description">{{ __('Find answers in our docs or talk with our trainned support bot. Explore setup guides, troubleshooting steps, and best practices.') }}</x-slot>

            <div class="flex justify-center">
                <x-filament::button
                    tag="a"
                    href="https://jabali-panel.com/docs/"
                    target="_blank"
                    rel="noopener"
                    icon="heroicon-o-arrow-top-right-on-square"
                >
                    {{ __('Open Documentation') }}
                </x-filament::button>
            </div>
        </x-filament::section>

        <x-filament::section
            icon="heroicon-o-bug-ant"
            icon-color="warning"
            class="support-card"
        >
            <x-slot name="heading">{{ __('Report a Bug') }}</x-slot>
            <x-slot name="description">{{ __('To help us diagnose issues faster, click "Diagnostic Report" above to generate an encrypted report with your system info, service statuses, and recent logs. Paste it in your GitHub issue — only the Jabali team can read it.') }}</x-slot>

            <div class="flex justify-center">
                <x-filament::button
                    tag="a"
                    href="https://github.com/shukiv/jabali-panel/issues"
                    target="_blank"
                    rel="noopener"
                    icon="heroicon-o-arrow-top-right-on-square"
                    color="gray"
                >
                    {{ __('Open GitHub Issues') }}
                </x-filament::button>
            </div>
        </x-filament::section>

        <x-filament::section
            icon="heroicon-o-lifebuoy"
            icon-color="primary"
            class="support-card"
        >
            <x-slot name="heading">{{ __('Paid Support') }}</x-slot>
        <x-slot name="description">{{ __('Get professional assistance for migrations, performance tuning, and priority fixes. Plans include onboarding and dedicated support.') }}</x-slot>

            <div class="flex justify-center">
                <x-filament::button
                    tag="a"
                    href="https://jabali-panel.com/support/"
                    target="_blank"
                    rel="noopener"
                    icon="heroicon-o-arrow-top-right-on-square"
                >
                    {{ __('View Support Plans') }}
                </x-filament::button>
            </div>
        </x-filament::section>

        <x-filament::section
            icon="heroicon-o-clock"
            icon-color="gray"
            compact
            class="support-card"
        >
            <x-slot name="heading">{{ __('Emergency Support') }}</x-slot>
        <x-slot name="description">{{ __('We typically respond within 4-8 hours. For critical incidents, use Emergency Support for faster response.') }}</x-slot>

            <div class="flex justify-center">
                <x-filament::button
                    tag="a"
                    href="https://jabali-panel.com/emergency/"
                    target="_blank"
                    rel="noopener"
                    icon="heroicon-o-arrow-top-right-on-square"
                    color="warning"
                >
                    {{ __('Emergency Support') }}
                </x-filament::button>
            </div>
        </x-filament::section>
    </div>

    {{-- Diagnostic Report Modal --}}
    <div
        x-data="{ open: false, copied: false }"
        x-on:report-generated.window="open = true; copied = false"
    >
        <template x-teleport="body">
            <div
                x-show="open"
                x-transition.opacity
                x-cloak
                class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
                x-on:keydown.escape.window="open = false"
            >
                <div
                    x-on:click.away="open = false"
                    x-show="open"
                    x-transition
                    class="w-full max-w-2xl rounded-xl bg-white shadow-xl dark:bg-gray-900"
                >
                    <div class="flex items-center justify-between border-b border-gray-200 px-6 py-4 dark:border-gray-700">
                        <div>
                            <h2 class="text-base font-semibold text-gray-950 dark:text-white">
                                {{ __('Diagnostic Report') }}
                            </h2>
                            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                                {{ __('Copy the report below and paste it in your GitHub issue. It is encrypted — only the Jabali team can read it.') }}
                            </p>
                        </div>
                        <button x-on:click="open = false" class="text-gray-400 hover:text-gray-500 dark:hover:text-gray-300">
                            <x-heroicon-o-x-mark class="h-5 w-5" />
                        </button>
                    </div>

                    <div class="px-6 py-4">
                        <textarea
                            readonly
                            rows="14"
                            class="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 font-mono text-xs text-gray-700 dark:border-gray-600 dark:bg-gray-800 dark:text-gray-300"
                        >{{ $this->diagnosticReport }}</textarea>
                    </div>

                    <div class="flex items-center justify-end gap-3 border-t border-gray-200 px-6 py-4 dark:border-gray-700">
                        <button
                            x-on:click="open = false"
                            class="fi-btn fi-btn-size-md rounded-lg px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-800"
                        >
                            {{ __('Close') }}
                        </button>
                        <button
                            x-on:click="
                                navigator.clipboard.writeText($el.closest('.fixed').querySelector('textarea').value);
                                copied = true;
                                setTimeout(() => copied = false, 2000);
                            "
                            class="fi-btn fi-btn-size-md inline-flex items-center gap-1.5 rounded-lg bg-primary-600 px-4 py-2 text-sm font-medium text-white hover:bg-primary-500"
                        >
                            <template x-if="!copied">
                                <span class="flex items-center gap-1.5">
                                    <x-heroicon-o-clipboard-document class="h-4 w-4" />
                                    {{ __('Copy to Clipboard') }}
                                </span>
                            </template>
                            <template x-if="copied">
                                <span class="flex items-center gap-1.5">
                                    <x-heroicon-o-check class="h-4 w-4" />
                                    {{ __('Copied!') }}
                                </span>
                            </template>
                        </button>
                    </div>
                </div>
            </div>
        </template>
    </div>
</x-filament-panels::page>
