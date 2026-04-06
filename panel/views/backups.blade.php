<x-filament-panels::page>
    <div class="fi-sc-tabs">
        <x-filament::tabs>
            <x-filament::tabs.item :active="$activeTab === 'destination'" wire:click="$set('activeTab', 'destination')" icon="heroicon-o-server-stack" :badge="count($destinations)" badge-color="primary">{{ __('Destination') }}</x-filament::tabs.item>
            <x-filament::tabs.item :active="$activeTab === 'schedule'" wire:click="$set('activeTab', 'schedule')" icon="heroicon-o-clock" :badge="count($schedules)" badge-color="primary">{{ __('Schedule') }}</x-filament::tabs.item>
            <x-filament::tabs.item :active="$activeTab === 'restore_download'" wire:click="$set('activeTab', 'restore_download')" icon="heroicon-o-arrow-down-tray">{{ __('Restore / Download') }}</x-filament::tabs.item>
            <x-filament::tabs.item :active="$activeTab === 'logs'" wire:click="$set('activeTab', 'logs')" icon="heroicon-o-document-text">{{ __('Logs') }}</x-filament::tabs.item>
            <x-filament::tabs.item :active="$activeTab === 'settings'" wire:click="$set('activeTab', 'settings')" icon="heroicon-o-cog-6-tooth">{{ __('Settings') }}</x-filament::tabs.item>
        </x-filament::tabs>

        <div class="fi-page-content mt-6" wire:loading.remove wire:target="activeTab">
        <x-tab-loading-skeleton>
            {{-- DESTINATION --}}
            @if($activeTab === 'destination')
                <div wire:key="tab-destination">
                {{ $this->table }}
                </div>
            @endif

            {{-- SCHEDULE --}}
            @if($activeTab === 'schedule')
                <div wire:key="tab-schedule">
                {{ $this->table }}
                </div>
            @endif

            {{-- RESTORE / DOWNLOAD --}}
            @if($activeTab === 'restore_download')
                <div wire:key="tab-restore-download">
                {{ $this->table }}
                </div>
            @endif

            {{-- LOGS --}}
            @if($activeTab === 'logs')
            <x-filament::section :heading="__('Backup Logs')" :description="__('Recent backup and restore activity.')" icon="heroicon-o-document-text">
                <x-slot name="afterHeader">
                    <x-filament::button wire:click="loadLogs" size="sm" color="gray" icon="heroicon-o-arrow-path">{{ __('Refresh') }}</x-filament::button>
                </x-slot>

                @if($logsOutput)
                <x-filament::section compact>
                    <code><pre>{{ $logsOutput }}</pre></code>
                </x-filament::section>
                @else
                    <p>{{ __('No log entries found.') }}</p>
                @endif
            </x-filament::section>
            @endif

            {{-- SETTINGS --}}
            @if($activeTab === 'settings')
            <x-filament::section :heading="__('Dependencies')" icon="heroicon-o-check-circle">
                <x-filament::section compact>
                    <code><pre>{{ $doctorOutput ?: __('Loading...') }}</pre></code>
                </x-filament::section>
            </x-filament::section>

            <x-filament::section :heading="__('Configuration')" icon="heroicon-o-cog-6-tooth">
                <x-filament::section compact>
                    <code><pre>{{ $configOutput ?: __('Loading...') }}</pre></code>
                </x-filament::section>
            </x-filament::section>

            <x-filament::section :heading="__('Maintenance')" icon="heroicon-o-wrench-screwdriver">
                <x-filament::button wire:click="runForget" wire:confirm="{{ __('Prune old snapshots?') }}" color="gray" icon="heroicon-o-archive-box-x-mark">{{ __('Apply Retention') }}</x-filament::button>
            </x-filament::section>
            @endif
        </x-tab-loading-skeleton>
        </div>
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
