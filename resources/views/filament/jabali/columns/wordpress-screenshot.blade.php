@php
    $record = $getRecord();
    $screenshotUrl = $this->getScreenshotUrl($record['id']);
@endphp

<div style="padding: 12px 0;">
    <div style="position: relative; width: 320px; height: 192px;">
        {{-- Screenshot Image --}}
        @if($screenshotUrl)
            <img
                src="{{ $screenshotUrl }}"
                alt="{{ $record['domain'] }}"
                style="position: absolute; top: 0; left: 0; width: 100%; height: 100%; object-fit: cover; object-position: top; border-radius: 12px; border: 1px solid rgba(229, 231, 235, 1);"
                class="dark:border-gray-700"
            />
        @else
            <div style="position: absolute; top: 0; left: 0; width: 100%; height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; border-radius: 12px; border: 1px solid rgba(229, 231, 235, 1);" class="bg-gray-100 dark:bg-gray-800 dark:border-gray-700 fi-color-gray fi-text-color-400">
                <x-filament::icon icon="heroicon-o-photo" :size="\Filament\Support\Enums\IconSize::TwoExtraLarge" class="mb-2" />
                <span class="fi-section-header-description">{{ __('No preview') }}</span>
            </div>
        @endif

        {{-- Screenshot Actions - Overlaid on Top Left --}}
        <div style="position: absolute; top: 8px; left: 8px; z-index: 50; display: flex; gap: 4px;">
            <x-filament::icon-button
                wire:click="captureScreenshot('{{ $record['id'] }}')"
                wire:loading.attr="disabled"
                wire:target="captureScreenshot('{{ $record['id'] }}')"
                icon="heroicon-o-camera"
                color="gray"
                size="sm"
                :tooltip="__('Capture Screenshot')"
                class="bg-white/90 dark:bg-gray-800/90 shadow-sm"
            />
            @if($screenshotUrl)
                <x-filament::icon-button
                    tag="a"
                    href="{{ $screenshotUrl }}"
                    download="screenshot-{{ $record['domain'] }}.png"
                    icon="heroicon-o-arrow-down-tray"
                    color="gray"
                    size="sm"
                    :tooltip="__('Download Screenshot')"
                    class="bg-white/90 dark:bg-gray-800/90 shadow-sm"
                />
            @endif
        </div>
    </div>
</div>
