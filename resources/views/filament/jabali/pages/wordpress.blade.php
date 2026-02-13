<x-filament-panels::page>
    {{-- Info Banner --}}
    <x-filament::section
        icon="heroicon-o-information-circle"
        icon-color="info"
    >
        <x-slot name="heading">{{ __('Tip') }}</x-slot>
        <x-slot name="description">{{ __('Jabali Panel includes the Jabali Cache plugin for optimal caching performance. Enable it via the Cache toggle in your site\'s settings. After installing WordPress, complete the setup wizard, install security plugins, and keep everything updated.') }}</x-slot>
    </x-filament::section>

    {{ $this->table }}

    {{-- Security Scan Results Modal --}}
    <x-filament::modal
        id="security-scan-modal"
        wire:model="showSecurityScanModal"
        width="2xl"
        icon="heroicon-o-shield-check"
        :icon-color="count($securityScanResults['vulnerabilities'] ?? []) > 0 ? 'danger' : 'success'"
    >
        <x-slot name="heading">{{ __('Security Scan Results') }}</x-slot>
        <x-slot name="description">{{ $securityScanResults['url'] ?? '' }}</x-slot>

        {{-- Scan Info --}}
        <x-filament::section compact>
            <x-slot name="heading">{{ __('Scan Information') }}</x-slot>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 fi-section-header-description">
                <div>
                    <span class="fi-section-header-description">{{ __('Scanned URL') }}</span>
                    <div class="fi-section-header-heading">{{ $securityScanResults['url'] ?? __('Unknown') }}</div>
                </div>
                <div>
                    <span class="fi-section-header-description">{{ __('Scan Time') }}</span>
                    <div class="fi-section-header-heading">{{ $securityScanResults['scan_time'] ?? '' }}</div>
                </div>
            </div>
        </x-filament::section>

        @if(isset($securityScanResults['error']))
            <div class="p-4 rounded-lg bg-danger-50 dark:bg-danger-400/10 ring-1 ring-danger-600/10 dark:ring-danger-400/20">
                <div class="fi-section-header-heading fi-color-danger fi-text-color-600 dark:fi-text-color-400 mb-2">{{ __('Scan Error') }}</div>
                <div class="fi-section-header-description fi-color-danger fi-text-color-500">{{ $securityScanResults['error'] }}</div>
            </div>
        @else
            {{-- WordPress Version --}}
            @if(isset($securityScanResults['wordpress_version']))
                <x-filament::section compact>
                    <x-slot name="heading">{{ __('WordPress Version') }}</x-slot>
                    <div class="flex items-center gap-2">
                        <span class="fi-section-header-heading">{{ $securityScanResults['wordpress_version']['number'] }}</span>
                        @if(($securityScanResults['wordpress_version']['status'] ?? '') === 'insecure')
                            <x-filament::badge color="danger">{{ __('Insecure') }}</x-filament::badge>
                        @elseif(($securityScanResults['wordpress_version']['status'] ?? '') === 'outdated')
                            <x-filament::badge color="warning">{{ __('Outdated') }}</x-filament::badge>
                        @else
                            <x-filament::badge color="success">{{ __('Latest') }}</x-filament::badge>
                        @endif
                    </div>
                </x-filament::section>
            @endif

            {{-- Vulnerabilities --}}
            @if(count($securityScanResults['vulnerabilities'] ?? []) > 0)
                <x-filament::section
                    icon="heroicon-o-exclamation-triangle"
                    icon-color="danger"
                    compact
                >
                    <x-slot name="heading">{{ __('Vulnerabilities Found') }} ({{ count($securityScanResults['vulnerabilities']) }})</x-slot>
                    <div class="space-y-2">
                        @foreach($securityScanResults['vulnerabilities'] as $vuln)
                            <div class="p-3 rounded-lg bg-danger-50 dark:bg-danger-400/10 ring-1 ring-danger-600/10 dark:ring-danger-400/20">
                                <div class="fi-section-header-description fi-color-danger fi-text-color-600 dark:fi-text-color-400 mb-1">{{ $vuln['type'] }}</div>
                                <div class="fi-section-header-heading">{{ $vuln['title'] }}</div>
                                @if($vuln['fixed_in'])
                                    <div class="fi-section-header-description mt-1">{{ __('Fixed in:') }} {{ $vuln['fixed_in'] }}</div>
                                @endif
                            </div>
                        @endforeach
                    </div>
                </x-filament::section>
            @else
                <div class="p-6 rounded-lg bg-success-50 dark:bg-success-400/10 ring-1 ring-success-600/10 dark:ring-success-400/20 text-center">
                    <x-filament::icon icon="heroicon-o-check-circle" :size="\Filament\Support\Enums\IconSize::ExtraLarge" class="mx-auto mb-3 fi-color-success fi-text-color-500" />
                    <div class="fi-section-header-heading fi-color-success fi-text-color-600 dark:fi-text-color-400">{{ __('No Vulnerabilities Found') }}</div>
                    <div class="fi-section-header-description fi-color-success fi-text-color-500 mt-1">{{ __('Your WordPress site appears to be secure') }}</div>
                </div>
            @endif

            {{-- Interesting Findings --}}
            @if(count($securityScanResults['interesting_findings'] ?? []) > 0)
                <x-filament::section
                    icon="heroicon-o-eye"
                    collapsible
                    collapsed
                    compact
                >
                    <x-slot name="heading">{{ __('Interesting Findings') }}</x-slot>
                    <div class="space-y-1">
                        @foreach(array_slice($securityScanResults['interesting_findings'], 0, 10) as $finding)
                            <div class="p-2 fi-section-header-description rounded bg-gray-50 dark:bg-white/5">
                                {{ $finding['description'] }}
                            </div>
                        @endforeach
                    </div>
                </x-filament::section>
            @endif

            {{-- Detected Plugins --}}
            @if(count($securityScanResults['plugins'] ?? []) > 0)
                <x-filament::section
                    icon="heroicon-o-puzzle-piece"
                    collapsible
                    collapsed
                    compact
                >
                    <x-slot name="heading">{{ __('Detected Plugins') }} ({{ count($securityScanResults['plugins']) }})</x-slot>
                    <div class="flex flex-wrap gap-2">
                        @foreach($securityScanResults['plugins'] as $plugin)
                            <x-filament::badge color="gray">
                                {{ $plugin['name'] }} v{{ $plugin['version'] }}
                            </x-filament::badge>
                        @endforeach
                    </div>
                </x-filament::section>
            @endif
        @endif

        <x-slot name="footerActions">
            <x-filament::button wire:click="closeSecurityScanModal" color="gray">{{ __('Close') }}</x-filament::button>
        </x-slot>
    </x-filament::modal>

    <x-filament-actions::modals />

    @script
    <script>
        $wire.on('open-url', ({ url }) => {
            window.open(url, '_blank');
        });
    </script>
    @endscript
</x-filament-panels::page>
