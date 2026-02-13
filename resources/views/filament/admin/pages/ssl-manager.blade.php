<x-filament-panels::page>
    @if($autoSslLog)
        <x-filament::section
            icon="heroicon-o-command-line"
            icon-color="success"
            collapsible
        >
            <x-slot name="heading">{{ __('SSL Check Log') }}</x-slot>
            <x-slot name="headerEnd">
                <x-filament::icon-button
                    wire:click="$set('autoSslLog', '')"
                    icon="heroicon-o-x-mark"
                    color="gray"
                    size="sm"
                    tooltip="{{ __('Clear Log') }}"
                />
            </x-slot>
            <div class="rounded-lg overflow-hidden">
                <pre class="bg-gray-950 p-4 font-mono fi-section-header-description leading-relaxed max-h-80 overflow-y-auto whitespace-pre-wrap">{{ $autoSslLog }}</pre>
            </div>
        </x-filament::section>
    @endif

    {{ $this->table }}
</x-filament-panels::page>
