<x-filament-panels::page>
    <div class="grid gap-6 md:grid-cols-2 xl:grid-cols-4">
        <x-filament::section
            icon="heroicon-o-book-open"
            icon-color="primary"
        >
            <x-slot name="heading">{{ __('Documentation') }}</x-slot>
            <x-slot name="description">{{ __('Find answers in our docs or talk with our trainned support bot. Explore setup guides, troubleshooting steps, and best practices.') }}</x-slot>

            <x-filament::button
                tag="a"
                href="https://jabali-panel.com/docs/"
                target="_blank"
                rel="noopener"
                icon="heroicon-o-arrow-top-right-on-square"
            >
                {{ __('Open Documentation') }}
            </x-filament::button>
        </x-filament::section>

        <x-filament::section
            icon="heroicon-o-bug-ant"
            icon-color="warning"
        >
            <x-slot name="heading">{{ __('Report a Bug') }}</x-slot>
            <x-slot name="description">{{ __('To help us diagnose issues faster, click "Diagnostic Report" above to generate an encrypted report with your system info, service statuses, and recent logs. Paste it in your GitHub issue — only the Jabali team can read it.') }}</x-slot>

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
        </x-filament::section>

        <x-filament::section
            icon="heroicon-o-lifebuoy"
            icon-color="primary"
        >
            <x-slot name="heading">{{ __('Paid Support') }}</x-slot>
            <x-slot name="description">{{ __('Get professional assistance for migrations, performance tuning, and priority fixes. Plans include onboarding and dedicated support.') }}</x-slot>

            <x-filament::button
                tag="a"
                href="https://jabali-panel.com/support/"
                target="_blank"
                rel="noopener"
                icon="heroicon-o-arrow-top-right-on-square"
            >
                {{ __('View Support Plans') }}
            </x-filament::button>
        </x-filament::section>

        <x-filament::section
            icon="heroicon-o-clock"
            icon-color="gray"
            compact
        >
            <x-slot name="heading">{{ __('Emergency Support') }}</x-slot>
            <x-slot name="description">{{ __('We typically respond within 4-8 hours. For critical incidents, use Emergency Support for faster response.') }}</x-slot>

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
        </x-filament::section>
    </div>
</x-filament-panels::page>
