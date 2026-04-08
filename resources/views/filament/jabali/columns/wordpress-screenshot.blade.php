@php
    $record = $getRecord();
    $screenshotUrl = $this->getScreenshotUrl($record['id']);
@endphp

<div style="padding: 12px 0;">
    <div style="position: relative; width: 320px; height: 192px; border-radius: 12px; overflow: hidden; border: 1px solid var(--gray-200);">
        @if($screenshotUrl)
            <img
                src="{{ $screenshotUrl }}"
                alt="{{ $record['domain'] }}"
                style="width: 100%; height: 100%; object-fit: cover; object-position: top;"
            />
        @else
            <div style="width: 100%; height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; background: var(--gray-100);">
                <x-filament::icon icon="heroicon-o-photo" :size="\Filament\Support\Enums\IconSize::TwoExtraLarge" style="margin-bottom: 8px; opacity: 0.4" />
                <span style="font-size: 0.875rem; opacity: 0.5">{{ __('No preview') }}</span>
            </div>
        @endif

        <div style="position: absolute; top: 8px; left: 8px; z-index: 50; display: flex; gap: 4px;">
            <x-filament::icon-button
                wire:click="captureScreenshot('{{ $record['id'] }}')"
                wire:loading.attr="disabled"
                wire:target="captureScreenshot('{{ $record['id'] }}')"
                icon="heroicon-o-camera"
                color="gray"
                size="sm"
                :tooltip="__('Capture Screenshot')"
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
                />
            @endif
        </div>
    </div>
</div>
