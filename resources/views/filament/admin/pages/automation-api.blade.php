<x-filament-panels::page>
    @if($plainToken)
        <x-filament::section icon="heroicon-o-key" icon-color="success">
            <x-slot name="heading">{{ __('New Token') }}</x-slot>
            <x-slot name="description">{{ __('Copy this token now. You will not be able to see it again.') }}</x-slot>

            <div class="rounded-lg border border-gray-200 bg-white p-4 dark:border-white/10 dark:bg-white/5">
                <p class="fi-section-header-description">{{ __('API Token') }}</p>
                <p class="mt-2 break-all font-mono fi-section-header-heading">{{ $plainToken }}</p>
            </div>
        </x-filament::section>
    @endif

    <x-filament::section icon="heroicon-o-document-text" class="mt-6">
        <x-slot name="heading">{{ __('Automation API Endpoints') }}</x-slot>
        <x-slot name="description">{{ __('Use these endpoints with a token that has the automation ability.') }}</x-slot>

        <div class="space-y-2 fi-section-header-description">
            <div><span class="font-mono">{{ __('GET') }}</span> {{ __('/api/automation/users') }}</div>
            <div><span class="font-mono">{{ __('POST') }}</span> {{ __('/api/automation/users') }}</div>
            <div><span class="font-mono">{{ __('POST') }}</span> {{ __('/api/automation/domains') }}</div>
        </div>
    </x-filament::section>

    <div class="mt-6">
        {{ $this->table }}
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
