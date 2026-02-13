@if($dsRecords && !empty($dsRecords['ds_records']))
    <div class="space-y-4">
        @foreach($dsRecords['ds_records'] as $type => $ds)
            <div class="p-4 bg-gray-50 dark:bg-gray-800 rounded-lg">
                <div class="flex items-center justify-between mb-2">
                    <span class="fi-section-header-heading">
                        {{ $ds['digest_name'] ?? $type }}
                        @if($loop->first)
                            <x-filament::badge color="success" class="ml-2">{{ __('Recommended') }}</x-filament::badge>
                        @endif
                    </span>
                    <x-filament::icon-button
                        icon="heroicon-o-clipboard-document"
                        color="gray"
                        size="sm"
                        x-on:click="navigator.clipboard.writeText('{{ addslashes($ds['record']) }}'); $tooltip('{{ __('Copied!') }}')"
                        :tooltip="__('Copy to clipboard')"
                    />
                </div>
                <code class="font-mono fi-section-header-description break-all block bg-gray-100 dark:bg-gray-900 p-2 rounded">{{ $ds['record'] }}</code>
                @if(isset($ds['parsed']))
                    <div class="mt-3 grid grid-cols-2 sm:grid-cols-4 gap-2">
                        <div>
                            <span class="fi-section-header-description">{{ __('Key Tag') }}:</span>
                            <span class="font-mono fi-section-header-description ml-1">{{ $ds['parsed']['key_tag'] }}</span>
                        </div>
                        <div>
                            <span class="fi-section-header-description">{{ __('Algorithm') }}:</span>
                            <span class="font-mono fi-section-header-description ml-1">{{ $ds['parsed']['algorithm'] }}</span>
                        </div>
                        <div>
                            <span class="fi-section-header-description">{{ __('Digest Type') }}:</span>
                            <span class="font-mono fi-section-header-description ml-1">{{ $ds['parsed']['digest_type'] }}</span>
                        </div>
                        <div>
                            <span class="fi-section-header-description">{{ __('Digest') }}:</span>
                            <span class="font-mono fi-section-header-description ml-1 truncate block" title="{{ $ds['parsed']['digest'] ?? '' }}">{{ Str::limit($ds['parsed']['digest'] ?? '', 12) }}</span>
                        </div>
                    </div>
                @endif
            </div>
        @endforeach
    </div>
    <div class="mt-4 p-3 bg-amber-50 dark:bg-amber-900/20 rounded-lg">
        <div class="flex items-start gap-2">
            <x-filament::icon icon="heroicon-o-information-circle" :size="\Filament\Support\Enums\IconSize::Large" class="fi-color-warning fi-text-color-500 shrink-0 mt-0.5" />
            <div class="fi-section-header-description fi-color-warning fi-text-color-700 dark:fi-text-color-300">
                <p class="fi-section-header-heading mb-1">{{ __('Important') }}</p>
                <p class="fi-section-header-description">{{ __('Add the DS record to your domain registrar (where you purchased the domain) to complete DNSSEC setup. DNS propagation may take up to 48 hours.') }}</p>
            </div>
        </div>
    </div>
@else
    <div class="text-center py-8">
        <x-filament::icon icon="heroicon-o-exclamation-triangle" :size="\Filament\Support\Enums\IconSize::TwoExtraLarge" class="mx-auto mb-4 fi-color-gray fi-text-color-400" />
        <p class="fi-section-header-description">{{ __('No DS records available.') }}</p>
        <p class="fi-section-header-description mt-2">{{ __('DNSSEC may not be properly configured for this domain.') }}</p>
    </div>
@endif
