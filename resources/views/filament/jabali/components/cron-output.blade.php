<div class="rounded-lg bg-gray-950 p-4 font-mono fi-section-header-description overflow-x-auto max-h-96 overflow-y-auto">
    @if($output)
        <pre class="whitespace-pre-wrap break-words">{{ $output }}</pre>
    @else
        <p class="fi-section-header-description italic">{{ __('No output recorded') }}</p>
    @endif
</div>
