@php
    /** @var \Illuminate\Support\Collection<int, \App\Models\NotificationChannel> $channels */
    $channels = $this->getChannels();
@endphp

<div class="space-y-2" data-testid="channels-list">
    @if ($channels->isEmpty())
        <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ __('No channels configured yet. Save Admin Recipients below to auto-create a default email channel, or use "Manage channels" to add Telegram, ntfy, or Web Push destinations.') }}
        </p>
    @else
        <ul class="divide-y divide-gray-200 dark:divide-gray-700">
            @foreach ($channels as $channel)
                <li class="py-2 flex items-center justify-between" wire:key="notif-ch-{{ $channel->id }}">
                    <div class="flex items-center gap-3">
                        <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-primary-100 text-primary-800 dark:bg-primary-900 dark:text-primary-200">
                            {{ $channel->type }}
                        </span>
                        <span class="font-medium text-gray-900 dark:text-gray-100">{{ $channel->name }}</span>
                        @unless ($channel->enabled)
                            <span class="text-xs text-red-600 dark:text-red-400">{{ __('disabled') }}</span>
                        @endunless
                    </div>
                </li>
            @endforeach
        </ul>
    @endif
</div>
