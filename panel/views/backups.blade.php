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
                <div wire:key="tab-logs" class="space-y-4">
                    @include('filament.admin.pages.partials.log-tabs')
                    {{ $this->table }}
                </div>
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

            @if(! empty($stalwartStatus) && ($stalwartStatus['installed'] ?? false))
            <x-filament::section :heading="__('Stalwart Mail Backup')" icon="heroicon-o-inbox-stack">
                <div class="space-y-3">
                    <div class="flex items-center gap-3">
                        @if($stalwartStatus['enabled'] ?? false)
                            <x-filament::badge color="success">{{ __('Enabled') }}</x-filament::badge>
                        @else
                            <x-filament::badge color="gray">{{ __('Disabled') }}</x-filament::badge>
                        @endif
                        <span class="text-sm text-gray-500 dark:text-gray-400">{{ $stalwartStatus['url'] ?? '' }}</span>
                    </div>

                    <div class="flex items-center gap-2 text-sm">
                        <span>{{ __('Admin token') }}:</span>
                        @if($stalwartStatus['has_token'] ?? false)
                            <x-filament::badge color="success" size="sm">{{ __('Set') }}</x-filament::badge>
                        @else
                            <x-filament::badge color="danger" size="sm">{{ __('Missing') }}</x-filament::badge>
                        @endif

                        <span class="mx-2">|</span>

                        <span>stalwart-cli:</span>
                        @if($stalwartStatus['cli_available'] ?? false)
                            <x-filament::badge color="success" size="sm">{{ __('Available') }}</x-filament::badge>
                        @else
                            <x-filament::badge color="warning" size="sm">{{ __('Not found') }}</x-filament::badge>
                        @endif
                    </div>

                    <div class="flex gap-2">
                        <x-filament::button
                            wire:click="toggleStalwart"
                            :color="($stalwartStatus['enabled'] ?? false) ? 'gray' : 'primary'"
                            size="sm"
                            :icon="($stalwartStatus['enabled'] ?? false) ? 'heroicon-o-pause' : 'heroicon-o-play'"
                        >
                            {{ ($stalwartStatus['enabled'] ?? false) ? __('Disable') : __('Enable') }}
                        </x-filament::button>
                        <x-filament::button wire:click="testStalwartConnection" color="gray" size="sm" icon="heroicon-o-signal">{{ __('Test Connection') }}</x-filament::button>
                    </div>
                </div>
            </x-filament::section>
            @endif

            <x-filament::section :heading="__('Maintenance')" icon="heroicon-o-wrench-screwdriver">
                <x-filament::button wire:click="runForget" wire:confirm="{{ __('Prune old snapshots?') }}" color="gray" icon="heroicon-o-archive-box-x-mark">{{ __('Apply Retention') }}</x-filament::button>
            </x-filament::section>
            @endif
        </x-tab-loading-skeleton>
        </div>
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
