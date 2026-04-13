@livewire('file-browser-widget', [
    'adapterClass' => \App\Backup\Adapters\ResticSnapshotAdapter::class,
    'adapterConfig' => ['snapshot_id' => $snapshotId, 'username' => $username],
    'readOnly' => true,
    'selectable' => true,
    'disabledFeatures' => ['view'],
], key('snbr-' . $username . '-' . $snapshotId . '-' . uniqid()))
