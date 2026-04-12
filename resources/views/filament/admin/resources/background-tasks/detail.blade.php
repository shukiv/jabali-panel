@php
    /** @var \App\Models\BackgroundTask $task */
    $log = file_exists($task->log_path)
        ? array_slice(file($task->log_path, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES), -50)
        : [];
@endphp

<div class="space-y-4">
    <dl class="grid grid-cols-2 gap-3 text-sm">
        <dt class="font-semibold">{{ __('Task ID') }}</dt>
        <dd class="font-mono text-xs">{{ $task->id }}</dd>

        <dt class="font-semibold">{{ __('Type') }}</dt>
        <dd>{{ $task->type->label() }}</dd>

        <dt class="font-semibold">{{ __('Status') }}</dt>
        <dd>{{ $task->status->label() }}</dd>

        <dt class="font-semibold">{{ __('Systemd unit') }}</dt>
        <dd class="font-mono text-xs">{{ $task->systemd_unit ?? '—' }}</dd>

        <dt class="font-semibold">{{ __('PID') }}</dt>
        <dd>{{ $task->pid ?? '—' }}</dd>

        <dt class="font-semibold">{{ __('Started') }}</dt>
        <dd>{{ $task->started_at?->toDateTimeString() ?? '—' }}</dd>

        <dt class="font-semibold">{{ __('Finished') }}</dt>
        <dd>{{ $task->finished_at?->toDateTimeString() ?? '—' }}</dd>

        @if ($task->error_message)
            <dt class="font-semibold">{{ __('Error') }}</dt>
            <dd class="text-red-600">{{ $task->error_message }}</dd>
        @endif
    </dl>

    <div>
        <h3 class="font-semibold mb-2">{{ __('Recent events (last 50)') }}</h3>
        <pre class="bg-gray-100 dark:bg-gray-800 p-3 rounded text-xs overflow-auto max-h-80">{{ implode("\n", $log) ?: __('No events yet') }}</pre>
    </div>
</div>
