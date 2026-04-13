{{-- Watches the Filament restore_files toggle and shows/hides the file browser --}}
<div
    x-data="{
        get isOff() {
            const toggle = document.querySelector('[wire\\:model\\.live=\'mountedActionsData.0.restore_files\'], [wire\\:model=\'mountedActionsData.0.restore_files\']');
            if (toggle) return !toggle.checked;
            return !($wire.get('mountedActionsData.0.restore_files') ?? true);
        }
    }"
    x-init="$watch(() => $wire.get('mountedActionsData.0.restore_files'), () => $el.dispatchEvent(new Event('toggle-changed')))"
    @toggle-changed.window="$nextTick(() => {})"
>
    <template x-if="isOff">
        <div class="space-y-4">
            @include('filament.admin.pages.partials.restore-file-badges')
            @include('filament.admin.pages.partials.snapshot-browser-embed', [
                'snapshotId' => $snapshotId ?? 'latest',
                'username' => $username ?? '',
            ])
        </div>
    </template>
</div>
