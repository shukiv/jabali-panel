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
            <x-filament::section :heading="__('Backup Jobs')" :description="__('Recent backup and restore activity.')" icon="heroicon-o-document-text">
                <x-slot name="afterHeader">
                    <x-filament::button wire:click="loadLogs" size="sm" color="gray" icon="heroicon-o-arrow-path">{{ __('Refresh') }}</x-filament::button>
                </x-slot>

                @forelse($logJobs as $job)
                <div x-data="{ open: false }" class="border border-gray-200 dark:border-gray-700 rounded-xl mb-3 overflow-hidden">
                    {{-- Job header --}}
                    <button @click="open = !open" class="w-full flex items-center justify-between gap-3 px-4 py-3 text-left hover:bg-gray-50 dark:hover:bg-white/5 transition">
                        <div class="flex items-center gap-3 min-w-0">
                            @if($job['type'] === 'backup')
                                <x-heroicon-o-cloud-arrow-up class="w-5 h-5 text-gray-400 shrink-0" />
                            @else
                                <x-heroicon-o-arrow-path class="w-5 h-5 text-gray-400 shrink-0" />
                            @endif

                            <div class="min-w-0">
                                <div class="flex items-center gap-2 flex-wrap">
                                    <span class="font-medium text-sm text-gray-900 dark:text-gray-100">
                                        {{ ucfirst($job['type']) }}
                                    </span>
                                    @if($job['status'] === 'success')
                                        <x-filament::badge color="success" size="sm">{{ __('Success') }}</x-filament::badge>
                                    @elseif($job['status'] === 'partial')
                                        <x-filament::badge color="warning" size="sm">{{ __('Partial') }}</x-filament::badge>
                                    @elseif($job['status'] === 'failed')
                                        <x-filament::badge color="danger" size="sm">{{ __('Failed') }}</x-filament::badge>
                                    @else
                                        <x-filament::badge color="gray" size="sm">{{ __('Running') }}</x-filament::badge>
                                    @endif
                                    @if($job['errors'] > 0)
                                        <span class="text-xs text-danger-500">{{ $job['errors'] }} {{ __('error(s)') }}</span>
                                    @endif
                                </div>
                                <div class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
                                    {{ implode(', ', $job['accounts'] ?? []) ?: __('No accounts') }}
                                </div>
                            </div>
                        </div>

                        <div class="flex items-center gap-3 shrink-0">
                            <div class="text-right text-xs text-gray-500 dark:text-gray-400">
                                <div>{{ $job['started_at'] ?? '' }}</div>
                                @if(isset($job['duration_seconds']))
                                    <div>{{ $job['duration_seconds'] }}s</div>
                                @endif
                            </div>
                            <x-heroicon-o-chevron-down class="w-4 h-4 text-gray-400 transition-transform" ::class="open && 'rotate-180'" />
                        </div>
                    </button>

                    {{-- Expandable events + raw log --}}
                    <div x-show="open" x-cloak x-collapse>
                        <div x-data="{ showRaw: false }" class="border-t border-gray-200 dark:border-gray-700 px-4 py-3 space-y-2">
                            {{-- Human-readable events --}}
                            <div x-show="!showRaw" class="space-y-1">
                                @foreach($job['events'] ?? [] as $event)
                                    <div class="flex items-start gap-2 text-sm">
                                        @switch($event['level'] ?? 'info')
                                            @case('heading')
                                                <span class="font-semibold text-gray-900 dark:text-gray-100 mt-2 mb-0.5">{{ $event['text'] }}</span>
                                                @break
                                            @case('success')
                                                <span class="text-success-500 shrink-0">&#x2713;</span>
                                                <span class="text-gray-700 dark:text-gray-300">{{ $event['text'] }}</span>
                                                @break
                                            @case('error')
                                                <span class="text-danger-500 shrink-0">&#x2717;</span>
                                                <span class="text-danger-600 dark:text-danger-400">{{ $event['text'] }}</span>
                                                @break
                                            @case('warn')
                                                <span class="text-warning-500 shrink-0">&#x26A0;</span>
                                                <span class="text-warning-600 dark:text-warning-400">{{ $event['text'] }}</span>
                                                @break
                                            @case('skip')
                                                <span class="text-gray-400 shrink-0">&mdash;</span>
                                                <span class="text-gray-400">{{ $event['text'] }}</span>
                                                @break
                                            @default
                                                <span class="text-gray-400 shrink-0">&#x2022;</span>
                                                <span class="text-gray-600 dark:text-gray-400">{{ $event['text'] }}</span>
                                        @endswitch
                                    </div>
                                @endforeach
                                @if(empty($job['events']))
                                    <p class="text-sm text-gray-400">{{ __('No details available.') }}</p>
                                @endif
                            </div>

                            {{-- Raw log (toggle) --}}
                            <div x-show="showRaw" x-cloak>
                                <pre class="text-xs font-mono text-gray-600 dark:text-gray-300 whitespace-pre-wrap max-h-80 overflow-y-auto bg-gray-50 dark:bg-gray-900 rounded-lg p-3">{{ implode("\n", $job['log'] ?? []) }}</pre>
                            </div>

                            <button @click="showRaw = !showRaw" class="text-xs text-primary-500 hover:underline">
                                <span x-text="showRaw ? '{{ __('Show summary') }}' : '{{ __('Show raw log') }}'"></span>
                            </button>
                        </div>
                    </div>
                </div>
                @empty
                    <p class="text-sm text-gray-500">{{ __('No backup jobs found.') }}</p>
                @endforelse
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
