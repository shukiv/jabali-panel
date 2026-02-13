<x-filament-panels::page>
    <x-filament::section>
        <x-slot name="heading">
            {{ __('Jabali Panel Update') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Update the panel codebase and assets.') }}
        </x-slot>

        <div class="grid gap-4 md:grid-cols-3">
            <x-filament::section class="h-full" compact>
                <x-slot name="heading">{{ $currentVersion ?? 'unknown' }}</x-slot>
                <x-slot name="description">{{ __('Current Version') }}</x-slot>
            </x-filament::section>
            <x-filament::section class="h-full" compact>
                <x-slot name="heading">{{ $this->updateStatusLabel }}</x-slot>
                <x-slot name="description">{{ __('Status') }}</x-slot>
            </x-filament::section>
            <x-filament::section class="h-full" compact>
                <x-slot name="heading">{{ $lastCheckedAt ?? __('Not checked') }}</x-slot>
                <x-slot name="description">{{ __('Last Checked') }}</x-slot>
            </x-filament::section>
        </div>

        @if (! empty($recentChanges))
            <x-filament::section class="mt-4" compact>
                <div class="fi-section-header-heading">{{ __('Recent Changes') }}</div>
                <ul class="mt-2 list-inside list-disc space-y-1 fi-section-header-description">
                    @foreach ($recentChanges as $change)
                        <li>{{ $change }}</li>
                    @endforeach
                </ul>
            </x-filament::section>
        @endif

        <div class="mt-4 flex flex-wrap items-center gap-2">
            <x-filament::button
                icon="heroicon-o-magnifying-glass"
                color="gray"
                wire:click="checkForUpdates"
                wire:loading.attr="disabled"
                wire:target="checkForUpdates"
            >
                {{ __('Check for updates') }}
            </x-filament::button>

            <x-filament::button
                icon="heroicon-o-arrow-path-rounded-square"
                color="primary"
                wire:click="performUpgrade"
                wire:loading.attr="disabled"
                wire:target="performUpgrade"
            >
                {{ __('Upgrade now') }}
            </x-filament::button>
        </div>

        @if ($jabaliOutput !== null)
            <x-filament::section class="mt-4" collapsible>
                <x-slot name="heading">
                    {{ $jabaliOutputTitle ?? __('Jabali Update Output') }}
                </x-slot>
                @if ($jabaliOutputAt)
                    <x-slot name="afterHeader">
                        <span class="fi-section-header-description">
                            {{ __('Last run at :time', ['time' => $jabaliOutputAt]) }}
                        </span>
                    </x-slot>
                @endif
                <div class="fi-section-header-description">
                    <pre class="whitespace-pre-wrap break-words rounded bg-gray-50 p-4 fi-section-header-description">{{ $jabaliOutput }}</pre>
                </div>
            </x-filament::section>
        @endif
    </x-filament::section>

    <x-filament::section class="mt-6">
        <x-slot name="heading">{{ __('System Packages') }}</x-slot>
        <x-slot name="afterHeader">
            <div class="flex flex-wrap items-center gap-2">
                <x-filament::button
                    icon="heroicon-o-arrow-path"
                    color="gray"
                    wire:click="loadUpdates(true, true)"
                    wire:loading.attr="disabled"
                    wire:target="loadUpdates"
                >
                    {{ __('Refresh') }}
                </x-filament::button>

                <x-filament::button
                    icon="heroicon-o-arrow-path-rounded-square"
                    color="primary"
                    wire:click="runUpdates"
                    wire:loading.attr="disabled"
                    wire:target="runUpdates"
                >
                    {{ __('Run Updates') }}
                </x-filament::button>
            </div>
        </x-slot>

        @if ($refreshOutputAt)
            <x-filament::section class="mt-4" compact>
                <x-slot name="heading">{{ $refreshOutputTitle ?? __('Update Refresh Output') }}</x-slot>
                <x-slot name="afterHeader">
                    <span class="fi-section-header-description">
                        {{ __('Last run at :time', ['time' => $refreshOutputAt]) }}
                    </span>
                </x-slot>
                <div class="fi-section-header-description">
                    @php
                        $outputLines = is_array($refreshOutput) ? $refreshOutput : [$refreshOutput];
                    @endphp
                    <pre class="whitespace-pre-wrap break-words rounded bg-gray-50 p-4 fi-section-header-description">{{ implode("\n", $outputLines) }}</pre>
                </div>
            </x-filament::section>
        @endif
    </x-filament::section>

    {{ $this->table }}

    <x-filament-actions::modals />
</x-filament-panels::page>
