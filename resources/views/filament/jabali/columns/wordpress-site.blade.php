@php
    $record = $getRecord();
    $domain = $record['domain'] . ($record['path'] ? '/' . $record['path'] : '');
    $siteUrl = $record['url'];
    $adminUrl = $siteUrl . '/wp-admin/';
    $pluginCount = $record['plugin_count'] ?? 0;
    $themeCount = $record['theme_count'] ?? 0;
    $version = $record['version'] ?? '';
@endphp

<div class="fi-ta-text flex flex-col gap-1">
    <div>
        <x-filament::link
            href="{{ $siteUrl }}"
            target="_blank"
            icon="heroicon-o-globe-alt"
            :tooltip="__('Open site')"
        >
            {{ $domain }}
        </x-filament::link>
    </div>
    <div>
        <x-filament::link
            tag="button"
            wire:click="autoLogin('{{ $record['id'] }}')"
            icon="heroicon-o-arrow-right-on-rectangle"
            :tooltip="__('Login to WordPress admin')"
        >
            {{ $adminUrl }}
        </x-filament::link>
    </div>
    <div class="fi-ta-text-item-label flex gap-1.5 pt-1">
        <x-filament::badge size="sm" color="gray" icon="heroicon-o-code-bracket">
            WP {{ $version }}
        </x-filament::badge>
        <x-filament::badge size="sm" color="gray" icon="heroicon-o-paint-brush">
            {{ __('Themes') }}: {{ $themeCount }}
        </x-filament::badge>
        <x-filament::badge size="sm" color="gray" icon="heroicon-o-puzzle-piece">
            {{ __('Plugins') }}: {{ $pluginCount }}
        </x-filament::badge>
    </div>
</div>
