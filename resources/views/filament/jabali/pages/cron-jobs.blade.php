<x-filament-panels::page>
    {{-- Warning Banner --}}
    <x-filament::section
        icon="heroicon-o-exclamation-triangle"
        icon-color="warning"
        class="mb-6"
    >
        <x-slot name="heading">
            {{ __('Important: Cron Job Safety') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Cron jobs execute commands automatically on a schedule. Incorrect usage can cause server overload, send spam, fill disk space, or damage your data. Only add cron jobs if you fully understand what the command does.') }}
        </x-slot>
    </x-filament::section>

    {{ $this->table }}

    {{-- Help Section --}}
    <x-filament::section
        icon="heroicon-o-question-mark-circle"
        icon-color="gray"
        class="mt-6"
    >
        <x-slot name="heading">
            {{ __('Cron Schedule Reference') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Use these common schedule formats when creating custom cron jobs') }}
        </x-slot>

        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
            <div class="font-mono fi-section-header-description">
                <span class="fi-section-header-heading">* * * * *</span> = {{ __('Every minute') }}
            </div>
            <div class="font-mono fi-section-header-description">
                <span class="fi-section-header-heading">*/5 * * * *</span> = {{ __('Every 5 minutes') }}
            </div>
            <div class="font-mono fi-section-header-description">
                <span class="fi-section-header-heading">0 * * * *</span> = {{ __('Every hour') }}
            </div>
            <div class="font-mono fi-section-header-description">
                <span class="fi-section-header-heading">0 0 * * *</span> = {{ __('Daily at midnight') }}
            </div>
            <div class="font-mono fi-section-header-description">
                <span class="fi-section-header-heading">0 0 * * 0</span> = {{ __('Weekly (Sunday)') }}
            </div>
            <div class="font-mono fi-section-header-description">
                <span class="fi-section-header-heading">0 0 1 * *</span> = {{ __('Monthly (1st)') }}
            </div>
        </div>
        <p class="mt-4 fi-section-header-description">
            {{ __('Format:') }} <code class="px-1.5 py-0.5 rounded bg-gray-100 dark:bg-gray-800 fi-section-header-description">{{ __('minute hour day month weekday') }}</code>
        </p>
    </x-filament::section>

    <x-filament-actions::modals />
</x-filament-panels::page>
