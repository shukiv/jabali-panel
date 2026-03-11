<div>
    @php
        $tasks = \App\Models\ImapSyncTask::where('user_id', auth()->id())
            ->with('mailbox.emailDomain')
            ->orderByDesc('created_at')
            ->limit(50)
            ->get();
    @endphp

    @if($tasks->isEmpty())
        <div class="text-center py-6">
            <x-heroicon-o-inbox class="mx-auto h-12 w-12 text-gray-400" />
            <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ __('No sync tasks yet') }}</p>
        </div>
    @else
        <div class="overflow-x-auto">
            <table class="w-full text-sm text-start">
                <thead>
                    <tr class="border-b border-gray-200 dark:border-gray-700">
                        <th class="px-3 py-2 text-start font-medium text-gray-600 dark:text-gray-400">{{ __('Source') }}</th>
                        <th class="px-3 py-2 text-start font-medium text-gray-600 dark:text-gray-400">{{ __('Destination') }}</th>
                        <th class="px-3 py-2 text-start font-medium text-gray-600 dark:text-gray-400">{{ __('Status') }}</th>
                        <th class="px-3 py-2 text-start font-medium text-gray-600 dark:text-gray-400">{{ __('Messages') }}</th>
                        <th class="px-3 py-2 text-start font-medium text-gray-600 dark:text-gray-400">{{ __('Date') }}</th>
                        <th class="px-3 py-2 text-start font-medium text-gray-600 dark:text-gray-400">{{ __('Actions') }}</th>
                    </tr>
                </thead>
                <tbody class="divide-y divide-gray-200 dark:divide-gray-700">
                    @foreach($tasks as $task)
                        <tr>
                            <td class="px-3 py-2">
                                <div class="font-medium">{{ $task->source_email }}</div>
                                <div class="text-xs text-gray-500">{{ $task->source_host }}:{{ $task->source_port }}</div>
                            </td>
                            <td class="px-3 py-2">
                                {{ $task->mailbox?->local_part }}@{{ $task->mailbox?->emailDomain?->domain_name }}
                            </td>
                            <td class="px-3 py-2">
                                @switch($task->status)
                                    @case('pending')
                                        <x-filament::badge color="gray">{{ __('Pending') }}</x-filament::badge>
                                        @break
                                    @case('running')
                                        <x-filament::badge color="warning">{{ __('Running') }}</x-filament::badge>
                                        @break
                                    @case('completed')
                                        <x-filament::badge color="success">{{ __('Completed') }}</x-filament::badge>
                                        @break
                                    @case('failed')
                                        <x-filament::badge color="danger">{{ __('Failed') }}</x-filament::badge>
                                        @break
                                    @case('cancelled')
                                        <x-filament::badge color="gray">{{ __('Cancelled') }}</x-filament::badge>
                                        @break
                                @endswitch
                            </td>
                            <td class="px-3 py-2">
                                @if($task->messages_total > 0)
                                    {{ $task->messages_synced }}/{{ $task->messages_total }}
                                @else
                                    -
                                @endif
                            </td>
                            <td class="px-3 py-2 text-xs text-gray-500">
                                {{ $task->created_at->diffForHumans() }}
                            </td>
                            <td class="px-3 py-2">
                                <div class="flex gap-1">
                                    @if(in_array($task->status, ['failed', 'cancelled']))
                                        <x-filament::button size="xs" color="gray" wire:click="retryTask({{ $task->id }})">
                                            {{ __('Retry') }}
                                        </x-filament::button>
                                    @endif
                                    @if(in_array($task->status, ['pending', 'running']))
                                        <x-filament::button size="xs" color="danger" wire:click="cancelTask({{ $task->id }})">
                                            {{ __('Cancel') }}
                                        </x-filament::button>
                                    @endif
                                </div>
                                @if($task->error_message)
                                    <div class="mt-1 text-xs text-danger-600 dark:text-danger-400">{{ Str::limit($task->error_message, 100) }}</div>
                                @endif
                            </td>
                        </tr>
                    @endforeach
                </tbody>
            </table>
        </div>
    @endif

    {{-- Status log for active sync --}}
    @if(!empty($this->statusLog))
        <div class="mt-4 rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-gray-700 dark:bg-gray-800">
            <h4 class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{ __('Sync Log') }}</h4>
            <div class="max-h-60 space-y-1 overflow-y-auto font-mono text-xs">
                @foreach($this->statusLog as $entry)
                    <div class="flex gap-2 @if(($entry['status'] ?? '') === 'error') text-danger-600 dark:text-danger-400 @elseif(($entry['status'] ?? '') === 'success') text-success-600 dark:text-success-400 @else text-gray-600 dark:text-gray-400 @endif">
                        <span class="shrink-0 text-gray-400">[{{ $entry['time'] ?? '' }}]</span>
                        <span>{{ $entry['message'] ?? '' }}</span>
                    </div>
                @endforeach
            </div>
        </div>
    @endif
</div>
