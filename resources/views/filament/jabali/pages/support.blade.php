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
            <x-slot name="heading">{{ __('GitHub Issues') }}</x-slot>
            <x-slot name="description">{{ __('Report bugs or request features. Include steps, logs, and screenshots so we can reproduce quickly.') }}</x-slot>

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
</x-filament-panels::page>
